package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/seorilabs/platform/server/internal/admin"
)

// registerAdmin은 백오피스 전용 API를 연다.
//
// 인증은 Google OIDC다. 백오피스가 backoffice-admin@ 서비스 계정으로
// 부른다. 그 SA에는 run.invoker 외에 아무 권한도 없어서, RPI 클러스터가
// 침해되어도 여기까지가 폭발 반경이다.
func registerAdmin(mux *http.ServeMux, d *deps) error {
	audience := os.Getenv("ADMIN_OIDC_AUDIENCE")
	if audience == "" {
		// 검증할 수 없으면 열지 않는다. 인증 없는 Admin API는
		// 원장 전체를 공개하는 것과 같다.
		return errors.New("admin role에 ADMIN_OIDC_AUDIENCE가 필요하다")
	}

	validator, err := admin.NewGoogleTokenValidator(audience)
	if err != nil {
		return err
	}

	// 허용 목록을 비워두면 audience가 맞는 어떤 Google 계정이든
	// 부를 수 있다. 반드시 좁힌다.
	allowed := splitList(os.Getenv("ADMIN_ALLOWED_ACCOUNTS"))
	if len(allowed) == 0 {
		return errors.New("admin role에 ADMIN_ALLOWED_ACCOUNTS가 필요하다")
	}

	auth, err := admin.NewAuthenticator(validator, allowed)
	if err != nil {
		return err
	}

	handler, err := admin.NewHandler(d.iap.ledger, auth, auditAdapter{col: d.events})
	if err != nil {
		return err
	}
	handler.Register(mux)

	slog.Info("Admin API 준비 완료", "allowed_accounts", len(allowed))
	return nil
}
