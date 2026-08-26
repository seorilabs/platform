package config

import (
	"encoding/base64"
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

// ingest는 인증이 없는 이벤트도 받지만, 세션 토큰이 있으면 검증해
// platform_user_id를 붙인다. 키가 없으면 인증 이벤트가 조용히 익명으로
// 적재되므로 부팅 단계에서 실패해야 한다.
func TestIngestRequiresPlatformSessionSecret(t *testing.T) {
	t.Setenv("PLATFORM_ROLE", string(RoleIngest))
	t.Setenv("GOOGLE_CLOUD_PROJECT", "platform-test")
	t.Setenv("PLATFORM_SESSION_SECRET", "")

	if _, err := Load(); err == nil {
		t.Fatal("ingest가 세션 서명키 없이 부팅됐다")
	}

	want := strings.Repeat("s", 32)
	t.Setenv("PLATFORM_SESSION_SECRET", base64.StdEncoding.EncodeToString([]byte(want)))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("ingest config load 실패: %v", err)
	}
	if got := string(cfg.SessionSecret); got != want {
		t.Fatalf("ingest 세션 서명키가 달라졌다: 길이=%d", len(got))
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

func TestPresenceConfigIsOptionalButRejectsPartialPair(t *testing.T) {
	t.Setenv("PLATFORM_ROLE", string(RoleIngest))
	t.Setenv("GOOGLE_CLOUD_PROJECT", "platform-test")
	t.Setenv("PLATFORM_SESSION_SECRET", strings.Repeat("s", 64))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("presence 비활성 config load 실패: %v", err)
	}
	if cfg.Presence.Enabled() {
		t.Fatal("키가 없는데 presence가 활성화됐다")
	}

	t.Setenv("PLATFORM_PRESENCE_EDGE_URL", "https://edge.vzyx.xyz")
	if _, err := Load(); err == nil {
		t.Fatal("Edge URL만 있는 presence 설정을 허용했다")
	}

	t.Setenv("PLATFORM_PRESENCE_PRIVATE_KEY", "private-key-placeholder")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("presence 설정 load 실패: %v", err)
	}
	if !cfg.Presence.Enabled() || cfg.Presence.EdgeURL != "https://edge.vzyx.xyz" {
		t.Fatalf("presence 설정이 달라졌다: %+v", cfg.Presence)
	}

	t.Setenv("PLATFORM_PRESENCE_EDGE_URL", "http://edge.vzyx.xyz")
	if _, err := Load(); err == nil {
		t.Fatal("외부 평문 HTTP Edge URL을 허용했다")
	}
}
