package registry

import (
	"context"
	"os"
	"testing"
)

// 게임이 요청하는 보상형 지면 세 개의 계약을 고정한다. 클라이언트·AdMob 콘솔·레지스트리 중
// 하나라도 달라지면 광고 시청 뒤 claim 생성이나 SSV가 거부되므로 원장 계약을 고정한다.
//   - stage_retry_boost: 실패 결과 화면에서 이동 +5
//   - rewarded_result_bonus: 결과 화면에서 이번 클리어 코인을 한 번 더(= 2배)
//   - rewarded_hurdle_skip: 하트가 없을 때 하트 +1
//
// AdMob 쪽 보상(reward_item/amount)은 SSV 대조용 고정 표식이고, 실제 지급량은 claim 의
// reward 범위가 정한다. 코인 2배는 판마다 금액이 달라 범위로 둔다(spiritgate result_double 과 같은 방식).
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
	if err := alley.Validate(); err != nil {
		t.Fatalf("레지스트리 항목이 유효하지 않다: %v", err)
	}
	if alley.FirebaseProjectID != "alley-market-match" ||
		alley.FirebaseCustomTokenServiceAccount != "platform-auth@alley-market-match.iam.gserviceaccount.com" {
		t.Fatalf("Firebase 인증 계약이 다르다: project=%q service_account=%q",
			alley.FirebaseProjectID, alley.FirebaseCustomTokenServiceAccount)
	}
	if !alley.FeatureEnabled("firebase_custom_token_bridge") || !alley.FeatureEnabled("ads") {
		t.Fatal("Firebase custom token bridge와 광고 feature가 모두 활성이어야 한다")
	}
	if alley.RequireAppCheck {
		t.Fatal("실기기 attestation 검증 전에는 App Check를 강제하지 않는다")
	}

	cases := []struct {
		id           string
		rewardKey    string
		minAmount    int
		maxAmount    int
		dailyLimit   int
		cooldown     int
		androidUnit  string
		iosUnit      string
		rewardItem   string
		rewardAmount int
	}{
		// 보상형 광고 하루 합계를 조직 rewarded-only 기준 10회(4+3+3)에 맞춘다.
		{"stage_retry_boost", "moves", 5, 5, 4, 120,
			"ca-app-pub-9932778305312246/3020251241", "ca-app-pub-9932778305312246/6934998415", "moves", 5},
		// 게임의 한 판 최대 코인은 3별 20 + 챕터 복구 40 = 60 (alley-market-match godot/data/economy.json).
		// 경제 수치를 올리면 max_amount 도 함께 올려야 2배 claim 이 거부되지 않는다.
		{"rewarded_result_bonus", "coins", 1, 60, 3, 60,
			"ca-app-pub-9932778305312246/7802918027", "ca-app-pub-9932778305312246/1904643959", "coin_bonus", 1},
		{"rewarded_hurdle_skip", "lives", 1, 1, 3, 180,
			"ca-app-pub-9932778305312246/4790751034", "ca-app-pub-9932778305312246/3066605822", "life", 1},
	}
	if len(alley.Ads.Placements) != len(cases) {
		t.Fatalf("지면 수가 클라이언트 계약과 다르다: got %d want %d", len(alley.Ads.Placements), len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			placement, ok := alley.AdsPlacement(tc.id)
			if !ok || placement.Format != "rewarded" {
				t.Fatalf("%s rewarded 지면이 없다: %+v", tc.id, placement)
			}
			if placement.Reward == nil || placement.Reward.Key != tc.rewardKey ||
				placement.Reward.MinAmount != tc.minAmount || placement.Reward.MaxAmount != tc.maxAmount {
				t.Fatalf("보상 계약이 클라이언트와 다르다: %+v", placement.Reward)
			}
			if placement.DailyLimit != tc.dailyLimit || placement.CooldownSeconds != tc.cooldown {
				t.Fatalf("광고 운영 제한이 다르다: daily=%d cooldown=%d",
					placement.DailyLimit, placement.CooldownSeconds)
			}
			provider, ok := placement.Providers["admob"]
			if !ok {
				t.Fatal("AdMob provider 설정이 없다")
			}
			if provider.AndroidAdUnitID != tc.androidUnit || provider.IOSAdUnitID != tc.iosUnit {
				t.Fatalf("AdMob unit이 콘솔과 다르다: %+v", provider)
			}
			if provider.RewardItem != tc.rewardItem || provider.RewardAmount != tc.rewardAmount {
				t.Fatalf("AdMob SSV 보상 설정이 콘솔과 다르다: item=%q amount=%d",
					provider.RewardItem, provider.RewardAmount)
			}
		})
	}
}
