package ads

import (
	"context"
	"os"
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// 조물조물의 클라이언트는 힌트 보상 하나만 요청한다. placement id, 보상 key, 보상량은
// 게임 코드의 상수(JmGodotAdMobAds.PLACEMENT/REWARD_KEY, JmHintEconomy.REWARDED_AD_GRANT)와
// 짝을 이루므로, 레지스트리만 바뀌면 실기기에서 claim이 전부 거부된다.
// 마켓에 나간 빌드는 되돌릴 수 없으니 그 짝을 여기서 고정한다.
func TestJomulRewardedContractMatchesClient(t *testing.T) {
	jomul := loadRegistryApp(t, "jomul")

	if !jomul.FeatureEnabled("ads") {
		t.Fatal("jomul 광고 기능이 꺼져 있다")
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
	// 아동 대상 제품이라 하루 3회, 1회 1편이 제품 계약이다.
	if placement.DailyLimit != 3 {
		t.Fatalf("daily_limit=%d, want 3", placement.DailyLimit)
	}
	provider, ok := placement.Providers["admob"]
	if !ok || provider.AndroidAdUnitID == "" {
		t.Fatal("Android AdMob unit이 없다")
	}
	// iOS Kids 빌드는 광고 SDK를 링크하지 않는다. unit을 등록하면 그 경계가 흐려진다.
	if provider.IOSAdUnitID != "" {
		t.Fatalf("iOS unit이 등록됐다: %q", provider.IOSAdUnitID)
	}
}

// 클라이언트가 보내는 보상량은 언제나 3이다. 그 밖의 값은 거부돼야 위·변조된 요청이
// 힌트를 더 받아 가지 못한다.
func TestJomulRejectsRewardsOutsideRegistryRange(t *testing.T) {
	jomul := loadRegistryApp(t, "jomul")
	placement, ok := jomul.AdsPlacement("hint_reward")
	if !ok {
		t.Fatal("hint_reward 지면이 없다")
	}

	svc, err := NewService(&fakeRepo{}, fakeApps{jomul.AppID: jomul}, fakeEntitlements{}, fakeUsers{})
	if err != nil {
		t.Fatal(err)
	}

	for _, amount := range []int{placement.Reward.MinAmount - 1, placement.Reward.MaxAmount + 1} {
		_, err := svc.CreateClaim(context.Background(), CreateClaimInput{
			RequestID:      "jomul-reward-range",
			AppID:          jomul.AppID,
			PlatformUserID: "pu_jomul",
			SupportCode:    "JOMUL",
			PlacementID:    placement.ID,
			Provider:       "admob",
			ClientPlatform: "android",
			Reward:         Reward{Key: placement.Reward.Key, Amount: amount},
		})
		if platformerr.CodeOf(err) != platformerr.CodeAdRewardInvalid {
			t.Fatalf("amount=%d code=%q, want %q", amount, platformerr.CodeOf(err), platformerr.CodeAdRewardInvalid)
		}
	}
}

func loadRegistryApp(t *testing.T, appID string) registry.App {
	t.Helper()
	source := registry.NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.AppID == appID {
			return app
		}
	}
	t.Fatalf("%s registry가 없다", appID)
	return registry.App{}
}
