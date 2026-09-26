package registry

import (
	"context"
	"os"
	"testing"
)

func TestReascendLaunchMonetizationContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var app *App
	for i := range apps {
		if apps[i].AppID == "reascend" {
			app = &apps[i]
			break
		}
	}
	if app == nil {
		t.Fatal("reascend registry가 없다")
	}
	if err := app.Validate(); err != nil {
		t.Fatal(err)
	}
	if !app.MarketEnabled("app_store") || !app.MarketEnabled("google_play") ||
		app.IAP.AppStoreBundleID != "com.seorilabs.reascend" ||
		!app.IAP.AppleSandboxEnabled || !app.IAP.AppStoreClientCompletion || !app.IAP.AppStoreRequireAccountToken ||
		app.IAP.LegacyUnscopedLedger || app.IAP.LedgerEnvironment != LedgerProduction {
		t.Fatalf("Reascend Apple 소모품의 구매자·환경·앱 완료 경계가 다르다: %#v", app.IAP)
	}
	// 클라이언트 판매 스위치는 꺼도, 서버 IAP 원장은 과거 구매 복원을 위해 유지한다.
	if !app.FeatureEnabled("iap") || !app.FeatureEnabled("ads") {
		t.Fatalf("복원과 보상형 광고 기능 경계가 다르다: %#v", app.Features)
	}
	if len(app.Ads.Placements) != 3 {
		t.Fatalf("보상형 광고 지면은 3개여야 한다: %d", len(app.Ads.Placements))
	}
	want := map[string]bool{"offline_reward_boost": true, "material_box_free": true, "daily_supply_extra": true}
	for _, placement := range app.Ads.Placements {
		if !want[placement.ID] || placement.Format != "rewarded" || placement.Reward == nil {
			t.Fatalf("예상하지 않은 광고 지면: %#v", placement)
		}
		unit := placement.Providers["admob"]
		if unit.AndroidAdUnitID == "" || unit.IOSAdUnitID == "" || unit.RewardItem != "reward" || unit.RewardAmount != 1 {
			t.Fatalf("AdMob 단위와 SSV 보상 표식이 다르다: %s", placement.ID)
		}
		delete(want, placement.ID)
	}
	if len(want) != 0 {
		t.Fatalf("누락된 광고 지면: %#v", want)
	}
}
