package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

const (
	appleAuthBaseURL       = "https://appleid.apple.com"
	appleClientSecretTTL   = 5 * time.Minute
	appleResponseMaxBytes  = 64 * 1024
	appleAuthorizationCode = 1024
)

// AppleSignInKey는 Sign in with Apple REST API용 앱별 키다. 개인키는 Secret Manager에서만
// 들어오고 로그·응답에 남기지 않는다. ClientID는 레지스트리 apple audience(번들 ID)와 같아야 한다.
type AppleSignInKey struct {
	ClientID   string
	TeamID     string
	KeyID      string
	PrivateKey *ecdsa.PrivateKey
}

// AppleGrant는 authorization code 교환 결과에서 계정 삭제가 쓰는 값만 담는다.
type AppleGrant struct {
	Subject      string
	RefreshToken string
}

// AppleAuthorizationRevoker는 계정 삭제 때 사용자가 다시 승인한 Sign in with Apple
// 권한을 철회한다. App Store 심사 지침 5.1.1(v)의 토큰 철회 요구를 따른다.
type AppleAuthorizationRevoker interface {
	Configured(appID string) bool
	Exchange(ctx context.Context, app registry.App, code string) (AppleGrant, error)
	Revoke(ctx context.Context, app registry.App, refreshToken string) error
}

// AppleTokenRevoker는 Apple의 /auth/token, /auth/revoke를 호출한다.
type AppleTokenRevoker struct {
	keys    map[string]AppleSignInKey
	client  *http.Client
	baseURL string
	now     func() time.Time
}

type appleKeyJSON struct {
	ClientID   string `json:"clientId"`
	TeamID     string `json:"teamId"`
	KeyID      string `json:"keyId"`
	PrivateKey string `json:"privateKey"`
}

// ParseAppleSignInKeys는 {"<appId>": {"clientId","teamId","keyId","privateKey"}} 형식을 읽는다.
func ParseAppleSignInKeys(raw []byte) (map[string]AppleSignInKey, error) {
	var entries map[string]appleKeyJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, errors.New("apple sign-in keys: JSON 형식이 아니다")
	}
	keys := make(map[string]AppleSignInKey, len(entries))
	for appID, e := range entries {
		if appID == "" || e.ClientID == "" || e.TeamID == "" || e.KeyID == "" {
			return nil, fmt.Errorf("apple sign-in keys: %q 항목이 비었다", appID)
		}
		block, _ := pem.Decode([]byte(e.PrivateKey))
		if block == nil {
			return nil, fmt.Errorf("apple sign-in keys: %q 개인키가 PEM이 아니다", appID)
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("apple sign-in keys: %q 개인키를 읽지 못했다", appID)
		}
		key, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("apple sign-in keys: %q 개인키가 EC가 아니다", appID)
		}
		keys[appID] = AppleSignInKey{ClientID: e.ClientID, TeamID: e.TeamID, KeyID: e.KeyID, PrivateKey: key}
	}
	return keys, nil
}

func NewAppleTokenRevoker(keys map[string]AppleSignInKey, client *http.Client) *AppleTokenRevoker {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &AppleTokenRevoker{keys: keys, client: client, baseURL: appleAuthBaseURL, now: time.Now}
}

func (r *AppleTokenRevoker) Configured(appID string) bool {
	_, ok := r.keys[appID]
	return ok
}

func (r *AppleTokenRevoker) key(app registry.App) (AppleSignInKey, error) {
	key, ok := r.keys[app.AppID]
	if !ok {
		return AppleSignInKey{}, platformerr.New(platformerr.CodePlatformUnavailable, "Apple 계정 처리가 준비되지 않았어요")
	}
	if key.ClientID != app.Auth.AccountProviders["apple"].Audience {
		return AppleSignInKey{}, platformerr.New(platformerr.CodePlatformUnavailable, "Apple 계정 설정이 앱과 맞지 않아요")
	}
	return key, nil
}

func (r *AppleTokenRevoker) clientSecret(key AppleSignInKey) (string, error) {
	now := r.now()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{
		Issuer:    key.TeamID,
		Subject:   key.ClientID,
		Audience:  jwt.ClaimStrings{appleAuthBaseURL},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(appleClientSecretTTL)),
	})
	token.Header["kid"] = key.KeyID
	return token.SignedString(key.PrivateKey)
}

