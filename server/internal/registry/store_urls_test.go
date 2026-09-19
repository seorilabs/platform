package registry

import (
	"context"
	"os"
	"testing"
)

// 스토어 주소는 강제 업데이트 화면이 유저를 보내는 유일한 목적지다.
//
// 값을 여기에 고정하는 이유는 ads placement를 고정하는 것과 같다. 레지스트리만
// 조용히 움직이면 이미 마켓에 나간 빌드가 잘못된 곳으로 유저를 보낸다.
//
// **여기 있는 주소는 실제로 열리는 것을 확인한 것만 넣는다.** 아직 공개되지
// 않은 앱의 주소를 미리 넣으면 차단 화면의 버튼이 404로 간다.
func TestStoreURLsArePinned(t *testing.T) {
	reg := New(NewFSSource(os.DirFS("../../../registry"), "apps"))

	tests := map[string]struct {
		play     string
		appStore string
	}{
		"happy-farm": {
			play: "https://play.google.com/store/apps/details?id=com.seorilabs.happyfarm",
		},
		"lizard-tycoon": {
			play:     "https://play.google.com/store/apps/details?id=com.seorilabs.lizardtycoon",
			appStore: "https://apps.apple.com/app/id6786516830",
		},
		"babycare": {
			play:     "https://play.google.com/store/apps/details?id=com.seorilabs.babycare",
			appStore: "https://apps.apple.com/app/id6792193162",
		},
		"crossword-puzzle": {
			play: "https://play.google.com/store/apps/details?id=com.seorilabs.crosswordpuzzle",
		},
	}

	for appID, want := range tests {
		t.Run(appID, func(t *testing.T) {
			app, err := reg.Get(context.Background(), appID)
			if err != nil {
				t.Fatalf("레지스트리 조회 실패: %v", err)
			}

			if got := app.UpdateURL("android"); got != want.play {
				t.Errorf("android = %q, want %q", got, want.play)
			}
			if got := app.UpdateURL("ios"); got != want.appStore {
				t.Errorf("ios = %q, want %q", got, want.appStore)
			}

			// 플래그가 꺼지면 정책 판정이 통째로 멈춘다. 주소만 있고 플래그가
			// 없으면 아무 일도 일어나지 않는다.
			if !app.FeatureEnabled("config") {
				t.Error("스토어 주소가 있는데 features.config가 꺼져 있다")
			}
		})
	}
}

// 아직 공개되지 않은 앱에 주소를 넣지 않는다.
//
// 2026-09-06 확인: ungeul·cycle-pair·jomul은 Google Play에서 404이고
// cycle-pair는 App Store에서도 404다. 공개된 뒤에 넣는다.
func TestUnpublishedAppsHaveNoStoreURL(t *testing.T) {
	reg := New(NewFSSource(os.DirFS("../../../registry"), "apps"))

	for _, appID := range []string{"ungeul", "cycle-pair", "jomul"} {
		t.Run(appID, func(t *testing.T) {
			app, err := reg.Get(context.Background(), appID)
			if err != nil {
				t.Fatalf("레지스트리 조회 실패: %v", err)
			}
			if app.Store.GooglePlayURL != "" || app.Store.AppStoreURL != "" {
				t.Errorf("공개 확인 없이 스토어 주소가 들어갔다: %+v", app.Store)
			}
		})
	}
}
