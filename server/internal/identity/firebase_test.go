package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// 골든 JWT 테스트다. P1의 필수 게이트이며 6종을 전부 거부해야 한다.
//
// 인증은 플랫폼 전체의 신뢰 기반이라 여기가 뚫리면 나머지 방어가 의미 없다.
// 특히 alg 변조는 JWT 구현에서 반복적으로 나온 취약점이라 반드시 막아야 한다.

const (
	testProjectID = "lizard-tycoon"
	testKID       = "test-kid-1"
	testUID       = "firebase-uid-abc123"
)

func testApp() registry.App {
	return registry.App{
		AppID:             "lizard-tycoon",
		DisplayName:       "도마뱀 테라리움",
		FirebaseProjectID: testProjectID,
		Status:            registry.StatusActive,
	}
}

// fakeKeys는 고정 공개키를 돌려준다.
// 실제 키셋을 받아오지 않으므로 테스트가 네트워크에 의존하지 않는다.
type fakeKeys struct {
	byKID map[string]*rsa.PublicKey
}

func (f fakeKeys) Key(_ context.Context, kid string) (*rsa.PublicKey, error) {
	k, ok := f.byKID[kid]
	if !ok {
		return nil, errors.New("알 수 없는 kid")
	}
	return k, nil
}

type tokenFixture struct {
	priv     *rsa.PrivateKey
	verifier *FirebaseVerifier
	now      time.Time
}

func newFixture(t *testing.T) *tokenFixture {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("키 생성 실패: %v", err)
	}

	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	keys := fakeKeys{byKID: map[string]*rsa.PublicKey{testKID: &priv.PublicKey}}

	// WithClock이 jwt.WithTimeFunc까지 넣으므로 라이브러리 판정도 같은 시각을 본다.
	v := NewFirebaseVerifier(keys).WithClock(func() time.Time { return now })

	return &tokenFixture{priv: priv, verifier: v, now: now}
}

// claimsOpt는 토큰 내용을 케이스별로 비튼다.
type claimsOpt func(*firebaseClaims)

func (f *tokenFixture) sign(t *testing.T, method jwt.SigningMethod, kid string, key any, opts ...claimsOpt) string {
	t.Helper()

	c := &firebaseClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   testUID,
			Issuer:    "https://securetoken.google.com/" + testProjectID,
			Audience:  jwt.ClaimStrings{testProjectID},
			IssuedAt:  jwt.NewNumericDate(f.now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(f.now.Add(time.Hour)),
		},
		AuthTime: f.now.Add(-time.Minute).Unix(),
	}
	c.Firebase.SignInProvider = "anonymous"

	for _, o := range opts {
		o(c)
	}

	tok := jwt.NewWithClaims(method, c)
	if kid != "" {
		tok.Header["kid"] = kid
	}

	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("서명 실패: %v", err)
	}
	return s
}

// TestVerifyGoldenRejections는 6종을 전부 거부하는지 본다. 필수 게이트다.
func TestVerifyGoldenRejections(t *testing.T) {
	f := newFixture(t)
	app := testApp()

	// alg 변조용 별도 키
	otherPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("키 생성 실패: %v", err)
	}

	tests := []struct {
		name  string
		token func() string
		why   string
	}{
		{
			name: "1. 만료된 토큰",
			token: func() string {
				return f.sign(t, jwt.SigningMethodRS256, testKID, f.priv, func(c *firebaseClaims) {
					c.ExpiresAt = jwt.NewNumericDate(f.now.Add(-2 * time.Hour))
					c.IssuedAt = jwt.NewNumericDate(f.now.Add(-3 * time.Hour))
				})
			},
			why: "만료 토큰이 통과하면 로그아웃이 무의미해진다",
		},
		{
			name: "2. 잘못된 aud",
			token: func() string {
				return f.sign(t, jwt.SigningMethodRS256, testKID, f.priv, func(c *firebaseClaims) {
					c.Audience = jwt.ClaimStrings{"other-project"}
				})
			},
			why: "다른 앱의 토큰으로 이 앱을 사칭할 수 있게 된다",
		},
		{
			name: "3. 잘못된 iss",
			token: func() string {
				return f.sign(t, jwt.SigningMethodRS256, testKID, f.priv, func(c *firebaseClaims) {
					c.Issuer = "https://evil.example.com/" + testProjectID
				})
			},
			why: "발급자를 확인하지 않으면 임의 토큰이 통과한다",
		},
		{
			name: "4-a. alg를 none으로 변조",
			token: func() string {
				// alg=none은 JWT 구현에서 반복된 취약점이다.
				tok := jwt.NewWithClaims(jwt.SigningMethodNone, &firebaseClaims{
					RegisteredClaims: jwt.RegisteredClaims{
						Subject:   testUID,
						Issuer:    "https://securetoken.google.com/" + testProjectID,
						Audience:  jwt.ClaimStrings{testProjectID},
						ExpiresAt: jwt.NewNumericDate(f.now.Add(time.Hour)),
					},
				})
				tok.Header["kid"] = testKID
				s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
				if err != nil {
					t.Fatalf("서명 실패: %v", err)
				}
				return s
			},
			why: "alg=none이 통과하면 누구나 토큰을 위조할 수 있다",
		},
		{
			name: "4-b. alg를 HS256으로 변조",
			token: func() string {
				// 공개키를 HMAC 비밀키로 쓰는 혼동 공격이다.
				tok := jwt.NewWithClaims(jwt.SigningMethodHS256, &firebaseClaims{
					RegisteredClaims: jwt.RegisteredClaims{
						Subject:   testUID,
						Issuer:    "https://securetoken.google.com/" + testProjectID,
						Audience:  jwt.ClaimStrings{testProjectID},
						ExpiresAt: jwt.NewNumericDate(f.now.Add(time.Hour)),
					},
				})
				tok.Header["kid"] = testKID
				s, err := tok.SignedString([]byte("공개키를_비밀키로_쓰는_혼동공격"))
				if err != nil {
					t.Fatalf("서명 실패: %v", err)
				}
				return s
			},
			why: "알고리즘 혼동 공격을 막지 못하면 공개키만으로 위조된다",
		},
		{
			name: "5. kid가 키셋에 없음",
			token: func() string {
				return f.sign(t, jwt.SigningMethodRS256, "존재하지-않는-kid", f.priv)
			},
			why: "모르는 키로 서명된 토큰을 받아들이면 안 된다",
		},
		{
			name: "6-a. 다른 키로 서명 (서명 불일치)",
			token: func() string {
				return f.sign(t, jwt.SigningMethodRS256, testKID, otherPriv)
			},
			why: "서명 검증이 실질적인 위조 방어선이다",
		},
		{
			name: "6-b. 서명 바이트 변조",
			token: func() string {
				s := f.sign(t, jwt.SigningMethodRS256, testKID, f.priv)
				// 마지막 글자를 바꾼다.
				last := s[len(s)-1]
				repl := byte('A')
				if last == 'A' {
					repl = 'B'
				}
				return s[:len(s)-1] + string(repl)
			},
			why: "서명 일부만 바뀌어도 거부해야 한다",
		},
		{
			name: "kid 헤더 자체가 없음",
			token: func() string {
				return f.sign(t, jwt.SigningMethodRS256, "", f.priv)
			},
			why: "kid 없이는 어느 키로 검증할지 정할 수 없다",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.verifier.Verify(context.Background(), tt.token(), app)

			if err == nil {
				t.Fatalf("거부해야 하는데 통과했다. %s", tt.why)
			}
			if code := platformerr.CodeOf(err); code != platformerr.CodeAuthInvalid {
				t.Errorf("code = %q, want auth_invalid", code)
			}
		})
	}
}

