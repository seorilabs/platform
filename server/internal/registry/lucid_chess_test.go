package registry

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestLucidChessRegistryEventContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var lucidChess *App
	for i := range apps {
		if apps[i].AppID == "lucid-chess" {
			lucidChess = &apps[i]
			break
		}
	}
	if lucidChess == nil {
		t.Fatal("lucid-chess registry가 없다")
	}

	if lucidChess.FirebaseProjectID != "lucid-chess-dbb9d" || lucidChess.RequireAppCheck {
		t.Fatalf("Firebase/App Check 계약이 다르다: %#v", lucidChess)
	}
	if !lucidChess.FeatureEnabled("events") || lucidChess.FeatureEnabled("config") ||
		lucidChess.FeatureEnabled("iap") || lucidChess.FeatureEnabled("ads") ||
		lucidChess.FeatureEnabled("firebase_custom_token_bridge") {
		t.Fatalf("Events 외 기능이 활성화됐다: %#v", lucidChess.Features)
	}

	wantAllowlist := []string{
		"app_open",
		"game_start",
		"game_end",
		"game_abandon",
		"daily_puzzle_complete",
		"ad_interstitial_shown",
		"ad_interstitial_skip",
	}
	if !reflect.DeepEqual(lucidChess.PlatformEventAllowlist, wantAllowlist) {
		t.Fatalf("allowlist가 다르다\n got: %#v\nwant: %#v", lucidChess.PlatformEventAllowlist, wantAllowlist)
	}
	if lucidChess.EventAllowed("hint_used") || lucidChess.EventAllowed("review_analyzed") {
		t.Fatal("1차 범위 밖 고빈도 또는 상세 이벤트가 허용됐다")
	}
}
