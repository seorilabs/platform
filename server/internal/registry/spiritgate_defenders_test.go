package registry

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestSpiritgateDefendersAdsRegistryContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var spiritgate *App
	for i := range apps {
		if apps[i].AppID == "spiritgate-defenders" {
			spiritgate = &apps[i]
			break
		}
	}
	if spiritgate == nil {
		t.Fatal("spiritgate-defenders registry가 없다")
	}
	if !spiritgate.FeatureEnabled("ads") {
		t.Fatal("Spiritgate 광고 feature가 비활성이다")
	}
	if !reflect.DeepEqual(spiritgate.Ads.Providers, []string{"admob"}) {
		t.Fatalf("광고 provider가 다르다: %#v", spiritgate.Ads.Providers)
	}

	wantPlacements := map[string]AdsPlacementConfig{
		"revive": {
			Reward:     &AdsRewardConfig{Key: "revive", MinAmount: 1, MaxAmount: 1},
			DailyLimit: 10,
		},
		"result_double": {
			Reward:     &AdsRewardConfig{Key: "spirit_stones", MinAmount: 1, MaxAmount: 426},
			DailyLimit: 10,
		},
		"relic_reroll": {
			Reward:     &AdsRewardConfig{Key: "relic_reroll", MinAmount: 1, MaxAmount: 1},
			DailyLimit: 10,
		},
		"prep_gold": {
			Reward:     &AdsRewardConfig{Key: "gold", MinAmount: 40, MaxAmount: 385},
			DailyLimit: 20,
		},
	}
	if len(spiritgate.Ads.Placements) != len(wantPlacements) {
		t.Fatalf("광고 지면 수 = %d, want %d", len(spiritgate.Ads.Placements), len(wantPlacements))
	}
	for _, placement := range spiritgate.Ads.Placements {
		want, ok := wantPlacements[placement.ID]
		if !ok {
			t.Fatalf("예상하지 않은 광고 지면: %q", placement.ID)
		}
		delete(wantPlacements, placement.ID)
		provider, ok := placement.Providers["admob"]
		if !ok {
			t.Fatalf("%s: AdMob provider가 없다", placement.ID)
		}
		if placement.Format != "rewarded" || placement.CooldownSeconds != 30 || placement.DailyLimit != want.DailyLimit {
			t.Fatalf("%s: 지면 정책이 다르다: %#v", placement.ID, placement)
		}
		if !reflect.DeepEqual(placement.Reward, want.Reward) {
			t.Fatalf("%s: 보상 범위가 다르다: %#v", placement.ID, placement.Reward)
		}
		if provider.AndroidAdUnitID != "ca-app-pub-2444587584524186/6693003024" ||
			provider.IOSAdUnitID != "ca-app-pub-2444587584524186/9364182214" {
			t.Fatalf("%s: 모바일 AdMob unit이 다르다: %#v", placement.ID, provider)
		}
		if provider.RewardItem != "in_game_bonus" || provider.RewardAmount != 1 {
			t.Fatalf("%s: SSV 보상 계약이 다르다: %#v", placement.ID, provider)
		}
	}
	if len(wantPlacements) != 0 {
		t.Fatalf("누락된 광고 지면이 있다: %#v", wantPlacements)
	}

	for _, event := range []string{"ad_reward_requested", "ad_reward_granted", "ad_request_ignored", "ad_impression"} {
		if !spiritgate.EventAllowed(event) {
			t.Fatalf("실제 앱 광고 이벤트 %q가 allowlist에 없다", event)
		}
	}
	if spiritgate.EventAllowed("unregistered_ad_event") {
		t.Fatal("등록하지 않은 광고 이벤트가 허용됐다")
	}
}
