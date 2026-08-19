package registry

import (
	"context"
	"os"
	"testing"
)

// 조물조물의 클라이언트는 힌트 보상 지면 하나만 요청한다. placement id, 보상 key, 보상량,
// 하루 한도는 게임 코드의 상수와 짝을 이룬다 — JmGodotAdMobAds.PLACEMENT/REWARD_KEY 와
// JmHintEconomy.REWARDED_AD_GRANT/REWARDED_AD_DAILY_CAP. 레지스트리만 움직이면 이미 마켓에
// 나간 빌드에서 claim 이 전부 거부되므로 여기서 짝을 고정한다.
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
	// 아동 대상 제품의 「하루 3회, 1회 1편」이 제품 계약이다.
	if placement.DailyLimit != 3 {
		t.Fatalf("daily_limit=%d, want 3", placement.DailyLimit)
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
	// iOS 빌드는 Kids Category 대상이라 광고 SDK를 링크하지 않는다. AdMob 콘솔에는 iOS 앱과
	// unit이 따로 있지만 레지스트리에 넣으면 그 경계가 흐려지고 SSV 대조 대상만 늘어난다.
	if provider.IOSAdUnitID != "" {
		t.Fatalf("iOS unit이 등록됐다: %q", provider.IOSAdUnitID)
	}
	// 콘솔 값과 한 글자라도 다르면 SSV가 전건 거부되므로 보상 항목·수량은 registry에 적지 않는다.
	if provider.RewardItem != "" || provider.RewardAmount != 0 {
		t.Fatalf("AdMob 콘솔 보상 계약이 registry에 박혔다: item=%q amount=%d",
			provider.RewardItem, provider.RewardAmount)
	}
}
