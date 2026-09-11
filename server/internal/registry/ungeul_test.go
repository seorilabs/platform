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
//
// AppsInToss 도 같은 지면을 쓴다. 서버는 보상 claim 에서 지면에 등록된 provider 만
// 허용하므로 `apps_in_toss` 가 빠지면 광고를 끝까지 봐도 청구가 막힌다. ad group id
// 는 클라이언트가 `VITE_AIT_REWARD_AD_GROUP_ID` 로 굽는 값과 같아야 한다 — 서버는
// 지면에 provider 가 있는지만 보고 group id 는 대조하지 않아서 어긋나도 런타임에
// 드러나지 않는다.
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

	ait, ok := placement.Providers["apps_in_toss"]
	if !ok {
		t.Fatal("AppsInToss provider 설정이 없다")
	}
	if ait.AdGroupID != "ait.v2.live.44b40e237fed4252" {
		t.Fatalf("AppsInToss ad group=%q", ait.AdGroupID)
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

// 운글 클라이언트의 분석 계약(saju-reader `design/saju-reader/analytics/event-contract.json`)이
// 보내는 이벤트 이름 전부가 allowlist 에 있어야 한다. 서버는 allowlist 밖 이벤트를 오류 없이
// 200 으로 버리므로(`events/handler.go`), 이름 하나가 빠지면 앱은 정상인데 적재만 0건이 된다.
// 계약에 이벤트를 더한 PR 이 이 목록도 함께 늘리도록 여기서 고정한다.
func TestUngeulEventAllowlistCoversClientContract(t *testing.T) {
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

	clientContract := []string{
		// 첫 리딩 퍼널과 결과 화면.
		"home_viewed", "input_step_viewed", "reading_started", "reading_input_blocked",
		"time_choice_shown", "time_choice_completed", "reading_generated",
		"reading_section_viewed", "term_help_opened", "term_label_rendered",
		// 심화 게이트와 보상형 광고.
		"deep_gate_shown", "deep_means_selected", "deep_ticket_used",
		"reward_ad_requested", "reward_ad_shown", "reward_ad_granted",
		"reward_ad_declined", "reward_ad_closed",
		// 정체성 카드 공유·홈 타일·궁합.
		"identity_share_requested", "identity_share_outcome", "home_tile_tapped",
		"pairing_picker_viewed", "pairing_started", "pairing_generated", "pairing_section_viewed",
		// 체감 일치도 문항 노출·응답·닫음 (`feedback-contract.json`). 점수·이유는 GA4 로 오지 않는다.
		"feedback_prompt_shown", "feedback_prompt_responded", "feedback_prompt_dismissed",
	}
	expected := make(map[string]bool, len(clientContract))
	for _, name := range clientContract {
		expected[name] = true
		if !ungeul.EventAllowed(name) {
			t.Errorf("클라이언트 계약 이벤트 %q 가 platform_event_allowlist 에 없다", name)
		}
	}
	seen := make(map[string]bool, len(ungeul.PlatformEventAllowlist))
	for _, name := range ungeul.PlatformEventAllowlist {
		if seen[name] {
			t.Errorf("platform_event_allowlist 에 %q 가 중복이다", name)
		}
		seen[name] = true
		// 어느 계약에도 없는 이름이 allowlist 에 남으면 서버가 받는 것과 앱이 보내는 것이 갈린 것이다.
		if !expected[name] {
			t.Errorf("platform_event_allowlist 의 %q 는 어느 클라이언트 계약에도 없다", name)
		}
	}
	if len(ungeul.PlatformEventAllowlist) != len(clientContract) {
		t.Fatalf("allowlist=%d, want %d (계약과 같은 크기)", len(ungeul.PlatformEventAllowlist), len(clientContract))
	}
}

// AppsInToss SDK 는 미니앱을 버전에 따라 다른 호스트에서 띄운다. 2.x 는
// `<app>.apps.tossmini.com`, 3.x 는 `<app>.web.tossmini.com` 이고 콘솔 QR 테스트 환경도
// 같은 규칙으로 갈린다. 레지스트리에 없는 origin 은 서버가 preflight 부터 거부하므로,
// 한쪽만 남으면 그 세대의 번들에서 해설·결제·광고·분석이 한꺼번에 막힌다.
//
// **두 세대를 함께 담아 둔다.** 새 번들을 올려도 사용자가 받기 전까지는 옛 호스트에서
// 계속 요청하기 때문이다. 3.x 가 충분히 퍼진 뒤에 2.x 쪽을 걷는 것은 별도 판단이고,
// 그때까지 어느 한쪽이 조용히 사라지지 않게 여기서 고정한다.
func TestUngeulCORSOriginsCoverBothAppsInTossGenerations(t *testing.T) {
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

	required := map[string]string{
		"https://ungeul.apps.tossmini.com":         "AppsInToss SDK 2.x 실제 서비스",
		"https://ungeul.private-apps.tossmini.com": "AppsInToss SDK 2.x 콘솔 QR 테스트",
		"https://ungeul.web.tossmini.com":          "AppsInToss SDK 3.x 실제 서비스",
		"https://ungeul.private-web.tossmini.com":  "AppsInToss SDK 3.x 콘솔 QR 테스트",
	}
	present := make(map[string]bool, len(ungeul.CORSOrigins))
	for _, origin := range ungeul.CORSOrigins {
		if present[origin] {
			t.Errorf("cors_origins 에 %q 가 중복이다", origin)
		}
		present[origin] = true
	}
	for origin, why := range required {
		if !present[origin] {
			t.Errorf("cors_origins 에 %q 가 없다 — %s 번들이 막힌다", origin, why)
		}
	}

	// 실제로 서버가 그 origin 을 통과시키는지까지 본다. 목록에 적힌 것과 판정이 갈리면
	// 레지스트리만 고쳐 두고 런타임은 거부하는 상태가 된다.
	r := New(staticRegistrySource{apps: []App{*ungeul}})
	for origin := range required {
		allowed, err := r.AllowsCORSOrigin(context.Background(), origin)
		if err != nil {
			t.Fatalf("AllowsCORSOrigin(%q) error = %v", origin, err)
		}
		if !allowed {
			t.Errorf("등록된 origin 인데 거부됐다: %s", origin)
		}
	}
}
