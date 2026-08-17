package config

import (
	"strings"
	"testing"
)

// Admin은 최종 사용자 세션을 발급하지 않는다. 운영 조회를 위해 세션
// 서명키를 마운트하면 필요 없는 비밀과 권한이 늘어난다.
func TestAdminDoesNotRequirePlatformSessionSecret(t *testing.T) {
	t.Setenv("PLATFORM_ROLE", string(RoleAdmin))
	t.Setenv("GOOGLE_CLOUD_PROJECT", "platform-test")
	// 환경에 잘못 남아 있어도 Admin Config에는 읽어 들이지 않는다.
	t.Setenv("PLATFORM_SESSION_SECRET", strings.Repeat("s", 64))
	t.Setenv("IAP_LEDGER_ENVIRONMENT", EnvSandbox)
	t.Setenv("IAP_CATALOG_JSON", testCatalog)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("admin config load 실패: %v", err)
	}
	if len(cfg.SessionSecret) != 0 {
		t.Error("admin role에 세션 서명키가 조립됐다")
	}
	if len(cfg.IAP.CatalogJSON) == 0 {
		t.Error("admin role이 entitlement 카탈로그를 읽지 않았다")
	}
}

func TestOperationalConfigRequiresPairAndPreservesSharedSecret(t *testing.T) {
	t.Setenv("PLATFORM_ROLE", string(RoleAPI))
	t.Setenv("GOOGLE_CLOUD_PROJECT", "platform-test")
	t.Setenv("PLATFORM_SESSION_SECRET", strings.Repeat("s", 64))
	t.Setenv("BACKOFFICE_OPERATIONAL_EVENTS_URL", "https://backoffice.example/internal/events")
	t.Setenv("BACKOFFICE_OPERATIONAL_EVENTS_SECRET", strings.Repeat("b", 44))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("config load 실패: %v", err)
	}
	if !cfg.Operational.Enabled() {
		t.Fatal("operational 전달 설정이 활성화되지 않았다")
	}
	if got := string(cfg.Operational.Secret); got != strings.Repeat("b", 44) {
		t.Fatalf("Backoffice와 공유할 원문 secret이 바뀌었다: 길이=%d", len(got))
	}

	t.Setenv("BACKOFFICE_OPERATIONAL_EVENTS_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("URL만 있는 설정을 허용했다")
	}

	t.Setenv("BACKOFFICE_OPERATIONAL_EVENTS_SECRET", strings.Repeat("b", 44))
	t.Setenv("BACKOFFICE_OPERATIONAL_EVENTS_URL", "http://backoffice.example/internal/events")
	if _, err := Load(); err == nil {
		t.Fatal("외부 평문 HTTP로 서명키를 보내도록 허용했다")
	}
}
