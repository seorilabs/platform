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

	var lizardTycoon App
	found := false
	for i := range apps {
		if apps[i].AppID == "lizard-tycoon" {
			lizardTycoon = apps[i]
			found = true
			break
		}
	}
	if !found {
		t.Fatal("lizard-tycoon registry가 없다")
	}
	// 1.4.2의 신규 상품도 기존 원장과 같은 앱 경계에서 검증하고 복원한다.
	wantEntitlements := []string{
		"sp_galaxy_gecko", "sp_shootingstar_tokay", "sp_aurora_skink",
		"sp_moonlight_crested", "sp_gargoyle_gecko", "sp_uromastyx",
		"ft_rack_pack_1", "ft_rack_pack_2", "th_night_sky_terrarium",
		"ck_starlight_accessory_set",
		"crystal_300", "crystal_1000", "crystal_3200",
		"crystal_5500", "crystal_starter",
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
	if lizardTycoon.RequireAppCheck || !lizardTycoon.Auth.RequireAccountLinkAppCheck ||
		!lizardTycoon.FeatureEnabled("firebase_custom_token_bridge") ||
		lizardTycoon.Auth.AccountProviders["google"].Audience != "647782357004-mlnsme6h6rd1s8m1p88blj24e4nni1f9.apps.googleusercontent.com" {
		t.Fatal("연결 계정만 App Check를 요구하고 검증된 Google audience를 써야 한다")
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

// 보상형 광고 4지면은 seorilabs/lizard-tycoon 설계 rewarded-ads-leaf-balance(ADR 0019)와
// data/monetization/admob-rewarded.bm-profile.json의 계약이다. 클라이언트가 요청한 unit과 다르면
// 광고는 재생되고 SSV만 ad_unit_mismatch로 거부되며, 보상 이름·수량은 AdMob 콘솔 값과 한 글자라도
// 다르면 SSV가 전부 거부되므로 2026-10-16 콘솔에 만든 값을 여기서 고정한다.
func TestLizardTycoonAdsRegistryContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var lizardTycoon App
	found := false
	for i := range apps {
		if apps[i].AppID == "lizard-tycoon" {
			lizardTycoon = apps[i]
			found = true
			break
		}
	}
	if !found {
		t.Fatal("lizard-tycoon registry가 없다")
	}
	if !lizardTycoon.FeatureEnabled("ads") {
		t.Fatal("도마뱀 테라리움 광고 feature가 비활성이다")
	}

	type wantPlacement struct {
		id, rewardKey, androidUnit, iosUnit string
		dailyLimit                          int
	}
	wants := []wantPlacement{
		{"rewarded_care_rescue", "care_rescue", "ca-app-pub-9932778305312246/8265668010", "ca-app-pub-9932778305312246/6617455132", 2},
		{"rewarded_attendance_double", "attendance_double", "ca-app-pub-9932778305312246/2132249368", "ca-app-pub-9932778305312246/4432390901", 1},
		{"rewarded_play_extend", "play_extend", "ca-app-pub-9932778305312246/9684717580", "ca-app-pub-9932778305312246/7690952940", 1},
		{"rewarded_mission_reroll", "mission_reroll", "ca-app-pub-9932778305312246/2982296606", "ca-app-pub-9932778305312246/3751707939", 1},
	}
	for _, want := range wants {
		placement, ok := lizardTycoon.AdsPlacement(want.id)
		if !ok {
			t.Fatalf("%s 지면이 없다", want.id)
		}
		if placement.Format != "rewarded" {
			t.Fatalf("%s format=%q, want rewarded", want.id, placement.Format)
		}
		if placement.Reward == nil || placement.Reward.Key != want.rewardKey ||
			placement.Reward.MinAmount != 1 || placement.Reward.MaxAmount != 1 {
			t.Fatalf("%s 보상 계약이 클라이언트와 다르다: %+v", want.id, placement.Reward)
		}
		if placement.DailyLimit != want.dailyLimit || placement.CooldownSeconds != 60 {
			t.Fatalf("%s daily_limit=%d cooldown=%d, want %d/60", want.id, placement.DailyLimit, placement.CooldownSeconds, want.dailyLimit)
		}
		provider, ok := placement.Providers["admob"]
		if !ok {
			t.Fatalf("%s AdMob provider 설정이 없다", want.id)
		}
		if provider.AndroidAdUnitID != want.androidUnit || provider.IOSAdUnitID != want.iosUnit {
			t.Fatalf("%s unit=%q/%q, want %q/%q", want.id, provider.AndroidAdUnitID, provider.IOSAdUnitID, want.androidUnit, want.iosUnit)
		}
		if provider.RewardItem != want.rewardKey || provider.RewardAmount != 1 {
			t.Fatalf("%s AdMob 콘솔 보상 계약=%q/%d, want %q/1", want.id, provider.RewardItem, provider.RewardAmount, want.rewardKey)
		}
	}
}
