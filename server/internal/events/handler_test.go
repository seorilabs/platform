package events

import (
	"context"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/registry"
)

func TestBuildRowsCopiesContextAndDropsUnknownEvent(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	h := &Handler{collector: &Collector{now: func() time.Time { return now }}}
	app := registry.App{
		AppID:                  "happy-farm",
		PlatformEventAllowlist: []string{"game_start"},
	}
	req := ingestRequest{
		Events: []clientEvent{
			{EventID: "event-1", Name: "game_start", SessionID: "session-1"},
			{EventID: "event-2", Name: "crop_harvested", SessionID: "session-1"},
		},
	}
	req.Context.Platform = "ait"
	req.Context.AppVersion = "1.2.3"
	req.Context.Locale = "ko-KR"
	req.Context.SDKVersion = "0.1.0"

	rows, dropped := h.buildRows(context.Background(), app, req, "")

	if len(rows) != 1 || dropped != 1 {
		t.Fatalf("rows=%d dropped=%d, want rows=1 dropped=1", len(rows), dropped)
	}
	row := rows[0]
	if row.AppID != "happy-farm" || row.Platform != "ait" || row.AppVersion != "1.2.3" ||
		row.Locale != "ko-KR" || row.SDKVersion != "0.1.0" || row.SessionID != "session-1" {
		t.Fatalf("context가 행에 반영되지 않았다: %#v", row)
	}
	if row.PlatformUserID != "" || row.GA4ClientID != "" {
		t.Fatalf("익명 이벤트에 식별값이 생겼다: %#v", row)
	}
}