func TestVerifyAccepts(t *testing.T) {
	f := newFixture(t)
	app := testApp()

	token := f.sign(t, jwt.SigningMethodRS256, testKID, f.priv)

	claims, err := f.verifier.Verify(context.Background(), token, app)
	if err != nil {
		t.Fatalf("정상 토큰을 거부했다: %v", err)
	}

	if claims.UID != testUID {
		t.Errorf("UID = %q, want %q", claims.UID, testUID)
	}
	if !claims.IsAnonymous {
		t.Error("IsAnonymous = false, want true")
	}
	if claims.SignInProvider != "anonymous" {
		t.Errorf("SignInProvider = %q", claims.SignInProvider)
	}
}

func TestVerifySubjectConstraints(t *testing.T) {
	f := newFixture(t)
	app := testApp()

	t.Run("sub가 비어 있으면 거부", func(t *testing.T) {
		token := f.sign(t, jwt.SigningMethodRS256, testKID, f.priv, func(c *firebaseClaims) {
			c.Subject = ""
		})
		if _, err := f.verifier.Verify(context.Background(), token, app); err == nil {
			t.Fatal("빈 sub를 통과시켰다. 원장의 소유자 키가 비게 된다")
		}
	})

	t.Run("sub가 128자를 넘으면 거부", func(t *testing.T) {
		token := f.sign(t, jwt.SigningMethodRS256, testKID, f.priv, func(c *firebaseClaims) {
			c.Subject = strings.Repeat("a", maxSubjectLen+1)
		})
		if _, err := f.verifier.Verify(context.Background(), token, app); err == nil {
			t.Fatal("과도하게 긴 sub를 통과시켰다")
		}
	})
}

// auth_time이 미래면 시계 조작이거나 위조다.
func TestVerifyRejectsFutureAuthTime(t *testing.T) {
	f := newFixture(t)
	token := f.sign(t, jwt.SigningMethodRS256, testKID, f.priv, func(c *firebaseClaims) {
		c.AuthTime = f.now.Add(time.Hour).Unix()
	})

	if _, err := f.verifier.Verify(context.Background(), token, testApp()); err == nil {
		t.Fatal("미래 auth_time을 통과시켰다")
	}
}

// 차단된 계정은 토큰이 유효해도 막는다.
func TestVerifyRejectsBlockedUID(t *testing.T) {
	f := newFixture(t)
	app := testApp()
	app.BlockedUIDs = []string{testUID}

	token := f.sign(t, jwt.SigningMethodRS256, testKID, f.priv)

	_, err := f.verifier.Verify(context.Background(), token, app)
	if err == nil {
		t.Fatal("차단된 계정을 통과시켰다")
	}
	if code := platformerr.CodeOf(err); code != platformerr.CodeUserBlocked {
		t.Errorf("code = %q, want user_blocked", code)
	}
}

// clock skew 안쪽의 만료는 허용한다. 서버 간 시계 오차 때문이다.
func TestVerifyAllowsClockSkew(t *testing.T) {
	f := newFixture(t)
	token := f.sign(t, jwt.SigningMethodRS256, testKID, f.priv, func(c *firebaseClaims) {
		// 30초 전에 만료. skew 60초 안쪽이다.
		c.ExpiresAt = jwt.NewNumericDate(f.now.Add(-30 * time.Second))
	})

	if _, err := f.verifier.Verify(context.Background(), token, testApp()); err != nil {
		t.Fatalf("skew 안쪽 만료를 거부했다: %v", err)
	}
}
