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

	// 조회와 조작 자격증명을 분리한다. 조회 자격증명이 유출돼도 원장을
	// 바꿀 수 없어야 한다. 예전 단일 목록에는 fallback하지 않는다.
	readAllowed := splitList(os.Getenv("ADMIN_READ_ALLOWED_ACCOUNTS"))
	writeAllowed := splitList(os.Getenv("ADMIN_WRITE_ALLOWED_ACCOUNTS"))
	if len(readAllowed) == 0 || len(writeAllowed) == 0 {
		return errors.New("admin role에 ADMIN_READ_ALLOWED_ACCOUNTS와 ADMIN_WRITE_ALLOWED_ACCOUNTS가 모두 필요하다")
	}
	if os.Getenv("ADMIN_ALLOWED_ACCOUNTS") != "" {
		slog.Warn("ADMIN_ALLOWED_ACCOUNTS는 더 이상 사용하지 않는다")
	}

	auth, err := admin.NewAuthenticator(validator, readAllowed, writeAllowed)
	if err != nil {
		return err
	}

	// RemoteConfig도 함께 넘긴다. break-glass의 점검 모드가 여기로 온다.
	handler, err := admin.NewHandler(
		d.iap.ledger,
		d.config,
		d.adminUsers,
		d.registry,
		d.iap.catalog,
		auth,
		auditAdapter{col: d.events},
	)
	if err != nil {
		return err
	}
	handler.Register(mux)

	slog.Info("Admin API 준비 완료",
		"read_allowed_accounts", len(readAllowed),
		"write_allowed_accounts", len(writeAllowed),
	)
	return nil
}
