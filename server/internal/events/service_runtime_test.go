package events

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/seorilabs/platform/server/internal/registry"
	"google.golang.org/api/option"
)

type runtimeEventApps struct{ app registry.App }

func (s runtimeEventApps) LoadApps(context.Context) ([]registry.App, error) {
	return []registry.App{s.app}, nil
}

// BQ/GA4 외부 HTTP만 격리한다. 수집·정규화·SDK 적재·전달은 제품 구현이다.
func TestServiceRuntimeEventIngestRelayDebugAndRecovery(t *testing.T) {
	var inserts, relays atomic.Int32
	var fail atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"code":400,"message":"fixture rejection"}}`)
			return
		}
		var body struct {
			Rows []struct {
				JSON map[string]any `json:"json"`
			} `json:"rows"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil || len(body.Rows) != 1 || body.Rows[0].JSON["event_name"] != "session_start" {
			w.WriteHeader(400)
			t.Error("BQ must receive one normalized allowlisted row")
			return
		}
		inserts.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(sink.Close)
	ga4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body ga4Request
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil || len(body.Events) != 1 || body.Events[0].Name != "session_start" {
			w.WriteHeader(400)
			t.Error("GA4 must receive allowlisted event")
			return
		}
		relays.Add(1)
		w.WriteHeader(204)
	}))
	t.Cleanup(ga4.Close)
	client, e := bigquery.NewClient(t.Context(), "demo-platform-service-e2e", option.WithEndpoint(sink.URL), option.WithoutAuthentication(), option.WithHTTPClient(sink.Client()))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = client.Close() })
	collector := &Collector{client: client, dataset: "isolated_runtime", now: time.Now}
	app := registry.App{AppID: "runtime-events", DisplayName: "Runtime events", FirebaseProjectID: "demo-platform-service-e2e", Status: registry.StatusActive, Features: map[string]bool{"events": true}, GA4: registry.GA4Config{MeasurementID: "G-RUNTIME123"}, PlatformEventAllowlist: []string{"session_start"}}
	reg := registry.New(runtimeEventApps{app})
	mp := NewMeasurementProtocol(map[string]string{app.AppID: "fixture-only"}, ga4.Client())
	mp.endpoint = ga4.URL
	var server *httptest.Server
	start := func() {
		mux := http.NewServeMux()
		NewHandler(collector, reg, nil).WithGA4(mp).Register(mux)
		server = httptest.NewServer(mux)
		server.Client().Timeout = 15 * time.Second
	}
	start()
	t.Cleanup(func() { server.Close() })
	body := `{"events":[{"eventId":"runtime-event-1","name":"session_start","sessionId":"runtime-session"},{"eventId":"runtime-event-2","name":"not_allowlisted"}],"context":{"platform":"android","ga4ClientId":"123.456","analyticsConsent":true}}`
	request := func(debug bool, want int) {
		t.Helper()
		r, e := http.NewRequest("POST", server.URL+"/v1/events", strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Seori-App", app.AppID)
		if debug {
			r.Header.Set("X-Seori-Build", "debug")
		}
		res, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != want {
			t.Fatalf("ingest status=%d want=%d", res.StatusCode, want)
		}
		var env struct {
			Result ingestResponse `json:"result"`
		}
		if e = json.NewDecoder(res.Body).Decode(&env); e != nil {
			t.Fatal(e)
		}
		if want == 200 {
			accepted, dropped := 1, 1
			if debug {
				accepted, dropped = 0, 2
			}
			if env.Result.Accepted != accepted || env.Result.Dropped != dropped {
				t.Fatalf("ingest counts=%+v", env.Result)
			}
		}
	}
	request(false, 200)
	request(true, 200)
	if inserts.Load() != 1 || relays.Load() != 1 {
		t.Fatal("debug must not write either sink")
	}
	fail.Store(true)
	request(false, 500)
	server.Close()
	fail.Store(false)
	start()
	request(false, 200)
	if inserts.Load() != 2 || relays.Load() != 2 {
		t.Fatal("restart/recovery must deliver both sinks")
	}
	t.Log("real HTTP ingest → normalization/partial acceptance → production BigQuery SDK/GA4 HTTP sinks → debug exclusion → sink failure/restart/recovery; cloud sinks are local fixtures")
}
