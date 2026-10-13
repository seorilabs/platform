//go:build integration

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/seorilabs/platform/server/internal/presence"
	"github.com/seorilabs/platform/server/internal/presenceedge"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type runtimePresenceProjection struct {
	mu      sync.Mutex
	writes  int
	fail    bool
	session presenceedge.Session
}

func (r *runtimePresenceProjection) Upsert(_ context.Context, s presenceedge.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return fmt.Errorf("fixture DB unavailable")
	}
	r.writes++
	r.session = s
	return nil
}
func (r *runtimePresenceProjection) Ping(context.Context) error { return nil }
func TestServiceRuntimePresenceSignedTokenAndEdgeRetry(t *testing.T) {
	f := newRuntimeFixture(t)
	f.app.Features["presence"] = true
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := presence.NewIssuer(priv, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := presence.NewVerifier(pub)
	if err != nil {
		t.Fatal(err)
	}
	projection := &runtimePresenceProjection{}
	var clock atomic.Int64
	clock.Store(time.Now().UTC().UnixNano())
	edgeMux := http.NewServeMux()
	presenceedge.NewHandler(verifier, projection).WithClock(func() time.Time { return time.Unix(0, clock.Load()).UTC() }).Register(edgeMux)
	edge := httptest.NewServer(edgeMux)
	defer edge.Close()
	f.register = func(mux *http.ServeMux) { presence.NewHandler(f.registry, issuer, edge.URL).Register(mux) }
	f.saveApp()
	issue := func(debug bool) map[string]any {
		body := strings.NewReader(`{"sessionId":"runtime_session_0001","platform":"android"}`)
		req, _ := http.NewRequest("POST", f.server.URL+"/v1/presence/token", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Seori-App", f.app.AppID)
		if debug {
			req.Header.Set("X-Seori-Build", "debug")
		}
		res, e := f.server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = res.Body.Close() }()
		var envelope struct {
			Result map[string]any `json:"result"`
		}
		if e = json.NewDecoder(res.Body).Decode(&envelope); e != nil || res.StatusCode != 200 {
			t.Fatal("token issue", res.StatusCode, e)
		}
		return envelope.Result
	}
	if issue(true)["enabled"] != false {
		t.Fatal("debug presence must remain disabled")
	}
	issued := issue(false)
	token := issued["token"].(string)
	heartbeat := func(token string, status int) {
		req, _ := http.NewRequest("POST", edge.URL+"/v1/presence/heartbeat", strings.NewReader(`{"version":1,"sequence":1,"platform":"android"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		res, e := edge.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_ = res.Body.Close()
		if res.StatusCode != status {
			t.Fatalf("heartbeat %d want %d", res.StatusCode, status)
		}
	}
	heartbeat("invalid", 401)
	heartbeat(token, 200)
	heartbeat(token, 200)
	projection.mu.Lock()
	if projection.writes != 1 || projection.session.AppID != f.app.AppID || projection.session.SessionHash == "runtime_session_0001" {
		t.Error("projection or duplicate write")
	}
	projection.fail = true
	projection.mu.Unlock()
	clock.Add(int64(time.Minute))
	heartbeat(token, 503)
	projection.mu.Lock()
	projection.fail = false
	projection.mu.Unlock()
	heartbeat(token, 200)
	f.restart()
	if issue(false)["enabled"] != true {
		t.Fatal("registry presence lost at restart")
	}
	projection.mu.Lock()
	defer projection.mu.Unlock()
	if projection.writes != 2 {
		t.Fatal("failed write consumed retry slot")
	}
	t.Log("HTTP token → actual Ed25519 verification → edge heartbeat → coalescing/error/retry → registry restart; MySQL projection port is a fixture, not live MySQL proof")
}
