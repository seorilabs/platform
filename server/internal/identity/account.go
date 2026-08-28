package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

const accountLinkChallengeTTL = 5 * time.Minute

// AccountProvider는 외부 OIDC 공급자 검증 어댑터다.
// 구현은 providers/oidc에 있고 유스케이스는 HTTP, JWK 형식을 모른다.
type AccountProvider interface {
	Name() string
	Verify(ctx context.Context, idToken, nonce string, app registry.App) (subject string, err error)
}

// AccountRepository는 계정 연결 challenge와 공급자 subject 매핑을 보관한다.
// subject 원문은 구현 안에서 즉시 해시하고 영구 저장하지 않는다.
type AccountRepository interface {
	CreateAccountLinkChallenge(
		ctx context.Context,
		appID, platformUserID, provider, nonce string,
		expiresAt time.Time,
	) error
	ConnectAccount(
		ctx context.Context,
		appID, currentPlatformUserID, provider, subject, nonce string,
		now time.Time,
	) (ConnectedAccount, error)
	DisconnectAccount(ctx context.Context, appID, provider, subject string) error
	IsAccountLinked(ctx context.Context, appID, platformUserID string) (bool, error)
}

type ConnectedAccount struct {
	PlatformUserID string
	AppUserID      string
	Provider       string
	Restored       bool
}

type AccountLinkChallenge struct {
	Provider  string
	Nonce     string
	ExpiresAt time.Time
}

type AccountLinkResult struct {
	Session             Result
	FirebaseCustomToken string
	Provider            string
	Restored            bool
}

// ConfigureAccountProviders는 외부 계정 연결 포트와 어댑터를 조립한다.
func (s *Service) ConfigureAccountProviders(repo AccountRepository, providers ...AccountProvider) error {
	if repo == nil || len(providers) == 0 {
		return platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"계정 연결 서비스 설정이 올바르지 않아요")
	}
	configured := make(map[string]AccountProvider, len(providers))
	for _, provider := range providers {
		if provider == nil || strings.TrimSpace(provider.Name()) == "" {
			return platformerr.New(platformerr.CodeRuntimeConfigInvalid,
				"계정 공급자 설정이 올바르지 않아요")
		}
		if _, exists := configured[provider.Name()]; exists {
			return platformerr.New(platformerr.CodeRuntimeConfigInvalid,
				"계정 공급자가 중복됐어요")
		}
		configured[provider.Name()] = provider
	}
	s.accounts = repo
	s.accountProviders = configured
	return nil
}

func (s *Service) BeginAccountLink(
	ctx context.Context,
	sess Session,
	provider string,
) (AccountLinkChallenge, error) {
	if err := sess.EnsureNotAnonymous(); err != nil {
		return AccountLinkChallenge{}, err
	}
	provider = strings.TrimSpace(provider)
	_, _, err := s.accountProvider(ctx, sess.AppID, provider)
	if err != nil {
		return AccountLinkChallenge{}, err
	}

	nonce, err := newAccountLinkNonce()
	if err != nil {
		return AccountLinkChallenge{}, platformerr.Wrap(err, platformerr.CodePlatformUnavailable,
			"로그인 요청을 만들지 못했어요")
	}
	expiresAt := s.now().UTC().Add(accountLinkChallengeTTL)
	if err := s.accounts.CreateAccountLinkChallenge(
		ctx, sess.AppID, sess.PlatformUserID, provider, nonce, expiresAt,
	); err != nil {
		return AccountLinkChallenge{}, err
	}
	return AccountLinkChallenge{Provider: provider, Nonce: nonce, ExpiresAt: expiresAt}, nil
}

func (s *Service) CompleteAccountLink(
	ctx context.Context,
	sess Session,
	provider, idToken, nonce string,
) (AccountLinkResult, error) {
	if err := sess.EnsureNotAnonymous(); err != nil {
		return AccountLinkResult{}, err
	}
	idToken = strings.TrimSpace(idToken)
	nonce = strings.TrimSpace(nonce)
	provider = strings.TrimSpace(provider)
	if idToken == "" || len(idToken) > 8192 || nonce == "" || len(nonce) > 128 {
		return AccountLinkResult{}, platformerr.New(platformerr.CodeAuthInvalid,
			"로그인 정보가 올바르지 않아요")
	}
	app, verifier, err := s.accountProvider(ctx, sess.AppID, provider)
	if err != nil {
		return AccountLinkResult{}, err
	}
	subject, err := verifier.Verify(ctx, idToken, nonce, app)
	if err != nil {
		return AccountLinkResult{}, err
	}
	connected, err := s.accounts.ConnectAccount(
		ctx, app.AppID, sess.PlatformUserID, provider, subject, nonce, s.now().UTC(),
	)
	if err != nil {
		return AccountLinkResult{}, err
	}
	if s.customTokens == nil {
		return AccountLinkResult{}, platformerr.New(platformerr.CodePlatformUnavailable,
			"Firebase 인증 bridge를 사용할 수 없어요")
	}
	customToken, err := s.customTokens.Mint(ctx, app, connected.AppUserID, false)
	if err != nil {
		return AccountLinkResult{}, platformerr.Wrap(err, platformerr.CodePlatformUnavailable,
			"Firebase 인증 토큰을 만들지 못했어요")
	}
	issued, err := s.issue(ctx, Session{
		PlatformUserID:  connected.PlatformUserID,
		AppID:           app.AppID,
		AppUserID:       connected.AppUserID,
		IsLinkedAccount: true,
	})
	if err != nil {
		return AccountLinkResult{}, err
	}
	return AccountLinkResult{
		Session: issued, FirebaseCustomToken: customToken,
		Provider: connected.Provider, Restored: connected.Restored,
	}, nil
}

// DisconnectExternalAccount는 공급자 webhook이 증명한 subject 매핑을 끊는다.
// provider subject 원문은 저장소 경계에서 즉시 해시되고 영구 저장되지 않는다.
func (s *Service) DisconnectExternalAccount(
	ctx context.Context,
	appID, provider, subject string,
) error {
	appID = strings.TrimSpace(appID)
	provider = strings.TrimSpace(provider)
	subject = strings.TrimSpace(subject)
	if s.accounts == nil {
		return platformerr.New(platformerr.CodePlatformUnavailable,
			"계정 연결 서비스가 준비되지 않았어요")
	}
	if appID == "" || provider == "" || subject == "" {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"연결 해제 정보가 올바르지 않아요")
	}
	return s.accounts.DisconnectAccount(ctx, appID, provider, subject)
}

func (s *Service) accountProvider(
	ctx context.Context,
	appID, provider string,
) (registry.App, AccountProvider, error) {
	if s.accounts == nil || len(s.accountProviders) == 0 {
		return registry.App{}, nil, platformerr.New(platformerr.CodePlatformUnavailable,
			"계정 연결 서비스가 준비되지 않았어요")
	}
	app, err := s.registry.GetUsable(ctx, appID)
	if err != nil {
		return registry.App{}, nil, err
	}
	provider = strings.TrimSpace(provider)
	if _, allowed := app.Auth.AccountProviders[provider]; !allowed {
		return registry.App{}, nil, platformerr.New(platformerr.CodeAuthForbidden,
			"이 앱에서 사용할 수 없는 로그인 방식이에요")
	}
	verifier, ok := s.accountProviders[provider]
	if !ok {
		return registry.App{}, nil, platformerr.New(platformerr.CodePlatformUnavailable,
			"로그인 공급자가 준비되지 않았어요")
	}
	return app, verifier, nil
}

func newAccountLinkNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