func (r *AppleTokenRevoker) post(ctx context.Context, path string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, appleResponseMaxBytes))
	return resp.StatusCode, body, err
}

// Exchange는 앱이 방금 받은 authorization code를 토큰으로 바꾸고 계정 subject를 돌려준다.
// id_token은 Apple 토큰 엔드포인트가 TLS로 직접 준 응답이라 서명 대신 iss·aud만 대조한다
// (OpenID Connect Core 3.1.3.7).
func (r *AppleTokenRevoker) Exchange(ctx context.Context, app registry.App, code string) (AppleGrant, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > appleAuthorizationCode {
		return AppleGrant{}, platformerr.New(platformerr.CodeAuthInvalid, "Apple 인증 정보가 올바르지 않아요")
	}
	key, err := r.key(app)
	if err != nil {
		return AppleGrant{}, err
	}
	secret, err := r.clientSecret(key)
	if err != nil {
		return AppleGrant{}, platformerr.Wrap(err, platformerr.CodePlatformUnavailable, "Apple 요청을 만들지 못했어요")
	}
	status, body, err := r.post(ctx, "/auth/token", url.Values{
		"client_id": {key.ClientID}, "client_secret": {secret},
		"code": {code}, "grant_type": {"authorization_code"},
	})
	if err != nil {
		return AppleGrant{}, platformerr.Wrap(err, platformerr.CodeProviderAuthFailed, "Apple에 연결하지 못했어요")
	}
	if status == http.StatusBadRequest {
		// invalid_grant: 만료(5분)·재사용된 code. 앱이 다시 승인을 받아야 한다.
		return AppleGrant{}, platformerr.New(platformerr.CodeAccountReauthRequired, "Apple 인증을 다시 해 주세요")
	}
	if status != http.StatusOK {
		return AppleGrant{}, platformerr.Newf(platformerr.CodeProviderAuthFailed, "Apple 인증 교환이 실패했어요(%d)", status)
	}
	var tokens struct {
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tokens); err != nil || tokens.RefreshToken == "" || tokens.IDToken == "" {
		return AppleGrant{}, platformerr.New(platformerr.CodeProviderAuthFailed, "Apple 인증 응답이 올바르지 않아요")
	}
	subject, err := appleIDTokenSubject(tokens.IDToken, key.ClientID)
	if err != nil {
		return AppleGrant{}, err
	}
	return AppleGrant{Subject: subject, RefreshToken: tokens.RefreshToken}, nil
}

func appleIDTokenSubject(idToken, clientID string) (string, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", platformerr.New(platformerr.CodeProviderAuthFailed, "Apple 인증 응답이 올바르지 않아요")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", platformerr.New(platformerr.CodeProviderAuthFailed, "Apple 인증 응답이 올바르지 않아요")
	}
	var claims struct {
		Issuer   string           `json:"iss"`
		Subject  string           `json:"sub"`
		Audience jwt.ClaimStrings `json:"aud"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Issuer != appleAuthBaseURL || claims.Subject == "" {
		return "", platformerr.New(platformerr.CodeProviderAuthFailed, "Apple 인증 응답이 올바르지 않아요")
	}
	for _, aud := range claims.Audience {
		if aud == clientID {
			return claims.Subject, nil
		}
	}
	return "", platformerr.New(platformerr.CodeProviderAuthFailed, "Apple 인증 응답의 앱이 달라요")
}

// Revoke는 refresh token을 철회해 사용자 Apple ID에서 앱 승인을 지운다.
func (r *AppleTokenRevoker) Revoke(ctx context.Context, app registry.App, refreshToken string) error {
	key, err := r.key(app)
	if err != nil {
		return err
	}
	secret, err := r.clientSecret(key)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodePlatformUnavailable, "Apple 요청을 만들지 못했어요")
	}
	status, _, err := r.post(ctx, "/auth/revoke", url.Values{
		"client_id": {key.ClientID}, "client_secret": {secret},
		"token": {refreshToken}, "token_type_hint": {"refresh_token"},
	})
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderAuthFailed, "Apple에 연결하지 못했어요")
	}
	if status != http.StatusOK {
		return platformerr.Newf(platformerr.CodeProviderAuthFailed, "Apple 승인 철회가 실패했어요(%d)", status)
	}
	return nil
}
