package identity

import (
	"context"
	"crypto/rsa"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// clockSkew는 서버 간 시계 오차 허용치다.
const clockSkew = 60 * time.Second

// maxSubjectLen은 sub claim 길이 상한이다.
// Firebase uid는 128자를 넘지 않는다.
const maxSubjectLen = 128

// Claims는 검증을 통과한 Firebase ID 토큰의 내용이다.
type Claims struct {
	// UID는 Firebase 사용자 식별자다. sub claim이다.
	UID string
	// SignInProvider는 anonymous, google.com, apple.com 등이다.
	SignInProvider string
	// AITUserKey는 AppsInToss 사용자 키다. custom claim이며 없을 수 있다.
	//
	// AIT 결제는 이 값을 마켓 API의 x-toss-user-key 헤더로 넘긴다.
	// body로 받지 않는 이유는 클라이언트가 타인 값을 넣을 수 있기 때문이다.
	AITUserKey string
	// IsAnonymous는 익명 로그인 여부다.
	IsAnonymous bool
	// AuthTime은 사용자가 실제로 인증한 시각이다.
	AuthTime time.Time
	// ExpiresAt은 토큰 만료 시각이다.
	ExpiresAt time.Time
}

// firebaseClaims는 JWT 파싱용 내부 타입이다.
type firebaseClaims struct {
	jwt.RegisteredClaims

	AuthTime int64 `json:"auth_time"`

	Firebase struct {
		SignInProvider string `json:"sign_in_provider"`
	} `json:"firebase"`

	AITUserKey string `json:"aitUserKey"`
}

// KeyProvider는 kid로 공개키를 찾는다.
//
// 인터페이스를 여기에 두는 이유는 FirebaseVerifier가 소비자이기 때문이다.
// KeyCache가 구현하고, 테스트는 고정 키를 주는 fake를 쓴다.
type KeyProvider interface {
	Key(ctx context.Context, kid string) (*rsa.PublicKey, error)
}

// FirebaseVerifier는 Firebase ID 토큰을 검증한다.
//
// Firebase Admin SDK를 쓰지 않는다. Admin SDK는 프로젝트별 App 인스턴스를
// 요구하는데 우리는 앱이 16개다. 인스턴스 16개는 메모리와 콜드스타트 낭비다.
// 서명키가 전 프로젝트 공통이므로 직접 검증하는 편이 단순하다.
type FirebaseVerifier struct {
	keys KeyProvider
	// baseOpts는 모든 검증에 공통인 옵션이다.
	// aud와 iss는 앱마다 다르므로 요청 시점에 덧붙인다.
	baseOpts []jwt.ParserOption
	now      func() time.Time
}

// NewFirebaseVerifier는 검증기를 만든다.
func NewFirebaseVerifier(keys KeyProvider) *FirebaseVerifier {
	return &FirebaseVerifier{
		keys: keys,
		// 알고리즘·만료·발급시각 판정을 라이브러리에 위임한다.
		// 직접 파싱하면 alg=none 혼동 같은 실수를 하기 쉽다.
		baseOpts: []jwt.ParserOption{
			jwt.WithValidMethods([]string{"RS256"}),
			jwt.WithLeeway(clockSkew),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		},
		now: time.Now,
	}
}

// WithClock은 시계를 주입한다. 테스트용이다.
//
// 라이브러리의 exp/iat 판정도 같은 시각을 봐야 하므로
// jwt.WithTimeFunc을 함께 넣는다. 둘이 어긋나면 테스트가 실제 동작과 달라진다.
func (v *FirebaseVerifier) WithClock(now func() time.Time) *FirebaseVerifier {
	v.now = now
	v.baseOpts = append(v.baseOpts, jwt.WithTimeFunc(now))
	return v
}

// Verify는 토큰을 검증하고 claim을 돌려준다.
//
// 검증 항목은 전부 강제다.
//   - alg가 RS256인가
//   - kid가 키셋에 있는가
//   - 서명이 유효한가
//   - exp와 iat이 유효한가 (skew 60초)
//   - aud가 앱의 Firebase 프로젝트 ID와 같은가
//   - iss가 securetoken.google.com/{projectID}인가
//   - sub가 비어있지 않고 128자 이하인가
//   - auth_time이 미래가 아닌가
//
// aud와 iss 검증이 실질적인 앱 인증이다. 다른 앱의 토큰으로
// 이 앱을 사칭할 수 없다. X-Seori-App 헤더는 어느 프로젝트로 검증할지
// 고르는 힌트일 뿐 권한이 아니다.
func (v *FirebaseVerifier) Verify(ctx context.Context, tokenStr string, app registry.App) (Claims, error) {
	claims := &firebaseClaims{}

	// 앱별로 기대 aud와 iss가 다르므로 옵션을 요청마다 덧붙인다.
	//
	// append(v.baseOpts, ...)를 바로 쓰면 안 된다. baseOpts의 backing array에
	// 여유가 있으면 동시 요청끼리 같은 배열을 덮어써 다른 앱의 aud로 검증하게 된다.
	// 새 슬라이스에 복사한다.
	opts := make([]jwt.ParserOption, 0, len(v.baseOpts)+2)
	opts = append(opts, v.baseOpts...)
	opts = append(opts, jwt.WithAudience(app.Audience()), jwt.WithIssuer(app.Issuer()))

	token, err := jwt.ParseWithClaims(tokenStr, claims,
		func(t *jwt.Token) (any, error) {
			kid, _ := t.Header["kid"].(string)
			if kid == "" {
				return nil, errors.New("kid 헤더가 없다")
			}
			return v.keys.Key(ctx, kid)
		},
		opts...,
	)
	if err != nil {
		return Claims{}, mapJWTError(err)
	}
	if !token.Valid {
		return Claims{}, platformerr.New(platformerr.CodeAuthInvalid, "토큰이 유효하지 않아요")
	}

	// sub는 원장의 소유자 키로 이어지므로 형식을 직접 확인한다.
	// 라이브러리는 sub의 존재나 길이를 검사하지 않는다.
	if claims.Subject == "" {
		return Claims{}, platformerr.New(platformerr.CodeAuthInvalid, "토큰에 사용자 식별자가 없어요")
	}
	if len(claims.Subject) > maxSubjectLen {
		return Claims{}, platformerr.New(platformerr.CodeAuthInvalid, "사용자 식별자가 너무 길어요")
	}

	authTime := time.Unix(claims.AuthTime, 0)
	if claims.AuthTime > 0 && authTime.After(v.now().Add(clockSkew)) {
		return Claims{}, platformerr.New(platformerr.CodeAuthInvalid, "토큰 인증 시각이 올바르지 않아요")
	}

	if app.UIDBlocked(claims.Subject) {
		return Claims{}, platformerr.New(platformerr.CodeUserBlocked, "이용이 제한된 계정이에요")
	}

	var expires time.Time
	if claims.ExpiresAt != nil {
		expires = claims.ExpiresAt.Time
	}

	provider := claims.Firebase.SignInProvider
	return Claims{
		UID:            claims.Subject,
		SignInProvider: provider,
		AITUserKey:     claims.AITUserKey,
		IsAnonymous:    provider == "anonymous",
		AuthTime:       authTime,
		ExpiresAt:      expires,
	}, nil
}

// mapJWTError는 jwt 라이브러리 에러를 플랫폼 에러로 번역한다.
//
// 원인을 응답 메시지에 그대로 넣지 않는다. 어느 검증에서 걸렸는지
// 알려주면 공격자가 토큰을 다듬는 데 쓸 수 있다.
// 다만 만료는 예외로, 클라이언트가 갱신하면 되는 정상 상황이다.
func mapJWTError(err error) error {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return platformerr.Wrap(err, platformerr.CodeAuthInvalid, "토큰이 만료됐어요. 다시 로그인해 주세요")
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return platformerr.Wrap(err, platformerr.CodeAuthInvalid, "토큰이 아직 유효하지 않아요")
	case errors.Is(err, jwt.ErrTokenSignatureInvalid),
		errors.Is(err, jwt.ErrTokenUnverifiable),
		errors.Is(err, jwt.ErrSignatureInvalid):
		return platformerr.Wrap(err, platformerr.CodeAuthInvalid, "토큰 서명을 확인할 수 없어요")
	case errors.Is(err, jwt.ErrTokenInvalidAudience),
		errors.Is(err, jwt.ErrTokenInvalidIssuer):
		// 다른 앱의 토큰으로 이 앱을 사칭하려는 시도일 수 있다.
		return platformerr.Wrap(err, platformerr.CodeAuthInvalid, "이 앱의 토큰이 아니에요")
	default:
		return platformerr.Wrap(err, platformerr.CodeAuthInvalid, "토큰을 확인할 수 없어요")
	}
}
