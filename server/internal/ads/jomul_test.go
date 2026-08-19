package ads

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// 클라이언트가 보내는 보상량은 언제나 3이다. 그 밖의 값이 통과하면 위·변조된 요청이
// 힌트를 더 받아 간다. 레지스트리 범위가 실제로 그것을 막는지 본다.
func TestJomulRejectsRewardsOutsideRegistryRange(t *testing.T) {
	source := registry.NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var jomul registry.App
	for _, app := range apps {
		if app.AppID == "jomul" {
			jomul = app
			break
		}
	}
	if jomul.AppID == "" {
		t.Fatal("jomul registry가 없다")
	}

	placement, ok := jomul.AdsPlacement("hint_reward")
	if !ok {
		t.Fatal("hint_reward 지면이 없다")
	}

	svc, err := NewService(&fakeRepo{}, fakeApps{jomul.AppID: jomul}, fakeEntitlements{}, fakeUsers{})
	if err != nil {
		t.Fatal(err)
	}

	for _, amount := range []int{placement.Reward.MinAmount - 1, placement.Reward.MaxAmount + 1} {
		t.Run("reject_"+strconv.Itoa(amount), func(t *testing.T) {
			_, err := svc.CreateClaim(context.Background(), CreateClaimInput{
				RequestID:      "jomul-reward-" + strconv.Itoa(amount),
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
		})
	}
}
