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

func TestBuildRowsUsesTransportSessionParameter(t *testing.T) {
	now := time.Date(2026, 9, 14, 6, 0, 0, 0, time.UTC)
	h := &Handler{collector: &Collector{now: func() time.Time { return now }}}
	app := registry.App{AppID: "jomul", PlatformEventAllowlist: []string{"session_start"}, GA4: registry.GA4Config{EventPrefix: "jomul_"}}
	req := ingestRequest{Events: []clientEvent{{
		EventID: "event-1", Name: "jomul_session_start",
		Params: map[string]any{"session_id": "1726293600", "engagement_time_msec": float64(1)},
	}}}
	rows, dropped := h.buildRows(context.Background(), app, req, "")
	if dropped != 0 || len(rows) != 1 || rows[0].SessionID != "1726293600" {
		t.Fatalf("전송 전용 session_id를 행에 보존하지 못했다: rows=%#v dropped=%d", rows, dropped)
	}
}

type failingGA4Sender struct {
	calls int
	relay ga4RelayContext
}

func (s *failingGA4Sender) Send(
	_ context.Context,
	_ registry.App,
	_ []*Row,
	relay ga4RelayContext,
) error {
	s.calls++
	s.relay = relay
	return platformerr.New(platformerr.CodeConfigUnavailable, "upstream unavailable")
}

func TestForwardGA4IsBestEffort(t *testing.T) {
	sender := &failingGA4Sender{}
	h := &Handler{ga4: sender}
	h.forwardGA4(
		t.Context(),
		registry.App{AppID: "jomul"},
		[]*Row{{EventID: "event-1"}},
		ga4RelayContext{IPOverride: "8.8.8.8"},
	)
	if sender.calls != 1 {
		t.Fatalf("GA4 sender 호출 횟수 = %d", sender.calls)
	}
	if sender.relay.IPOverride != "8.8.8.8" {
		t.Fatalf("GA4 relay context가 유실됐다: %#v", sender.relay)
	}
}

func TestGA4RelayContextRequiresConsentAndTrustedIngress(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
	// 앞의 1.1.1.1은 클라이언트가 조작한 값이다. 신뢰 ingress가 오른쪽에
	// 추가한 client, proxy 두 값만 기준으로 8.8.8.8을 선택한다.
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 8.8.8.8, 34.1.2.3")
	h := (&Handler{}).WithTrustedIngressProxyHops(1)

	if relay := h.ga4RelayContext(req, false); relay.IPOverride != "" {
		t.Fatalf("동의 없는 요청 주소가 전달됐다: %#v", relay)
	}
	if relay := (&Handler{}).ga4RelayContext(req, true); relay.IPOverride != "" {
		t.Fatalf("신뢰 ingress 설정 없이 요청 주소가 전달됐다: %#v", relay)
	}
	if relay := h.ga4RelayContext(req, true); relay.IPOverride != "8.8.8.8" {
		t.Fatalf("검증된 원 요청 주소 = %q", relay.IPOverride)
	}
	stored, _, err := (&Row{EventID: "event-1"}).Save()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := stored["ip_override"]; exists {
		t.Fatal("원 요청 주소가 Platform 이벤트 행에 저장됐다")
	}
}

func TestTrustedClientIPRejectsUnverifiableHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		hops   int
	}{
		{name: "disabled", header: "8.8.8.8, 34.1.2.3", hops: 0},
		{name: "missing trusted hop", header: "8.8.8.8", hops: 1},
		{name: "invalid trusted hop", header: "8.8.8.8, forged", hops: 1},
		{name: "private client", header: "10.0.0.8, 34.1.2.3", hops: 1},
		{name: "forged only", header: "1.1.1.1", hops: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
			req.Header.Set("X-Forwarded-For", tc.header)
			if got := trustedClientIP(req, tc.hops); got != "" {
				t.Fatalf("검증할 수 없는 주소를 신뢰했다: %q", got)
			}
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
	req.Header.Add("X-Forwarded-For", "1.1.1.1, 34.1.2.3")
	req.Header.Add("X-Forwarded-For", "8.8.8.8, 34.1.2.3")
	if got := trustedClientIP(req, 1); got != "" {
		t.Fatalf("중복 X-Forwarded-For를 신뢰했다: %q", got)
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
