package registry

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestLizardTycoonRegistryEventContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var lizardTycoon *App
	for i := range apps {
		if apps[i].AppID == "lizard-tycoon" {
			lizardTycoon = &apps[i]
			break
		}
	}
	if lizardTycoon == nil {
		t.Fatal("lizard-tycoon registry가 없다")
	}

	wantAllowlist := []string{
		"lizard_adopted",
		"tutorial_begin",
		"tutorial_step",
		"tutorial_complete",
		"daily_play_completed",
		"purchase",
		"premium_purchase_failed",
		"script_error",
	}
	if !reflect.DeepEqual(lizardTycoon.PlatformEventAllowlist, wantAllowlist) {
		t.Fatalf("allowlist가 다르다\n got: %#v\nwant: %#v", lizardTycoon.PlatformEventAllowlist, wantAllowlist)
	}
	if lizardTycoon.EventAllowed("screen_view") || lizardTycoon.EventAllowed("first_open") ||
		lizardTycoon.EventAllowed("session_start") {
		t.Fatal("GA4 SDK가 자동 수집하거나 화면 전용인 이벤트가 Platform allowlist에 포함됐다")
	}
}
