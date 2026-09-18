package registry

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

// slotmachine-game은 GDScript Presence 확대 대상이다. Phase A가 병합되고 앱
// 원장의 opt-in이 켜진 뒤에만 registry를 연다. 이 계약이 풀리면 앱이 요청해도
// token 발급이 비활성으로 돌아가거나, 반대로 준비되지 않은 앱에 열린다.
//
// 후보: seorilabs/slotmachine-game 756427b00253954445e232465fb7122761bc87b9
// (presenceEnabled=true, Platform GDScript SDK 0.6.6)
func TestSlotmachineGameRegistryPresenceContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var slotmachine *App
	for i := range apps {
		if apps[i].AppID == "slotmachine-game" {
			slotmachine = &apps[i]
			break
		}
	}
	if slotmachine == nil {
		t.Fatal("slotmachine-game registry가 없다")
	}

	if !slotmachine.FeatureEnabled("presence") {
		t.Fatal("Presence 확대 후보의 presence가 활성화되지 않았다")
	}

	// Presence는 익명 세션 heartbeat만 쓴다. 기존 기능 경계를 함께 바꾸지 않는다.
	if !slotmachine.FeatureEnabled("events") {
		t.Fatal("events 기능이 꺼졌다")
	}
	if !slotmachine.FeatureEnabled("ads") {
		t.Fatal("ads 기능이 꺼졌다")
	}
	if slotmachine.FeatureEnabled("iap") {
		t.Fatal("iap 기능이 의도치 않게 켜졌다")
	}
	if slotmachine.FeatureEnabled("config") {
		t.Fatal("config 기능이 의도치 않게 켜졌다")
	}
}

// 광고 단위는 앱과 레지스트리 양쪽에 같은 값이 있어야 한다. 한쪽만 바뀌면 광고는
// 정상 재생되고 SSV 만 CodeAdUnitMismatch 로 거부되므로, 사용자는 광고를 끝까지 보고도
// 보상을 받지 못한다. 실패가 claim 이 아니라 확정 단계에서만 나므로 실기기에서도
// 늦게 드러난다. 그래서 여기서 고정 비교한다.
//
// 정본은 seorilabs/.github#167 의 중앙 발급 원장이고, 앱 쪽 주입값
// (seorilabs/slotmachine-game 의 godot/admob.config.json) 도 같은 원장을 본다.
// 2026-09-18 에 레거시 publisher pub-2444587584524186 에서 유지 publisher
// pub-9932778305312246 으로 옮겼다. 레거시 unit 은 더 이상 유효한 비교 대상이 아니다.
func TestSlotmachineGameAdsRegistryContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var slotmachine *App
	for i := range apps {
		if apps[i].AppID == "slotmachine-game" {
			slotmachine = &apps[i]
			break
		}
	}
	if slotmachine == nil {
		t.Fatal("slotmachine-game registry가 없다")
	}

	// 네 지면 모두 rewarded 다. 지면이 빠지면 해당 보상 흐름만 조용히 죽는다.
	// 은퇴 unit은 이미 공개된 v1.2.0 빌드에 박힌 값이다. 이 목록이 비면 그 빌드를
	// 쓰는 사용자는 광고를 끝까지 보고도 ad_unit_mismatch 로 보상을 받지 못한다.
	// 구버전 소진을 확인한 뒤 registry/apps/README.md 4단계로 지운다.
	wantUnits := map[string]struct {
		android        string
		ios            string
		retiredAndroid string
		retiredIOS     string
	}{
		"ad_win": {
			android:        "ca-app-pub-9932778305312246/6892047646",
			ios:            "ca-app-pub-9932778305312246/7386037517",
			retiredAndroid: "ca-app-pub-2444587584524186/2032813100",
			retiredIOS:     "ca-app-pub-2444587584524186/9581678799",
		},
		"credit_refill": {
			android:        "ca-app-pub-9932778305312246/4265884309",
			ios:            "ca-app-pub-9932778305312246/8013557626",
			retiredAndroid: "ca-app-pub-2444587584524186/6784125068",
			retiredIOS:     "ca-app-pub-2444587584524186/2419825919",
		},
		"daily_bonus_double": {
			android:        "ca-app-pub-9932778305312246/6724526684",
			ios:            "ca-app-pub-9932778305312246/8967546644",
			retiredAndroid: "ca-app-pub-2444587584524186/3207842138",
			retiredIOS:     "ca-app-pub-2444587584524186/7887775015",
		},
		"piggy_bank": {
			android:        "ca-app-pub-9932778305312246/6452229793",
			ios:            "ca-app-pub-9932778305312246/3665092388",
			retiredAndroid: "ca-app-pub-2444587584524186/1426308448",
			retiredIOS:     "ca-app-pub-2444587584524186/1106744246",
		},
	}

	for id, want := range wantUnits {
		placement, ok := slotmachine.AdsPlacement(id)
		if !ok {
			t.Fatalf("%s 지면이 없다", id)
		}
		if placement.Format != "rewarded" {
			t.Fatalf("%s format=%q, want rewarded", id, placement.Format)
		}

		provider, ok := placement.Providers["admob"]
		if !ok {
			t.Fatalf("%s AdMob provider 설정이 없다", id)
		}
		if provider.AndroidAdUnitID != want.android {
			t.Fatalf("%s Android unit=%q, want %q", id, provider.AndroidAdUnitID, want.android)
		}
		if provider.IOSAdUnitID != want.ios {
			t.Fatalf("%s iOS unit=%q, want %q", id, provider.IOSAdUnitID, want.ios)
		}

		// ConfirmAdMob 은 이 순서대로 수용한다. 현재 unit 이 첫 항목이어야 하고
		// 은퇴 unit 이 빠지면 구버전 보상이 끊긴다.
		wantAndroidAccepted := []string{want.android, want.retiredAndroid}
		if got := provider.AcceptedAdMobUnits("android"); !slices.Equal(got, wantAndroidAccepted) {
			t.Fatalf("%s Android 수용 unit=%v, want %v", id, got, wantAndroidAccepted)
		}
		wantIOSAccepted := []string{want.ios, want.retiredIOS}
		if got := provider.AcceptedAdMobUnits("ios"); !slices.Equal(got, wantIOSAccepted) {
			t.Fatalf("%s iOS 수용 unit=%v, want %v", id, got, wantIOSAccepted)
		}

		// ConfirmAdMob 은 SSV 의 reward_item·reward_amount 를 이 값과 대조한다. AdMob
		// 콘솔의 단위별 보상 설정이 credit/1 이 아니면 CodeAdRewardInvalid 로 거부된다.
		// 게시자를 옮기면서 새로 만든 단위에도 같은 보상 설정이 필요하다.
		if provider.RewardItem != "credit" || provider.RewardAmount != 1 {
			t.Fatalf("%s AdMob 보상 계약이 어긋났다: item=%q amount=%d",
				id, provider.RewardItem, provider.RewardAmount)
		}
	}

	// 레거시 publisher 는 은퇴 목록에만 남아야 한다. 현재 unit 으로 되돌아오면
	// 새 publisher 로 옮긴 수익 귀속이 조용히 깨진다.
	for _, placement := range slotmachine.Ads.Placements {
		provider, ok := placement.Providers["admob"]
		if !ok {
			continue
		}
		for _, unit := range []string{provider.AndroidAdUnitID, provider.IOSAdUnitID} {
			if strings.Contains(unit, "ca-app-pub-2444587584524186") {
				t.Fatalf("%s 현재 unit 이 레거시 publisher 다: %s", placement.ID, unit)
			}
		}
	}
}
