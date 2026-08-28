package registry

import (
	"context"
	"os"
	"testing"
)

// 운글은 결과 화면에서 사용자가 직접 선택한 보상형 광고 한 번으로 같은 명식의
// 세운과 열두 달 월운을 함께 연다. 클라이언트, AdMob 콘솔, Platform registry가
// 다른 placement나 보상값을 쓰면 광고를 끝까지 보고도 SSV가 거부되므로 여기서
// 운영 계약을 고정한다.
func TestUngeulAdsRegistryContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var ungeul *App
	for i := range apps {
		if apps[i].AppID == "ungeul" {
			ungeul = &apps[i]
			break
		}
	}
	if ungeul == nil {
		t.Fatal("ungeul registry가 없다")
	}
	if !ungeul.FeatureEnabled("ads") {
		t.Fatal("운글 광고 feature가 비활성이다")
	}
	if len(ungeul.Ads.Placements) != 1 {
		t.Fatalf("placements=%d, want 1", len(ungeul.Ads.Placements))
	}

	placement, ok := ungeul.AdsPlacement("deep_flow")
	if !ok {
		t.Fatal("deep_flow 지면이 없다")
	}
	if placement.Format != "rewarded" {
		t.Fatalf("format=%q, want rewarded", placement.Format)
	}
	if placement.Reward == nil || placement.Reward.Key != "deep_flow" ||
		placement.Reward.MinAmount != 1 || placement.Reward.MaxAmount != 1 {
		t.Fatalf("보상 계약이 클라이언트와 다르다: %+v", placement.Reward)
	}
	if placement.DailyLimit != 10 || placement.CooldownSeconds != 30 {
		t.Fatalf("policy=(%d,%d), want (10,30)", placement.DailyLimit, placement.CooldownSeconds)
	}

	provider, ok := placement.Providers["admob"]
	if !ok {
		t.Fatal("AdMob provider 설정이 없다")
	}
	if provider.AndroidAdUnitID != "ca-app-pub-2444587584524186/8793041426" {
		t.Fatalf("Android unit=%q", provider.AndroidAdUnitID)
	}
	if provider.IOSAdUnitID != "ca-app-pub-2444587584524186/2557921082" {
		t.Fatalf("iOS unit=%q", provider.IOSAdUnitID)
	}
	if provider.RewardItem != "deep_flow" || provider.RewardAmount != 1 {
		t.Fatalf("AdMob reward=(%q,%d), want (deep_flow,1)", provider.RewardItem, provider.RewardAmount)
	}
	if ungeul.Content.RewardKey != "deep_flow" {
		t.Fatalf("content reward key=%q, want deep_flow", ungeul.Content.RewardKey)
	}
}

// 운글 열람권은 세 마켓의 소모성 상품 한 건을 동일 entitlement로 검증하고,
// 네이티브 구매는 카카오 또는 Apple로 연결된 계정에만 귀속한다. 상품 카탈로그,
// 클라이언트, registry가 어긋나면 결제 뒤 지급 또는 복원이 막히므로 운영 계약을 고정한다.
func TestUngeulIAPRegistryContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var ungeul *App
	for i := range apps {
		if apps[i].AppID == "ungeul" {
			ungeul = &apps[i]
			break
		}
	}
	if ungeul == nil {
		t.Fatal("ungeul registry가 없다")
	}
	if !ungeul.FeatureEnabled("iap") {
		t.Fatal("운글 IAP feature가 비활성이다")
	}
	if ungeul.IAP.LedgerEnvironment != LedgerProduction {
		t.Fatalf("ledger environment=%q, want production", ungeul.IAP.LedgerEnvironment)
	}
	wantMarkets := []string{"google_play", "app_store", "apps_in_toss"}
	if len(ungeul.IAP.Markets) != len(wantMarkets) {
		t.Fatalf("markets=%v, want %v", ungeul.IAP.Markets, wantMarkets)
	}
	for _, market := range wantMarkets {
		if !ungeul.MarketEnabled(market) {
			t.Fatalf("market %q가 비활성이다", market)
		}
	}
	if ungeul.IAP.GooglePlayPackageName != "com.seorilabs.ungeul" ||
		ungeul.IAP.AppStoreBundleID != "com.seorilabs.ungeul" {
		t.Fatalf("native app identifiers=(%q,%q)",
			ungeul.IAP.GooglePlayPackageName, ungeul.IAP.AppStoreBundleID)
	}
	if len(ungeul.IAP.EntitlementIDs) != 1 ||
		ungeul.IAP.EntitlementIDs[0] != "deep_reading_ticket" {
		t.Fatalf("entitlements=%v, want deep_reading_ticket", ungeul.IAP.EntitlementIDs)
	}
	if !ungeul.IAP.RequireLinkedAccount {
		t.Fatal("네이티브 구매의 연결 계정 요구가 비활성이다")
	}
	// 카카오 ID token의 aud는 앱 ID가 아니라 SDK 초기화에 쓴 앱 키다. 앱 ID를 넣으면
	// 사용자가 카카오 동의까지 마친 뒤 검증에서만 떨어져 원인을 찾기 어렵다.
	if got := ungeul.Auth.AccountProviders["kakao"].Audience; got != "4d309d86b98ea5db999bd1603b8c6c29" {
		t.Fatalf("Kakao audience=%q, want 네이티브 앱 키", got)
	}
	if got := ungeul.Auth.AccountProviders["apple"].Audience; got != "com.seorilabs.ungeul" {
		t.Fatalf("Apple audience=%q, want com.seorilabs.ungeul", got)
	}
	if ungeul.Content.TicketEntitlementID != "deep_reading_ticket" ||
		ungeul.Content.TicketUnitsPerPurchase != 5 {
		t.Fatalf("ticket content contract=(%q,%d), want (deep_reading_ticket,5)",
			ungeul.Content.TicketEntitlementID, ungeul.Content.TicketUnitsPerPurchase)
	}
}
