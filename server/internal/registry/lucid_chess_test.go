package registry

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestLucidChessRegistryAuthAndEventContract(t *testing.T) {
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

	if err := lucidChess.Validate(); err != nil {
		t.Fatalf("레지스트리 항목이 유효하지 않다: %v", err)
	}
	if lucidChess.FirebaseProjectID != "lucid-chess-dbb9d" || lucidChess.RequireAppCheck {
		t.Fatalf("Firebase/App Check 계약이 다르다: %#v", lucidChess)
	}
	if lucidChess.FirebaseCustomTokenServiceAccount !=
		"platform-auth@lucid-chess-dbb9d.iam.gserviceaccount.com" {
		t.Fatalf("custom token service account가 다르다: %q",
			lucidChess.FirebaseCustomTokenServiceAccount)
	}
	if !lucidChess.FeatureEnabled("events") || lucidChess.FeatureEnabled("config") ||
		lucidChess.FeatureEnabled("iap") || lucidChess.FeatureEnabled("ads") ||
		!lucidChess.FeatureEnabled("firebase_custom_token_bridge") {
		t.Fatalf("인증과 Events 기능 경계가 다르다: %#v", lucidChess.Features)
	}

	wantOrigins := []string{
		"https://lucid-chess.apps.tossmini.com",
		"https://lucid-chess.private-apps.tossmini.com",
	}
	if !reflect.DeepEqual(lucidChess.CORSOrigins, wantOrigins) {
		t.Fatalf("AIT origin이 다르다\n got: %#v\nwant: %#v", lucidChess.CORSOrigins, wantOrigins)
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
