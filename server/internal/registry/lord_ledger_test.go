package registry

import (
	"context"
	"os"
	"slices"
	"testing"
)

// 새 후보는 새 publisher의 unit을, 공개된 1.3.2는 옛 unit을 보낸다.
// 두 버전 모두 군자금 300 계약이므로 업데이트 도중에도 SSV 대조가 끊기면 안 된다.
func TestLordLedgerRewardAndPublisherTransition(t *testing.T) {
	apps, err := NewFSSource(os.DirFS("../../../registry"), "apps").LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.AppID != "lord-ledger" {
			continue
		}
		p, ok := app.AdsPlacement("city_supply")
		if !ok || p.Reward == nil || p.Reward.Key != "gold" || p.Reward.MinAmount != 300 || p.Reward.MaxAmount != 300 {
			t.Fatalf("출시 앱의 군자금 300 계약과 다르다: %+v", p)
		}
		cfg := p.Providers["admob"]
		if cfg.RewardItem != "gold" || cfg.RewardAmount != 300 {
			t.Fatalf("SSV 보상과 다르다: %+v", cfg)
		}
		for _, row := range []struct{ platform, current, previous string }{
			{"android", "ca-app-pub-9932778305312246/7930533236", "ca-app-pub-2444587584524186/5352255759"},
			{"ios", "ca-app-pub-9932778305312246/3795847101", "ca-app-pub-2444587584524186/5673383051"},
		} {
			units := cfg.AcceptedAdMobUnits(row.platform)
			if len(units) == 0 || units[0] != row.current || !slices.Contains(units, row.previous) {
				t.Fatalf("%s 신규·출시 버전의 unit을 함께 수용해야 한다: %v", row.platform, units)
			}
		}
		return
	}
	t.Fatal("lord-ledger registry가 없다")
}
