package registry

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// Amber Lizards(merge-lizard)는 Godot 클라이언트가 세션, 원격 설정, 우편함, 이벤트,
// 구매 검증을 모두 Platform으로 보낸다. 클라이언트 초기화가 우편함 조회까지 성공해야
// 온라인으로 시작하므로 inbox도 함께 켠다. 광고는 AdMob unit이 정해진 뒤 별도 변경으로 켠다.
func TestMergeLizardRegistryContract(t *testing.T) {
	apps, err := NewFSSource(os.DirFS("../../../registry"), "apps").LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var app App
	found := false
	for i := range apps {
		if apps[i].AppID == "merge-lizard" {
			app, found = apps[i], true
			break
		}
	}
	if !found {
		t.Fatal("merge-lizard registry가 없다")
	}
	if err := app.Validate(); err != nil {
		t.Fatal(err)
	}
	if app.DisplayName != "Amber Lizards" || app.FirebaseProjectID != "merge-lizard" ||
		app.FirebaseCustomTokenServiceAccount != "platform-auth@merge-lizard.iam.gserviceaccount.com" {
		t.Fatalf("앱 또는 Firebase 계약이 다르다: %#v", app)
	}
	for _, feature := range []string{"config", "events", "iap", "inbox", "firebase_custom_token_bridge"} {
		if !app.FeatureEnabled(feature) {
			t.Fatalf("%s 기능이 꺼져 있다: %#v", feature, app.Features)
		}
	}
	// AdMob unit 없이 ads를 켜면 Validate가 막는다. 켜는 것은 unit과 SSV 콜백이 준비된 뒤다.
	if app.FeatureEnabled("ads") || len(app.Ads.Placements) != 0 {
		t.Fatalf("광고가 unit 없이 활성화됐다: %#v", app.Ads)
	}
	// Godot에는 App Check SDK가 없다.
	if app.RequireAppCheck || app.FeatureEnabled("presence") {
		t.Fatalf("클라이언트가 지원하지 않는 강제 설정이 켜졌다: %#v", app)
	}

	iap := app.IAP
	if iap.LedgerEnvironment != LedgerProduction || !iap.AppleSandboxEnabled || iap.LegacyUnscopedLedger {
		t.Fatalf("원장 환경 경계가 다르다: %#v", iap)
	}
	if !reflect.DeepEqual(iap.Markets, []string{"google_play", "app_store"}) ||
		iap.GooglePlayPackageName != "com.seorilabs.mergelizard" ||
		iap.AppStoreBundleID != "com.seorilabs.mergelizard" {
		t.Fatalf("마켓 식별자가 다르다: %#v", iap)
	}
	// App Store 거래 완료는 서버 Finish Transaction이 기본이다. 클라이언트도 지급 커밋 뒤
	// finish를 부르지만(서버가 먼저 끝냈으면 not_found) 서버 경로를 끄지 않는다.
	if iap.AppStoreClientCompletion || iap.AppStoreRequireAccountToken || iap.RequireLinkedAccount {
		t.Fatalf("App Store 완료·구매자 경계가 다르다: %#v", iap)
	}

	// registry allowlist와 카탈로그 후보가 어긋나면 platform-iap 부팅(validateAppCatalog)이 실패한다.
	raw, err := os.ReadFile("../../../registry/iap-catalog/merge-lizard.json")
	if err != nil {
		t.Fatal(err)
	}
	var candidate struct {
		Apps map[string]struct {
			Entitlements map[string]json.RawMessage `json:"entitlements"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(raw, &candidate); err != nil {
		t.Fatal(err)
	}
	catalogIDs := make([]string, 0, len(candidate.Apps["merge-lizard"].Entitlements))
	for id := range candidate.Apps["merge-lizard"].Entitlements {
		catalogIDs = append(catalogIDs, id)
	}
	registryIDs := append([]string(nil), iap.EntitlementIDs...)
	sort.Strings(catalogIDs)
	sort.Strings(registryIDs)
	if len(registryIDs) != 15 || !reflect.DeepEqual(registryIDs, catalogIDs) {
		t.Fatalf("entitlement allowlist와 카탈로그 후보가 다르다\nregistry: %v\ncatalog:  %v", registryIDs, catalogIDs)
	}

	// 클라이언트 EventContract(godot/packages/product-core/src/analytics/event_contract.gd)
	// 25종과 SDK 예약 이벤트 2종이다. 클라이언트에는 다른 분석 경로가 없어서 빠진 이름은
	// 조용히 버려진다.
	wantEvents := []string{
		"seori_session_start", "seori_sdk_error",
		"app_open", "title_ready", "tutorial_begin", "tutorial_step", "tutorial_complete", "tutorial_skip",
		"merge", "purify", "level_up", "earn_virtual_currency", "spend_virtual_currency", "offline_claim",
		"unlock_achievement", "build", "codex_open", "shop_open", "shop_unlocked", "purchase",
		"purchase_pending", "refund", "settings_open", "consent_change", "save_error", "recovery", "rc_exposure",
	}
	if !reflect.DeepEqual(app.PlatformEventAllowlist, wantEvents) {
		t.Fatalf("이벤트 allowlist가 다르다: %v", app.PlatformEventAllowlist)
	}
	// Measurement Protocol api_secret이 ingest secret에 없으면 전송마다 경고만 남는다.
	if app.GA4.MeasurementID != "" || app.GA4.EventPrefix != "" {
		t.Fatalf("GA4 중계 설정이 준비 전에 켜졌다: %#v", app.GA4)
	}
	if len(app.CORSOrigins) != 0 {
		t.Fatalf("네이티브 앱에 CORS origin이 있다: %v", app.CORSOrigins)
	}
}
