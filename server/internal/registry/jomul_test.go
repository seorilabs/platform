package registry

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestJomulAnalyticsRegistryContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var jomul *App
	for i := range apps {
		if apps[i].AppID == "jomul" {
			jomul = &apps[i]
			break
		}
	}
	if jomul == nil {
		t.Fatal("jomul registry가 없다")
	}
	if !jomul.FeatureEnabled("events") {
		t.Fatal("조물조물 events 기능이 비활성이다")
	}
	if jomul.GA4.EventPrefix != "jomul_" || jomul.GA4.MeasurementID != "G-6PXDPK349G" {
		t.Fatalf("조물조물 GA4 계약이 다르다: %#v", jomul.GA4)
	}
	want := []string{
		"session_start", "clock_rollback_detected", "merge_attempt", "element_discovered",
		"recipe_discovered", "chapter_cleared", "chapter_complete", "chapter_unlocked",
		"save_migrated", "hint_used", "hint_earned", "onboarding_start",
		"onboarding_complete", "onboarding_skip", "rewarded_complete", "element_read",
		"easter_egg_found", "rewarded_start", "rewarded_failed", "dictionary_open", "stuck",
		"save_failed",
	}
	if !reflect.DeepEqual(jomul.PlatformEventAllowlist, want) {
		t.Fatalf("조물조물 이벤트 allowlist가 다르다\n got: %#v\nwant: %#v", jomul.PlatformEventAllowlist, want)
	}
	if jomul.EventAllowed("email") || jomul.EventAllowed("screen_view") {
		t.Fatal("등록하지 않은 이벤트가 허용됐다")
	}
}

