package registry

import (
	"context"
	"os"
	"testing"
)

// 운글은 2026-09-29 부터 광고만으로 운영한다. 전면·보상형 광고는 클라이언트가 직접 띄우고
// 서버는 권한을 기록하지 않는다 — 열람권(IAP)·계정 연결·서버 광고 지면(SSV)은 전부 걷었다.
// 그래서 심화 본문은 `deep_always_open` 으로 늘 내려가야 하고, 걷은 설정이 되살아나면
// 앱이 부르지 않는 경로가 원장에 남아 무엇이 잠금을 푸는지 두 갈래로 말하게 된다.
func TestUngeulAdOnlyRegistryContract(t *testing.T) {
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
	if !ungeul.FeatureEnabled("content") || !ungeul.Content.DeepAlwaysOpen {
		t.Fatal("운글 심화는 권한 확인 없이 항상 열려야 한다")
	}
	if !ungeul.Content.PairingEnabled {
		t.Fatal("운글 궁합이 꺼져 있다")
	}
	if ungeul.FeatureEnabled("iap") || ungeul.FeatureEnabled("ads") {
		t.Fatalf("운글은 서버 IAP·광고 경로를 쓰지 않는다: iap=%v ads=%v",
			ungeul.FeatureEnabled("iap"), ungeul.FeatureEnabled("ads"))
	}
	if len(ungeul.Ads.Placements) != 0 || len(ungeul.IAP.EntitlementIDs) != 0 ||
		len(ungeul.Auth.AccountProviders) != 0 {
		t.Fatalf("걷은 설정이 남아 있다: ads=%+v iap=%+v auth=%+v", ungeul.Ads, ungeul.IAP, ungeul.Auth)
	}
	if ungeul.Content.RewardKey != "" || ungeul.Content.TicketEntitlementID != "" {
		t.Fatalf("content 에 광고 보상·열람권 설정이 남아 있다: %+v", ungeul.Content)
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
		"deep_gate_shown", "deep_means_selected",
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
