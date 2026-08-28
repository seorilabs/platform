// Package presence는 Cloud Platform이 발급하고 RPI Edge가 검증하는
// 비식별 최근 활성 token 계약을 소유한다.
package presence

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	Issuer            = "seorilabs-platform-presence"
	Audience          = "edge.vzyx.xyz"
	ScopeWrite        = "presence:write"
	DefaultTokenTTL   = time.Hour
	HeartbeatInterval = 60 * time.Second
	ActiveTTL         = 150 * time.Second
)

// Token은 검증을 마친 Edge 권한이다. 원본 session ID는 Cloud에서도 RPI에서도
// 저장하지 않고 이 SHA-256 key만 전달한다.
type Token struct {
	AppID       string
	SessionHash string
	ExpiresAt   time.Time
}

type claims struct {
	jwt.RegisteredClaims
	AppID       string `json:"app"`
	SessionHash string `json:"sid"`
	Scope       string `json:"scope"`
}

// IssuerService는 Edge 공개키에 대응하는 비공개키로 단기 token을 발급한다.
type IssuerService struct {
	privateKey ed25519.PrivateKey
	ttl        time.Duration
	now        func() time.Time
}

func NewIssuer(privateKey ed25519.PrivateKey, ttl time.Duration) (*IssuerService, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("presence: Ed25519 비공개키 길이가 올바르지 않다: %d", len(privateKey))
	}
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	return &IssuerService{privateKey: privateKey, ttl: ttl, now: time.Now}, nil
}

func (i *IssuerService) WithClock(now func() time.Time) *IssuerService {
	i.now = now
	return i
}

func (i *IssuerService) Issue(appID, sessionID string) (string, time.Time, error) {
	now := i.now().UTC()
	expiresAt := now.Add(i.ttl)
	sum := sha256.Sum256([]byte(appID + "\x00" + sessionID))
	tokenClaims := claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			Audience:  jwt.ClaimStrings{Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
		AppID:       appID,
		SessionHash: hex.EncodeToString(sum[:]),
		Scope:       ScopeWrite,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, tokenClaims).SignedString(i.privateKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("presence: token 서명 실패: %w", err)
	}
	return signed, expiresAt, nil
}

// Verifier는 RPI가 Cloud 호출 없이 token을 검증하게 한다.
type Verifier struct {
	publicKey ed25519.PublicKey
	now       func() time.Time
}

func NewVerifier(publicKey ed25519.PublicKey) (*Verifier, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("presence: Ed25519 공개키 길이가 올바르지 않다: %d", len(publicKey))
	}
	return &Verifier{publicKey: publicKey, now: time.Now}, nil
}

func (v *Verifier) WithClock(now func() time.Time) *Verifier {
	v.now = now
	return v
}

func (v *Verifier) Verify(raw string) (Token, error) {
	tokenClaims := &claims{}
	_, err := jwt.ParseWithClaims(raw, tokenClaims,
		func(*jwt.Token) (any, error) { return v.publicKey, nil },
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer(Issuer),
		jwt.WithAudience(Audience),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.now),
	)
	if err != nil {
		return Token{}, fmt.Errorf("presence: token 검증 실패: %w", err)
	}
	if tokenClaims.AppID == "" || tokenClaims.Scope != ScopeWrite ||
		len(tokenClaims.SessionHash) != sha256.Size*2 {
		return Token{}, errors.New("presence: token claim이 올바르지 않다")
	}
	if _, err := hex.DecodeString(tokenClaims.SessionHash); err != nil {
		return Token{}, errors.New("presence: session hash가 올바르지 않다")
	}
	return Token{
		AppID:       tokenClaims.AppID,
		SessionHash: tokenClaims.SessionHash,
		ExpiresAt:   tokenClaims.ExpiresAt.Time,
	}, nil
}

// ParsePrivateKey는 Secret Manager에 넣기 쉬운 PKCS#8 PEM과 base64 raw key를
// 모두 받는다. 키 값 자체는 오류나 로그에 넣지 않는다.
func ParsePrivateKey(raw string) (ed25519.PrivateKey, error) {
	trimmed := strings.TrimSpace(raw)
	if block, _ := pem.Decode([]byte(trimmed)); block != nil {
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("presence: PKCS#8 비공개키 해석 실패: %w", err)
		}
		key, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("presence: Ed25519 비공개키가 아니다")
		}
		return key, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("presence: 비공개키는 PKCS#8 PEM 또는 base64 Ed25519 key여야 한다")
	}
	return ed25519.PrivateKey(decoded), nil
}

func ParsePublicKey(raw string) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("presence: 공개키는 base64 Ed25519 key여야 한다")
	}
	return ed25519.PublicKey(decoded), nil
}
