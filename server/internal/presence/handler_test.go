package presence

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/registry"
)

type fakeRegistry struct{ app registry.App }

func (f fakeRegistry) GetUsable(context.Context, string) (registry.App, error) { return f.app, nil }

type fakeIssuer struct{}

func (fakeIssuer) Issue(string, string) (string, time.Time, error) {
	return "presence-token", time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC), nil
}

func TestHandlerReturnsDisabledWithoutFeature(t *testing.T) {
	h := NewHandler(fakeRegistry{app: registry.App{Features: map[string]bool{}}}, fakeIssuer{}, "https://edge.vzyx.xyz")
	req := httptest.NewRequest(http.MethodPost, "/v1/presence/token",
		strings.NewReader(`{"sessionId":"session_0123456789abcdef","platform":"web"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Seori-App", "happy-farm")
	rec := httptest.NewRecorder()
	if err := h.issue(rec, req); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Result tokenResponse `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Result.Enabled || body.Result.HeartbeatIntervalSeconds != 60 {
		t.Fatalf("unexpected result: %+v", body.Result)
	}
}

func TestHandlerIssuesEnabledToken(t *testing.T) {
	h := NewHandler(fakeRegistry{app: registry.App{Features: map[string]bool{"presence": true}}}, fakeIssuer{}, "https://edge.vzyx.xyz/")
	req := httptest.NewRequest(http.MethodPost, "/v1/presence/token",
		strings.NewReader(`{"sessionId":"session_0123456789abcdef","platform":"ait","appVersion":"1.2.0"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Seori-App", "happy-farm")
	rec := httptest.NewRecorder()
	if err := h.issue(rec, req); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Result tokenResponse `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Result.Enabled || body.Result.Token != "presence-token" || body.Result.EdgeURL != "https://edge.vzyx.xyz" {
		t.Fatalf("unexpected result: %+v", body.Result)
	}
}
