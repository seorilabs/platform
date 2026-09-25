package registry

import (
	"context"
	"os"
	"testing"
)

func TestBloomhandGA4RelayContract(t *testing.T) {
	apps, err := NewFSSource(os.DirFS("../../../registry"), "apps").LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.AppID != "bloomhand" {
			continue
		}
		if !app.FeatureEnabled("events") || app.GA4.PropertyID != "555645988" || app.GA4.MeasurementID != "G-23J753X3GD" {
			t.Fatalf("Bloomhand GA4 relay 설정이 다르다: %#v", app.GA4)
		}
		for _, event := range []string{
			"tutorial_begin", "tutorial_complete", "run_start", "run_end", "daily_submit",
			"ad_requested", "ad_impression", "ad_failed", "rewarded_earned", "purchase", "purchase_restore",
			"save_recover", "replay_mismatch", "hand_play", "prune", "tool_use", "critter_buy",
			"tool_buy", "critter_sell", "slot_reorder", "reroll", "harvest_pick", "market_open",
			"day_start", "day_end",
		} {
			if !app.EventAllowed(event) {
				t.Fatalf("Bloomhand 제품 이벤트 %q가 허용되지 않았다", event)
			}
		}
		if app.EventAllowed("email") {
			t.Fatal("등록하지 않은 이벤트가 허용됐다")
		}
		return
	}
	t.Fatal("Bloomhand registry가 없다")
}
