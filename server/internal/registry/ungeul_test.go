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
