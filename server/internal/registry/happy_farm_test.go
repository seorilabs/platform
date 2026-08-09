package registry

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestHappyFarmRegistryContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var happyFarm *App
	for i := range apps {
		if apps[i].AppID == "happy-farm" {
			happyFarm = &apps[i]
			break
		}
	}
	if happyFarm == nil {
		t.Fatal("happy-farm registry가 없다")
	}

	if happyFarm.FirebaseProjectID != "happy-farm-tycoon" || happyFarm.RequireAppCheck {
		t.Fatalf("Firebase/App Check 계약이 다르다: %#v", happyFarm)
	}
	if !happyFarm.FeatureEnabled("events") || happyFarm.FeatureEnabled("config") ||
		!happyFarm.FeatureEnabled("iap") || !happyFarm.FeatureEnabled("ads") ||
		happyFarm.FeatureEnabled("firebase_custom_token_bridge") {
		t.Fatalf("Happy Farm 기능 계약이 다르다: %#v", happyFarm.Features)
	}
	if happyFarm.IAP.LedgerEnvironment != LedgerProduction ||
		happyFarm.IAP.GooglePlayPackageName != "com.seorilabs.happyfarm" ||
		happyFarm.IAP.AppStoreBundleID != "com.seorilabs.happyfarm" ||
		!happyFarm.EntitlementAllowed("ad_free") {
		t.Fatalf("Happy Farm IAP 계약이 다르다: %#v", happyFarm.IAP)
	}
	if len(happyFarm.Ads.Placements) == 0 {
		t.Fatal("Happy Farm 광고 지면이 없다")
	}

	wantAllowlist := []string{
		"seori_session_start",
		"seori_sdk_error",
		"game_start",
		"first_seed_selected",
		"first_meaningful_harvest",
		"onboarding_step_view",
		"onboarding_complete",
		"onboarding_skip",
		"onboarding_stall",
		"ad_reward_click",
		"ad_reward_completed",
		"ad_reward_failed",
		"ad_limit_blocked",
		"interstitial_shown",
		"notification_opened",
		"update_gate_shown",
		"update_gate_store_click",
	}
	if !reflect.DeepEqual(happyFarm.PlatformEventAllowlist, wantAllowlist) {
		t.Fatalf("allowlist가 다르다\n got: %#v\nwant: %#v", happyFarm.PlatformEventAllowlist, wantAllowlist)
	}
	if happyFarm.EventAllowed("ad_reward_impression") || happyFarm.EventAllowed("crop_harvested") {
		t.Fatal("고빈도 이벤트가 허용됐다")
	}
}
