package events

import (
	"context"
	"encoding/json"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

type deletionAppSource struct{ app registry.App }

func (s deletionAppSource) LoadApps(context.Context) ([]registry.App, error) {
	return []registry.App{s.app}, nil
}

type deletionSessions struct {
	err  error
	sess identity.Session
}

func (s deletionSessions) Authenticate(*http.Request) (identity.Session, error) { return s.sess, s.err }
func TestDeletionAppNeverDowngradesRejectedIdentityToAnonymous(t *testing.T) {
	raw, err := os.ReadFile("../../../registry/apps/lord-ledger.json")
	if err != nil {
		t.Fatal(err)
	}
	var app registry.App
	if err = json.Unmarshal(raw, &app); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		sessions SessionResolver
		want     platformerr.Code
	}{
		{"deleted token", deletionSessions{err: platformerr.New(platformerr.CodeAuthForbidden, "deleted")}, platformerr.CodeAuthForbidden},
		{"missing token", deletionSessions{err: platformerr.New(platformerr.CodeAuthRequired, "missing")}, platformerr.CodeAuthRequired},
		{"missing resolver", nil, platformerr.CodeAuthRequired},
		{"other app", deletionSessions{sess: identity.Session{AppID: "other", PlatformUserID: "pu_test"}}, platformerr.CodeAuthForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// collector가 없어도 거부가 끝나야 한다. 익명 행을 만들거나 Insert를
			// 호출하는 회귀가 생기면 nil collector 접근으로도 테스트가 실패한다.
			h := NewHandler(nil, registry.New(deletionAppSource{app}), tc.sessions)
			req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(`{"events":[{"eventId":"e","name":"turn_end","sessionId":"old-session"}],"context":{"ga4ClientId":"old-client"}}`))
			req.Header.Set(identity.AppHeader, app.AppID)
			req.Header.Set("Content-Type", "application/json")
			if err := h.ingest(httptest.NewRecorder(), req); platformerr.CodeOf(err) != tc.want {
				t.Fatalf("got %v want %s", err, tc.want)
			}
		})
	}
}
