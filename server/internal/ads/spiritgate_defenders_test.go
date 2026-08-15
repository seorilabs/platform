package ads

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

func TestSpiritgateDefendersRejectsRewardsOutsideRegistryRange(t *testing.T) {
	source := registry.NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var spiritgate registry.App
	for _, app := range apps {
		if app.AppID == "spiritgate-defenders" {
			spiritgate = app
			break
		}
	}
	if spiritgate.AppID == "" {
		t.Fatal("spiritgate-defenders registry가 없다")
	}

	repo := &fakeRepo{}
	svc, err := NewService(repo, fakeApps{spiritgate.AppID: spiritgate}, fakeEntitlements{}, fakeUsers{})
	if err != nil {
		t.Fatal(err)
	}

	for _, placement := range spiritgate.Ads.Placements {
		if placement.Format != "rewarded" {
			continue
		}
		if placement.Reward == nil {
			t.Fatalf("%s: rewarded 지면에 보상 계약이 없다", placement.ID)
		}
		for _, amount := range []int{placement.Reward.MinAmount - 1, placement.Reward.MaxAmount + 1} {
			t.Run(placement.ID+"_reject_"+strconv.Itoa(amount), func(t *testing.T) {
				_, err := svc.CreateClaim(context.Background(), CreateClaimInput{
					RequestID:      placement.ID + "-" + strconv.Itoa(amount),
					AppID:          spiritgate.AppID,
					PlatformUserID: "pu_spiritgate",
					SupportCode:    "SPIRITGATE",
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
}
