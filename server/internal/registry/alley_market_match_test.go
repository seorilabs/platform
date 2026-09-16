package registry

import (
	"context"
	"os"
	"testing"
)

// 게임의 실패 결과 화면은 지면 하나와 이동 5회 보상만 요청한다. 클라이언트와
// AdMob 콘솔 중 하나라도 달라지면 광고 시청 뒤 SSV가 거부되므로 원장 계약을 고정한다.
func TestAlleyMarketMatchAdsRegistryContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var alley *App
	for i := range apps {
		if apps[i].AppID == "alley-market-match" {
			alley = &apps[i]
			break
		}
	}
	if alley == nil {
		t.Fatal("Alley Market Match registry가 없다")
	}
	if !alley.FeatureEnabled("firebase_custom_token_bridge") || !alley.FeatureEnabled("ads") {
		t.Fatal("Firebase custom token bridge와 광고 feature가 모두 활성이어야 한다")
	}
	if alley.RequireAppCheck {
		t.Fatal("실기기 attestation 검증 전에는 App Check를 강제하지 않는다")
	}

	placement, ok := alley.AdsPlacement("stage_retry_boost")
	if !ok || placement.Format != "rewarded" {
		t.Fatalf("stage_retry_boost rewarded 지면이 없다: %+v", placement)
	}
	if placement.Reward == nil || placement.Reward.Key != "moves" ||
		placement.Reward.MinAmount != 5 || placement.Reward.MaxAmount != 5 {
		t.Fatalf("이동 보상 계약이 클라이언트와 다르다: %+v", placement.Reward)
	}
	if placement.DailyLimit != 20 || placement.CooldownSeconds != 30 {
		t.Fatalf("광고 운영 제한이 다르다: daily=%d cooldown=%d",
			placement.DailyLimit, placement.CooldownSeconds)
	}

	provider, ok := placement.Providers["admob"]
	if !ok {
		t.Fatal("AdMob provider 설정이 없다")
	}
	if provider.AndroidAdUnitID != "ca-app-pub-9932778305312246/3020251241" ||
		provider.IOSAdUnitID != "ca-app-pub-9932778305312246/6934998415" {
		t.Fatalf("AdMob unit이 콘솔과 다르다: %+v", provider)
	}
	if provider.RewardItem != "moves" || provider.RewardAmount != 5 {
		t.Fatalf("AdMob SSV 보상 설정이 콘솔과 다르다: item=%q amount=%d",
			provider.RewardItem, provider.RewardAmount)
	}
}
