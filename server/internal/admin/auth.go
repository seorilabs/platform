// Package admin은 백오피스 전용 API다.
//
// 이 API는 최종 사용자가 부르지 않는다. 백오피스가 Google OIDC
// 토큰으로 호출한다. 플랫폼 세션 토큰을 쓰지 않는 이유는
// 백오피스가 사용자가 아니라 서비스이기 때문이다.
//
// R1: 백오피스는 런타임 경로에 없다. 이 API가 죽어도 결제와 검증은 동작한다.
package admin

import (
	"context"
	"net/http"
	"strings"

	"google.golang.org/api/idtoken"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// TokenValidator는 Google OIDC 토큰을 검증한다.
//
// 소비자인 이 패키지가 인터페이스를 정의한다.
type TokenValidator interface {
	Validate(ctx context.Context, token string) (email string, err error)
}

// GoogleTokenValidator는 idtoken으로 검증한다.
type GoogleTokenValidator struct {
	audience string
}

func NewGoogleTokenValidator(audience string) (*GoogleTokenValidator, error) {
	if audience == "" {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"Admin API 인증 대상이 필요해요")
	}
	return &GoogleTokenValidator{audience: audience}, nil
}

func (v *GoogleTokenValidator) Validate(ctx context.Context, token string) (string, error) {
	payload, err := idtoken.Validate(ctx, token, v.audience)
	if err != nil {
		return "", platformerr.Wrap(err, platformerr.CodeAuthInvalid,
			"인증 토큰이 올바르지 않아요")
	}

	email, _ := payload.Claims["email"].(string)
	if email == "" {
		return "", platformerr.New(platformerr.CodeAuthInvalid,
			"인증 토큰에 호출자 정보가 없어요")
	}
	if verified, ok := payload.Claims["email_verified"].(bool); ok && !verified {
		return "", platformerr.New(platformerr.CodeAuthInvalid,
			"호출자가 확인되지 않았어요")
	}
	return email, nil
}

// Authenticator는 요청을 인증한다.
type Authenticator struct {
	validator TokenValidator
	// allowed가 비어 있지 않으면 그 서비스 계정만 허용한다.
	//
	// backoffice-admin@ 하나만 넣는 것이 원칙이다. RPI 클러스터가
	// 침해되어도 그 SA에는 run.invoker 외에 아무 권한이 없다.
	allowed map[string]bool
}

func NewAuthenticator(validator TokenValidator, allowedEmails []string) (*Authenticator, error) {
	if validator == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"Admin API 검증기가 필요해요")
	}

	allowed := make(map[string]bool, len(allowedEmails))
	for _, e := range allowedEmails {
		if e = strings.TrimSpace(e); e != "" {
			allowed[e] = true
		}
	}

	return &Authenticator{validator: validator, allowed: allowed}, nil
}

// actorKey는 요청 컨텍스트에 운영자를 담는 키다.
type actorKey struct{}

// Actor는 이 요청을 부른 운영자다.
type Actor struct {
	// Email은 OIDC 토큰이 증명한 서비스 계정이다.
	Email string
	// Login은 백오피스가 헤더로 넘긴 사람 계정이다.
	//
	// 서비스 계정만으로는 누가 눌렀는지 알 수 없다.
	// 이 값은 증명되지 않으므로 감사 기록에만 쓰고 권한 판단에 쓰지 않는다.
	Login string
}

// ActorFrom은 컨텍스트에서 운영자를 꺼낸다.
func ActorFrom(ctx context.Context) Actor {
	actor, _ := ctx.Value(actorKey{}).(Actor)
	return actor
}

// Middleware는 OIDC 토큰을 검증하고 운영자를 컨텍스트에 넣는다.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			writeAuthError(w, platformerr.New(platformerr.CodeAuthRequired,
				"인증 토큰이 없어요"))
			return
		}

		email, err := a.validator.Validate(r.Context(), strings.TrimSpace(token))
		if err != nil {
			// 검증 실패는 무조건 401이다. 검증기가 무슨 에러를 주든
			// 그대로 흘리면 분류되지 않은 오류가 500으로 나가고,
			// 호출자는 "서버 문제"로 읽어 재시도한다.
			writeAuthError(w, platformerr.Wrap(err, platformerr.CodeAuthInvalid,
				"인증 토큰이 올바르지 않아요"))
			return
		}

		if len(a.allowed) > 0 && !a.allowed[email] {
			writeAuthError(w, platformerr.New(platformerr.CodeAuthForbidden,
				"허용되지 않은 호출자예요"))
			return
		}

		actor := Actor{
			Email: email,
			// 백오피스가 누가 눌렀는지를 넘긴다. 증명되지 않은 값이다.
			Login: strings.TrimSpace(r.Header.Get("X-Seori-Actor")),
		}

		ctx := context.WithValue(r.Context(), actorKey{}, actor)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeAuthError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(platformerr.StatusOf(err))

	// envelope 형식을 유지한다. 백오피스도 같은 파서를 쓴다.
	_, _ = w.Write([]byte(
		`{"ok":false,"error":{"code":"` + string(platformerr.CodeOf(err)) +
			`","message":"인증에 실패했어요"}}`,
	))
}
