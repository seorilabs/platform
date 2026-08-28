package identity

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// 세션 토큰 기본 수명.
//
// 1시간은 Firebase ID 토큰과 같다. 이 값이 revocation 지연의 상한이 된다.
// 계정을 차단해도 이미 발급된 세션은 최대 1시간 유효하다.
// 즉시성이 필요하면 레지스트리의 blocked_uids로 막는다.
const (
	DefaultSessionTTL = time.Hour
	DefaultRefreshTTL = 90 * 24 * time.Hour
)

const sessionIssuer = "seorilabs-platform"

// Session은 발급된 플랫폼 세션이다.
type Session struct {
	PlatformUserID  string
	AppID           string
	AppUserID       string
	IsAnonymous     bool
	IsLinkedAccount bool
	IssuedAt        time.Time
	ExpiresAt       time.Time
}

// sessionClaims는 세션 토큰의 내용이다.
type sessionClaims struct {
	jwt.RegisteredClaims

	AppID     string `json:"app"`
	AppUserID string `json:"auid"`
	Anonymous bool   `json:"anon"`
	Linked    bool   `json:"linked"`
}

// SessionIssuer는 플랫폼 세션 토큰을 발급하고 검증한다.
//
// 자체 발급이라 대칭키로 충분하다. 검증자가 우리뿐이므로
// 비대칭키의 이점인 "공개키만으로 검증"이 필요 없다.
type SessionIssuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewSessionIssuer는 발급기를 만든다.
//
// secret은 32바이트 이상이어야 한다. HS256의 키 길이가 해시 출력보다
// 짧으면 보안 강도가 떨어진다.
func NewSessionIssuer(secret []byte, ttl time.Duration) (*SessionIssuer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("identity: 세션 비밀키는 32바이트 이상이어야 한다 (현재 %d)", len(secret))
	}
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &SessionIssuer{secret: secret, ttl: ttl, now: time.Now}, nil
}

// WithClock은 시계를 주입한다. 테스트용이다.
func (s *SessionIssuer) WithClock(now func() time.Time) *SessionIssuer {
	s.now = now
	return s
}

// TTL은 세션 수명을 돌려준다.
func (s *SessionIssuer) TTL() time.Duration { return s.ttl }

// Issue는 세션 토큰을 발급한다.
func (s *SessionIssuer) Issue(sess Session) (string, time.Time, error) {
	now := s.now()
	exp := now.Add(s.ttl)

	claims := sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   sess.PlatformUserID,
			Issuer:    sessionIssuer,
			Audience:  jwt.ClaimStrings{sess.AppID},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		AppID:     sess.AppID,
		AppUserID: sess.AppUserID,
		Anonymous: sess.IsAnonymous,
		Linked:    sess.IsLinkedAccount,
	}

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("identity: 세션 토큰 서명 실패: %w", err)
	}
	return token, exp, nil
}

// Verify는 세션 토큰을 검증한다.
//
// appID를 함께 받아 audience를 확인한다. 다른 앱에서 발급된 세션으로
// 이 앱의 자원에 접근할 수 없어야 한다.
func (s *SessionIssuer) Verify(tokenStr, appID string) (Session, error) {
	claims := &sessionClaims{}

	_, err := jwt.ParseWithClaims(tokenStr, claims,
		func(*jwt.Token) (any, error) { return s.secret, nil },
		// 세션 토큰은 우리가 HS256으로 발급한다.
		// RS256을 허용하면 Firebase 토큰을 세션 토큰으로 쓸 수 있게 된다.
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(sessionIssuer),
		jwt.WithAudience(appID),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(clockSkew),
		jwt.WithTimeFunc(s.now),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return Session{}, platformerr.Wrap(err, platformerr.CodeSessionExpired,
				"세션이 만료됐어요. 다시 시도해 주세요")
		}
		return Session{}, platformerr.Wrap(err, platformerr.CodeSessionInvalid,
			"세션을 확인할 수 없어요")
	}

	if claims.Subject == "" || claims.AppID == "" {
		return Session{}, platformerr.New(platformerr.CodeSessionInvalid, "세션 내용이 올바르지 않아요")
	}

	var issued, expires time.Time
	if claims.IssuedAt != nil {
		issued = claims.IssuedAt.Time
	}
	if claims.ExpiresAt != nil {
		expires = claims.ExpiresAt.Time
	}

	return Session{
		PlatformUserID:  claims.Subject,
		AppID:           claims.AppID,
		AppUserID:       claims.AppUserID,
		IsAnonymous:     claims.Anonymous,
		IsLinkedAccount: claims.Linked,
		IssuedAt:        issued,
		ExpiresAt:       expires,
	}, nil
}

// EnsureNotAnonymous는 클라이언트가 임의로 만든 사칭 가능한 신원을 거부한다.
//
// Firebase anonymous 사용자는 이름 없는 사용자이지만 Firebase가 서명한 ID token으로
// 소유권을 증명한다. 반면 KindAnonymous의 `anon:` appUserId는 클라이언트가 아무 값이나
// 보낼 수 있어 타인 사칭이 가능하다. 민감 경로는 후자만 막는다.
// Obsidian 프로젝트/platform/03-architecture/identity.md 참고.
func (s Session) EnsureNotAnonymous() error {
	if s.IsAnonymous && strings.HasPrefix(s.AppUserID, "anon:") {
		return platformerr.New(platformerr.CodeAnonymousNotAllowed,
			"이 기능은 로그인이 필요해요")
	}
	return nil
}

// NewRefreshToken은 불투명 갱신 토큰을 만든다.
//
// JWT가 아니라 난수다. 갱신 토큰은 서버가 저장소에서 조회해 확인하므로
// 자체 내용을 담을 이유가 없고, 담지 않으면 유출 시 정보도 새지 않는다.
func NewRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("identity: 갱신 토큰 생성 실패: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
