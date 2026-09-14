package events

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

func TestMeasurementProtocolSendsAcceptedRowsWithoutAdConsent(t *testing.T) {
	var received ga4Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("measurement_id") != "G-TEST1234" || r.URL.Query().Get("api_secret") != "test-secret" {
			t.Errorf("GA4 query가 다르다: %s", r.URL.RawQuery)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("GA4 payload 해석 실패: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sender := NewMeasurementProtocol(map[string]string{"jomul": "test-secret"}, server.Client())
	sender.endpoint = server.URL
	eventTime := time.Date(2026, 9, 14, 6, 0, 0, 123000000, time.UTC)
	rows := []*Row{{
		EventID: "event-1", EventTS: eventTime, AppID: "jomul", GA4ClientID: "client-1",
		SessionID: "1726293600", EventName: "session_start", Platform: "ait",
		AppVersion: "1.0.14", Locale: "ko", Params: map[string]any{"discovered_count": int64(5)},
	}}
	app := registry.App{AppID: "jomul", GA4: registry.GA4Config{EventPrefix: "jomul_", MeasurementID: "G-TEST1234"}}
	if err := sender.Send(t.Context(), app, rows); err != nil {
		t.Fatalf("GA4 전송 실패: %v", err)
	}
	if received.ClientID != "client-1" || received.TimestampMicros != eventTime.UnixMicro() {
		t.Fatalf("GA4 request context가 다르다: %#v", received)
	}
	if received.Consent.AdUserData != "DENIED" || received.Consent.AdPersonalization != "DENIED" {
		t.Fatalf("광고 consent가 차단되지 않았다: %#v", received.Consent)
	}
	if len(received.Events) != 1 || received.Events[0].Name != "jomul_session_start" {
		t.Fatalf("GA4 이벤트가 다르다: %#v", received.Events)
	}
	params := received.Events[0].Params
	if params["platform"] != "ait" || params["app_version"] != "1.0.14" || params["locale"] != "ko" ||
		params["seori_event_id"] != "event-1" || params["session_id"] != float64(1726293600) ||
		params["engagement_time_msec"] != float64(1) {
		t.Fatalf("GA4 파라미터가 다르다: %#v", params)
	}
}

func TestMeasurementProtocolSkipsLegacyClientAndRetriesConfigurationFailures(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	app := registry.App{AppID: "jomul", GA4: registry.GA4Config{EventPrefix: "jomul_", MeasurementID: "G-TEST1234"}}
	row := &Row{EventID: "event-1", EventTS: time.Now(), EventName: "session_start", GA4ClientID: "client-1"}

	missing := NewMeasurementProtocol(nil, server.Client())
	missing.endpoint = server.URL
	if err := missing.Send(t.Context(), app, []*Row{row}); platformerr.CodeOf(err) != platformerr.CodeConfigUnavailable {
		t.Fatalf("secret 누락 오류 = %v", err)
	} else if strings.Contains(err.Error(), "test-secret") {
		t.Fatal("오류에 GA4 secret이 노출됐다")
	}

	legacy := NewMeasurementProtocol(map[string]string{"jomul": "test-secret"}, server.Client())
	legacy.endpoint = server.URL
	legacyRow := *row
	legacyRow.GA4ClientID = ""
	if err := legacy.Send(t.Context(), app, []*Row{&legacyRow}); err != nil || requests != 0 {
		t.Fatalf("구버전 client는 GA4 전송 없이 Platform 적재를 유지해야 한다: err=%v requests=%d", err, requests)
	}

	if err := legacy.Send(t.Context(), app, []*Row{row}); platformerr.CodeOf(err) != platformerr.CodeConfigUnavailable || requests != 1 {
		t.Fatalf("GA4 upstream 실패가 재시도 오류로 보존되지 않았다: err=%v requests=%d", err, requests)
	}
}
