package registry

import (
	"context"
	"os"
	"testing"
)

// crossword-puzzle은 인증만 platform으로 넘기고 이벤트·결제·광고는 앱이 계속 소유한다.
// 이 테스트는 그 경계가 조용히 넓어지지 않게 고정한다.
func TestCrosswordPuzzleRegistryAuthBridgeContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var app *App
	for i := range apps {
		if apps[i].AppID == "crossword-puzzle" {
			app = &apps[i]
			break
		}
	}
	if app == nil {
		t.Fatal("crossword-puzzle registry가 없다")
	}

	// 스키마 규칙(중복 origin, bridge SA 형식 등)은 Validate가 소유한다. 계약
	// 테스트가 그 경로를 함께 타야 잘못된 등록이 여기서 걸린다.
	if err := app.Validate(); err != nil {
		t.Fatalf("레지스트리 항목이 유효하지 않다: %v", err)
	}

	if !app.FeatureEnabled("firebase_custom_token_bridge") {
		t.Fatal("인증 브리지가 비활성이다")
	}
	// 힌트 잔량이 기기 로컬 저장소에 있어 서버가 지킬 재화가 없다. 보상형 광고가
	// 있지만 ads(SSV)는 켜지 않는다. 힌트를 서버로 옮길 때 함께 검토한다.
	for _, feature := range []string{"config", "events", "iap", "ads"} {
		if app.FeatureEnabled(feature) {
			t.Fatalf("%s 기능이 활성화됐다: %#v", feature, app.Features)
		}
	}
	if len(app.PlatformEventAllowlist) != 0 {
		t.Fatalf("events가 비활성인데 allowlist가 있다: %#v", app.PlatformEventAllowlist)
	}

	// AIT WebView가 공개 bootstrap 경로를 호출하므로 서비스와 콘솔 QR origin이 모두 필요하다.
	wantOrigins := map[string]bool{
		"https://crossword-puzzle-game.apps.tossmini.com":         false,
		"https://crossword-puzzle-game.private-apps.tossmini.com": false,
	}
	for _, origin := range app.CORSOrigins {
		seen, ok := wantOrigins[origin]
		if !ok {
			t.Fatalf("등록되지 않은 origin: %q", origin)
		}
		if seen {
			t.Fatalf("origin 중복: %q", origin)
		}
		wantOrigins[origin] = true
	}
	for origin, seen := range wantOrigins {
		if !seen {
			t.Fatalf("필요한 origin이 없다: %q", origin)
		}
	}

	// GA4 접두사는 기존 시계열과 맞춰 game_ 을 유지한다(spec/events.md).
	if app.GA4.EventPrefix != "game_" {
		t.Fatalf("GA4 event prefix = %q, want game_", app.GA4.EventPrefix)
	}
	if got := app.StripEventPrefix("game_start"); got != "start" {
		t.Fatalf("stored event name = %q, want start", got)
	}

	// App Check는 3마켓 클라이언트 실기기 검증 뒤 켠다(ADR 0013 단계 적용).
	if app.RequireAppCheck {
		t.Fatal("클라이언트 검증 전에 App Check가 강제됐다")
	}
}
