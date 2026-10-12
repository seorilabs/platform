package identity

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

func validRecoveryUID(uid string) bool {
	return uid != "" && len(uid) <= 128 && utf8.ValidString(uid) &&
		uid != "." && uid != ".." && !strings.Contains(uid, "/")
}

func (s *Service) BeginAccountLinkRecovery(ctx context.Context, sess Session, provider, expectedAppUserID string) (AccountLinkChallenge, error) {
	if err := sess.EnsureNotAnonymous(); err != nil {
		return AccountLinkChallenge{}, err
	}
	if !validRecoveryUID(expectedAppUserID) {
		return AccountLinkChallenge{}, platformerr.New(platformerr.CodeRequestInvalid, "대상 계정 정보가 올바르지 않아요")
	}
	provider = strings.TrimSpace(provider)
	if _, _, err := s.accountProvider(ctx, sess.AppID, provider); err != nil {
		return AccountLinkChallenge{}, err
	}
	if err := s.ensureNotBlocked(ctx, sess.AppID, sess.AppUserID); err != nil {
		return AccountLinkChallenge{}, err
	}
	nonce, err := newAccountLinkNonce()
	if err != nil {
		return AccountLinkChallenge{}, platformerr.Wrap(err, platformerr.CodePlatformUnavailable, "로그인 요청을 만들지 못했어요")
	}
	expiresAt := s.now().UTC().Add(accountLinkChallengeTTL)
	if err := s.accounts.CreateAccountLinkRecoveryChallenge(ctx, sess.AppID, sess.PlatformUserID, provider, nonce, expectedAppUserID, expiresAt); err != nil {
		return AccountLinkChallenge{}, err
	}
	return AccountLinkChallenge{Provider: provider, Nonce: nonce, ExpiresAt: expiresAt}, nil
}

func (s *Service) RecoverAccountLink(ctx context.Context, sess Session, provider, idToken, nonce, expectedAppUserID string) (AccountLinkResult, error) {
	if err := sess.EnsureNotAnonymous(); err != nil {
		return AccountLinkResult{}, err
	}
	if !validRecoveryUID(expectedAppUserID) {
		return AccountLinkResult{}, platformerr.New(platformerr.CodeRequestInvalid, "대상 계정 정보가 올바르지 않아요")
	}
	if strings.TrimSpace(idToken) == "" || len(idToken) > 8192 || nonce == "" || len(nonce) > 128 {
		return AccountLinkResult{}, platformerr.New(platformerr.CodeAuthInvalid, "로그인 정보가 올바르지 않아요")
	}
	provider = strings.TrimSpace(provider)
	app, verifier, err := s.accountProvider(ctx, sess.AppID, provider)
	if err != nil {
		return AccountLinkResult{}, err
	}
	if s.customTokens == nil {
		return AccountLinkResult{}, platformerr.New(platformerr.CodePlatformUnavailable, "Firebase 인증 bridge를 사용할 수 없어요")
	}
	if err := s.ensureNotBlocked(ctx, app.AppID, sess.AppUserID); err != nil {
		return AccountLinkResult{}, err
	}
	subject, err := verifier.Verify(ctx, idToken, nonce, app)
	if err != nil {
		return AccountLinkResult{}, err
	}
	connected, err := s.accounts.RecoverAccount(ctx, app.AppID, sess.PlatformUserID, provider, subject, nonce, expectedAppUserID, s.now().UTC())
	if err != nil {
		return AccountLinkResult{}, err
	}
	// 외부 mint를 재실행 가능한 Firestore transaction 안에서 호출하지 않는다.
	// 해제/삭제 우선 여부는 저장소 commit에서 판정하고 발급 직전도 차단한다.
	if err := s.ensureNotBlocked(ctx, app.AppID, sess.AppUserID); err != nil {
		return AccountLinkResult{}, err
	}
	if err := s.ensureNotBlocked(ctx, app.AppID, connected.AppUserID); err != nil {
		return AccountLinkResult{}, err
	}
	customToken, err := s.customTokens.Mint(ctx, app, connected.AppUserID, false)
	if err != nil {
		return AccountLinkResult{}, platformerr.Wrap(err, platformerr.CodePlatformUnavailable, "Firebase 인증 토큰을 만들지 못했어요")
	}
	issued, err := s.issue(ctx, Session{PlatformUserID: connected.PlatformUserID, AppID: app.AppID, AppUserID: connected.AppUserID, IsLinkedAccount: true})
	if err != nil {
		return AccountLinkResult{}, err
	}
	return AccountLinkResult{Session: issued, FirebaseCustomToken: customToken, Provider: connected.Provider, Restored: connected.Restored}, nil
}