// 조물조물의 클라이언트는 힌트 보상 지면 하나만 요청한다. placement id, 보상 key, 보상량,
// 하루 한도는 게임 코드의 상수와 짝을 이룬다 — JmGodotAdMobAds 및 JmAitRewardedAds 의
// PLACEMENT/REWARD_KEY 와 JmHintEconomy.REWARDED_AD_GRANT/REWARDED_AD_DAILY_CAP.
// 레지스트리만 움직이면 이미 마켓에 나간 빌드에서 claim 이 전부 거부되므로 여기서 짝을 고정한다.
func TestJomulAdsRegistryContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var jomul *App
	for i := range apps {
		if apps[i].AppID == "jomul" {
			jomul = &apps[i]
			break
		}
	}
	if jomul == nil {
		t.Fatal("jomul registry가 없다")
	}
	if !jomul.FeatureEnabled("ads") {
		t.Fatal("조물조물 광고 feature가 비활성이다")
	}
	// registry와 앱 opt-in은 둘 다 켜져야 heartbeat가 돈다. registry만 꺼지면 token이
	// enabled=false 로 돌아가 이미 마켓에 나간 빌드의 동접이 통째로 사라진다.
	if !jomul.FeatureEnabled("presence") {
		t.Fatal("조물조물 presence feature가 비활성이다")
	}

	placement, ok := jomul.AdsPlacement("hint_reward")
	if !ok {
		t.Fatal("hint_reward 지면이 없다")
	}
	if placement.Format != "rewarded" {
		t.Fatalf("format=%q, want rewarded", placement.Format)
	}
	if placement.Reward == nil || placement.Reward.Key != "hint" ||
		placement.Reward.MinAmount != 3 || placement.Reward.MaxAmount != 3 {
		t.Fatalf("보상 계약이 클라이언트와 다르다: %+v", placement.Reward)
	}
	// 아동 대상 제품의 「힌트 0개에서 하루 1회, 1회 1편」이 제품 계약이다.
	if placement.DailyLimit != 1 {
		t.Fatalf("daily_limit=%d, want 1", placement.DailyLimit)
	}

	provider, ok := placement.Providers["admob"]
	if !ok {
		t.Fatal("AdMob provider 설정이 없다")
	}
	// unit 을 고정 비교한다. 반대편 원본은 코드가 아니라 seorilabs/jomul 의 google-play
	// 환경 변수 ADMOB_REWARDED_AD_UNIT_ID 이고, v0.1.5 signed AAB 가 이 unit 으로 빌드됐다.
	// 클라이언트가 요청한 unit 과 다르면 광고는 재생되고 SSV 만 CodeAdUnitMismatch 로 거부된다.
	// 실제로 이 PR 의 첫 커밋이 iOS unit 을 넣었다가 바로잡았다.
	const wantAndroidUnit = "ca-app-pub-2444587584524186/1396162476"
	if provider.AndroidAdUnitID != wantAndroidUnit {
		t.Fatalf("Android unit=%q, want %q", provider.AndroidAdUnitID, wantAndroidUnit)
	}
	// iOS 는 2026-09-05 까지 비어 있었다. Kids Category 대상이라 광고 SDK 를 링크하지 않았고,
	// 레지스트리에 넣으면 SSV 대조 대상만 늘었기 때문이다. seorilabs/jomul 의 ADR 0017 이 그
	// 전제를 뒤집어 iOS 를 4+ 일반 카테고리로 옮기고 Android 와 같은 리워드 광고를 붙이기로 했다.
	// 이 값이 없으면 iOS claim 은 ConfirmAdMob 이 CodeAdUnitMismatch 로 거부한다.
	const wantIOSUnit = "ca-app-pub-2444587584524186/4203846143"
	if provider.IOSAdUnitID != wantIOSUnit {
		t.Fatalf("iOS unit=%q, want %q", provider.IOSAdUnitID, wantIOSUnit)
	}
	// 콘솔 값과 한 글자라도 다르면 SSV가 전건 거부되므로 보상 항목·수량은 registry에 적지 않는다.
	if provider.RewardItem != "" || provider.RewardAmount != 0 {
		t.Fatalf("AdMob 콘솔 보상 계약이 registry에 박혔다: item=%q amount=%d",
			provider.RewardItem, provider.RewardAmount)
	}

	// AIT는 서버형 SSV가 없어 광고 SDK의 userEarnedReward를 받은 뒤 로컬 exactly-once 원장으로
	// 확정한다. provider나 그룹 ID가 빠지면 광고 자체가 로드되지 않으므로 콘솔 그룹을 고정한다.
	ait, ok := placement.Providers["apps_in_toss"]
	if !ok {
		t.Fatal("AppsInToss provider 설정이 없다")
	}
	if ait.AdGroupID != "ait.v2.live.d6bba0043afa4672" {
		t.Fatalf("AppsInToss ad group=%q", ait.AdGroupID)
	}
}

// AppsInToss SDK 2.x와 3.x의 실제·private WebView origin 모두에서 익명 이벤트와
// 광고 요청이 CORS를 통과해야 한다. app_id와 콘솔 appName을 혼동하면 preview에서만 실패한다.
func TestJomulCORSOriginsCoverBothAppsInTossGenerations(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var jomul *App
	for i := range apps {
		if apps[i].AppID == "jomul" {
			jomul = &apps[i]
			break
		}
	}
	if jomul == nil {
		t.Fatal("jomul registry가 없다")
	}

	required := map[string]string{
		"https://jomul-game.apps.tossmini.com":         "AppsInToss SDK 2.x 실제 서비스",
		"https://jomul-game.private-apps.tossmini.com": "AppsInToss SDK 2.x 콘솔 preview",
		"https://jomul-game.web.tossmini.com":          "AppsInToss SDK 3.x 실제 서비스",
		"https://jomul-game.private-web.tossmini.com":  "AppsInToss SDK 3.x 콘솔 preview",
	}
	present := make(map[string]bool, len(jomul.CORSOrigins))
	for _, origin := range jomul.CORSOrigins {
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

	r := New(staticRegistrySource{apps: []App{*jomul}})
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
