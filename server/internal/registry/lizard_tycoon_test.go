package registry

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestLizardTycoonRegistryEventContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var lizardTycoon *App
	for i := range apps {
		if apps[i].AppID == "lizard-tycoon" {
			lizardTycoon = &apps[i]
			break
		}
	}
	if lizardTycoon == nil {
		t.Fatal("lizard-tycoon registry가 없다")
	}
	// 1.4.2의 신규 상품도 기존 원장과 같은 앱 경계에서 검증하고 복원한다.
	wantEntitlements := []string{
		"sp_galaxy_gecko", "sp_shootingstar_tokay", "sp_aurora_skink",
		"sp_moonlight_crested", "sp_gargoyle_gecko", "sp_uromastyx",
		"ft_rack_pack_1", "ft_rack_pack_2", "th_night_sky_terrarium",
		"ck_starlight_accessory_set",
	}
	if !reflect.DeepEqual(lizardTycoon.IAP.EntitlementIDs, wantEntitlements) {
		t.Fatalf("도마뱀 IAP 허용 목록 불일치: %v", lizardTycoon.IAP.EntitlementIDs)
	}
	if lizardTycoon.IAP.LedgerEnvironment != LedgerProduction || !lizardTycoon.IAP.LegacyUnscopedLedger {
		t.Fatal("기존 운영 결제 원장 경계를 유지해야 한다")
	}
	if lizardTycoon.EntitlementAllowed("ad_free") || lizardTycoon.EntitlementAllowed("unknown_product") {
		t.Fatal("다른 앱 또는 미등록 상품이 허용됐다")
	}
	if !lizardTycoon.FeatureEnabled("presence") {
		t.Fatal("v1.1.12 canary 후보의 presence가 활성화되지 않았다")
	}

	wantAllowlist := []string{
		"lizard_adopted",
		"tutorial_begin",
		"tutorial_step",
		"tutorial_complete",
		"daily_play_completed",
		"purchase",
		"premium_purchase_failed",
		"script_error",
	}
	if !reflect.DeepEqual(lizardTycoon.PlatformEventAllowlist, wantAllowlist) {
		t.Fatalf("allowlist가 다르다\n got: %#v\nwant: %#v", lizardTycoon.PlatformEventAllowlist, wantAllowlist)
	}
	if lizardTycoon.EventAllowed("screen_view") || lizardTycoon.EventAllowed("first_open") ||
		lizardTycoon.EventAllowed("session_start") {
		t.Fatal("GA4 SDK가 자동 수집하거나 화면 전용인 이벤트가 Platform allowlist에 포함됐다")
	}
}
