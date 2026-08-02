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
	"regexp"
	"strings"

	"google.golang.org/api/idtoken"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

var githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)

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
	// readAllowed는 조회 전용 호출자다. writeAllowed는 조회와 조작을
	// 모두 할 수 있다. 두 목록을 나눠야 조회용 자격증명이 유출돼도
	// entitlement를 바꾸지 못한다.
	readAllowed  map[string]bool
	writeAllowed map[string]bool
}

func NewAuthenticator(
	validator TokenValidator,
	readAllowedEmails, writeAllowedEmails []string,
) (*Authenticator, error) {
	if validator == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"Admin API 검증기가 필요해요")
	}

	readAllowed := emailSet(readAllowedEmails)
	writeAllowed := emailSet(writeAllowedEmails)
	if len(readAllowed) == 0 || len(writeAllowed) == 0 {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"Admin API read/write 허용 계정이 모두 필요해요")
	}
	for email := range readAllowed {
		if writeAllowed[email] {
			return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
				"Admin API read/write 허용 계정은 서로 달라야 해요")
		}
	}

	return &Authenticator{
		validator:    validator,
		readAllowed:  readAllowed,
		writeAllowed: writeAllowed,
	}, nil
}

func emailSet(emails []string) map[string]bool {
	out := make(map[string]bool, len(emails))
	for _, e := range emails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			out[e] = true
		}
	}
	return out
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

// Access는 Admin API가 요구하는 최소 권한이다.
type Access int

const (
	AccessRead Access = iota
	AccessWrite
)

// Middleware는 OIDC 토큰을 검증하고 read/write allowlist를 강제한다.
func (a *Authenticator) Middleware(access Access, next http.Handler) http.Handler {
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

		email = strings.ToLower(strings.TrimSpace(email))
		allowed := a.writeAllowed[email]
		if access == AccessRead {
			allowed = allowed || a.readAllowed[email]
		}
		if !allowed {
			writeAuthError(w, platformerr.New(platformerr.CodeAuthForbidden,
				"허용되지 않은 호출자예요"))
			return
		}

		login := strings.TrimSpace(r.Header.Get("X-Seori-Actor"))
		if !githubLoginPattern.MatchString(login) {
			login = ""
		}
		actor := Actor{
			Email: email,
			// 백오피스가 누가 눌렀는지를 GitHub login으로 넘긴다. 증명되지
			// 않은 값이므로 형식만 제한하고 권한 판단에는 사용하지 않는다.
			Login: login,
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
