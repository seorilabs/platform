package ads

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// 복사한 provider fixture 대신 실제 출시 registry와 SSV 연결부를 함께 검사한다.
func TestSpiritgateRegistryAdMobConfirmation(t *testing.T) {
	apps, err := registry.NewFSSource(os.DirFS("../../../registry"), "apps").LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var app registry.App
	for _, candidate := range apps {
		if candidate.AppID == "spiritgate-defenders" {
			app = candidate
			break
		}
	}
	if app.AppID == "" {
		t.Fatal("Spiritgate registry가 없다")
	}
	for _, placement := range app.Ads.Placements {
		t.Run(placement.ID, func(t *testing.T) {
			for _, tc := range []struct {
				name, platform, unit, item string
				amount                     int
				code                       platformerr.Code
			}{
				{"android 현재", "android", "2352755575", "in_game_bonus", 1, ""},
				{"android 기존 설치본", "android", "6693003024", "in_game_bonus", 1, ""},
				{"ios 현재", "ios", "8374493589", "in_game_bonus", 1, ""},
				{"ios 기존 설치본", "ios", "9364182214", "in_game_bonus", 1, ""},
				{"android에서 ios 현재 거절", "android", "8374493589", "in_game_bonus", 1, platformerr.CodeAdUnitMismatch},
				{"ios에서 android 기존 거절", "ios", "6693003024", "in_game_bonus", 1, platformerr.CodeAdUnitMismatch},
				{"미등록 unit 거절", "android", "9999999999", "in_game_bonus", 1, platformerr.CodeAdUnitMismatch},
				{"AdMob 기본 항목 거절", "android", "2352755575", "reward", 1, platformerr.CodeAdRewardInvalid},
				{"다른 수량 거절", "ios", "8374493589", "in_game_bonus", 2, platformerr.CodeAdRewardInvalid},
			} {
				t.Run(tc.name, func(t *testing.T) {
					repo := &fakeRepo{claims: map[string]Claim{"cl_1": {
						ClaimID: "cl_1", AppID: app.AppID, PlatformUserID: "pu_1",
						PlacementID: placement.ID, Provider: "admob", ClientPlatform: tc.platform,
						State: StateAccepted, ExpiresAt: time.Now().Add(time.Hour),
					}}}
					svc, err := NewService(repo, fakeApps{app.AppID: app}, fakeEntitlements{}, fakeUsers{})
					if err != nil {
						t.Fatal(err)
					}
					claim, err := svc.ConfirmAdMob(context.Background(), app.AppID, SSVResult{
						ClaimID: "cl_1", PlatformUserID: "pu_1", AdUnitID: tc.unit,
						TransactionID: "tx", RewardItem: tc.item, RewardAmount: tc.amount,
					})
					if platformerr.CodeOf(err) != tc.code {
						t.Fatalf("code = %q, want %q", platformerr.CodeOf(err), tc.code)
					}
					if tc.code == "" && (claim.State != StateConfirmed || claim.Assurance != AssuranceServerVerified) {
						t.Fatalf("확정 연결 실패: state=%q assurance=%q", claim.State, claim.Assurance)
					}
					if tc.code != "" && repo.claims["cl_1"].State != StateAccepted {
						t.Fatal("거절한 callback이 claim을 변경했다")
					}
				})
			}
		})
	}
}
