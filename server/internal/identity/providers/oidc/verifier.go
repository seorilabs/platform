package oidc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

const (
	ProviderKakao = "kakao"
	ProviderApple = "apple"

	kakaoIssuer = "https://kauth.kakao.com"
	kakaoJWKS   = "https://kauth.kakao.com/.well-known/jwks.json"
	appleIssuer = "https://appleid.apple.com"
	appleJWKS   = "https://appleid.apple.com/auth/keys"
)

type nonceTransform func(string) string

type Verifier struct {
	provider       string
	issuer         string
	keys           *jwksCache
	nonceTransform nonceTransform
	now            func() time.Time
}

type Config struct {
	Provider       string
	Issuer         string
	JWKSURL        string
	Client         *http.Client
	NonceTransform func(string) string
	Now            func() time.Time
}

func New(config Config) (*Verifier, error) {
	if config.Provider == "" || config.Issuer == "" || config.JWKSURL == "" {
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"OIDC 로그인 공급자 설정이 올바르지 않아요")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.NonceTransform == nil {
		config.NonceTransform = func(value string) string { return value }
	}
	return &Verifier{
		provider: config.Provider, issuer: config.Issuer,
		keys:           newJWKSCache(config.JWKSURL, config.Client, config.Now),
		nonceTransform: config.NonceTransform, now: config.Now,
	}, nil
}

func NewKakao(client *http.Client) (*Verifier, error) {
	return New(Config{Provider: ProviderKakao, Issuer: kakaoIssuer, JWKSURL: kakaoJWKS, Client: client})
}

func NewApple(client *http.Client) (*Verifier, error) {
	return New(Config{
		Provider: ProviderApple, Issuer: appleIssuer, JWKSURL: appleJWKS, Client: client,
		NonceTransform: func(value string) string {
			sum := sha256.Sum256([]byte(value))
			return hex.EncodeToString(sum[:])
		},
	})
}

func (v *Verifier) Name() string { return v.provider }

type claims struct {
	jwt.RegisteredClaims
	Nonce string `json:"nonce"`
}

func (v *Verifier) Verify(
	ctx context.Context,
	idToken, nonce string,
	app registry.App,
) (string, error) {
	providerConfig, ok := app.Auth.AccountProviders[v.provider]
	if !ok || providerConfig.Audience == "" {
		return "", platformerr.New(platformerr.CodeAuthForbidden,
			"이 앱에서 사용할 수 없는 로그인 방식이에요")
	}
	parsedClaims := &claims{}
	token, err := jwt.ParseWithClaims(idToken, parsedClaims, func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, platformerr.New(platformerr.CodeAuthInvalid, "로그인 토큰에 서명 키가 없어요")
		}
		return v.keys.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(providerConfig.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(60*time.Second),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil || token == nil || !token.Valid {
		switch platformerr.CodeOf(err) {
		case platformerr.CodeProviderUnavailable, platformerr.CodeProviderResponseInvalid,
			platformerr.CodeProviderConfigInvalid:
			return "", err
		}
		return "", platformerr.Wrap(err, platformerr.CodeAuthInvalid,
			"로그인 토큰을 확인할 수 없어요")
	}
	if parsedClaims.IssuedAt == nil {
		return "", platformerr.New(platformerr.CodeAuthInvalid,
			"로그인 토큰에 발급 시각이 없어요")
	}
	if parsedClaims.Subject == "" || len(parsedClaims.Subject) > 255 {
		return "", platformerr.New(platformerr.CodeAuthInvalid,
			"로그인 토큰에 사용자 식별자가 없어요")
	}
	expectedNonce := v.nonceTransform(nonce)
	if len(parsedClaims.Nonce) != len(expectedNonce) ||
		subtle.ConstantTimeCompare([]byte(parsedClaims.Nonce), []byte(expectedNonce)) != 1 {
		return "", platformerr.New(platformerr.CodeAuthInvalid,
			"로그인 요청 nonce가 일치하지 않아요")
	}
	return parsedClaims.Subject, nil
}
