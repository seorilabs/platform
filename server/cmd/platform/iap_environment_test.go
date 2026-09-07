package main

import (
	"context"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/config"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type environmentRegistrySource struct{ app registry.App }

func (s environmentRegistrySource) LoadApps(context.Context) ([]registry.App, error) {
	return []registry.App{s.app}, nil
}

func TestAppleSandboxCompositionKeepsProductionDefault(t *testing.T) {
	app := registry.App{AppID: "test-app", DisplayName: "Test app", FirebaseProjectID: "test-app", Status: registry.StatusActive,
		Features: map[string]bool{"iap": true}, IAP: registry.IAPConfig{
			LedgerEnvironment: registry.LedgerProduction, AppleSandboxEnabled: true,
			Markets: []string{"app_store", "google_play"}, AppStoreBundleID: "com.seorilabs.testapp",
			GooglePlayPackageName: "com.seorilabs.testapp", EntitlementIDs: []string{"premium"},
		}}
	cat, err := catalog.Parse([]byte(`{"version":2,"apps":{"test-app":{"entitlements":{"premium":{"app_store":"com.seorilabs.testapp.premium","google_play":"premium"}}}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(environmentRegistrySource{app: app})
	// 조립만 검사한다. API 호출과 키 파싱을 하지 않는 불투명 테스트 값이다.
	cfg := config.Config{IAP: config.IAPConfig{Environment: "production", CompletionMaxAttempts: 10, CompletionMaxAge: 24 * time.Hour, Apple: config.AppleConfig{
		KeyContent: []byte("test-only-placeholder"), KeyID: "test-key", Issuer: "test-issuer", BundleID: app.IAP.AppStoreBundleID,
	}}}
	part, err := newAppleSandboxEnvironment(context.Background(), cfg, nil, app, cat, nil, reg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if part.ledger.Environment() != domain.EnvSandbox || part.app.IAP.LedgerEnvironment != registry.LedgerProduction || cfg.IAP.Apple.Sandbox {
		t.Fatal("additional environment changed production defaults")
	}
	if len(part.verifiers) != 1 || !part.service.Supports(domain.PlatformAppStore) || part.service.Supports(domain.PlatformGooglePlay) || part.service.Supports(domain.PlatformAppsInToss) {
		t.Fatal("additional Apple environment included another market")
	}
	// Play가 앱의 기본 마켓에 있어도 추가 Apple 환경에서는 제공자를 호출하거나
	// 원장에 도달하지 않는다. nil store도 이 거부 경로에서는 접근하면 안 된다.
	_, err = part.service.VerifyPurchase(context.Background(), app.AppID, "test-puid", domain.Proof{Platform: domain.PlatformGooglePlay, ProductID: "premium", Token: "test-proof"})
	if platformerr.CodeOf(err) != platformerr.CodePlatformUnavailable {
		t.Fatalf("unexpected cross-market result: %v", err)
	}
	if _, err := newWorkerFor(app.AppID, part.ledger, part.verifiers, nil, cfg, nil); err != nil {
		t.Fatalf("sandbox completion worker: %v", err)
	}
	app.IAP.AppleSandboxEnabled = false
	if _, err := newAppleSandboxEnvironment(context.Background(), cfg, nil, app, cat, nil, reg, nil, nil); err == nil {
		t.Fatal("unapproved app acquired a sandbox runtime")
	}
}
