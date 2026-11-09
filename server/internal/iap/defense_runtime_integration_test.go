//go:build integration

package iap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/store"
)

// The real HTTP router, application service and Firestore ledger run together.
// Only identity and the market proof are fixtures; no production endpoint is reachable here.
func TestDefenseHTTPRuntimeRoundTripAndRefund(t *testing.T) {
	if !strings.HasPrefix(os.Getenv("FIRESTORE_EMULATOR_HOST"), "127.0.0.1:") || !strings.HasPrefix(os.Getenv("GOOGLE_CLOUD_PROJECT"), "demo-") {
		t.Skip("requires loopback Firestore emulator and demo project")
	}
	ctx := context.Background()
	st, e := store.New(ctx, os.Getenv("GOOGLE_CLOUD_PROJECT"), "stg_")
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	l := ledger.NewForApp(st, domain.EnvSandbox, "mechanical-frontier")
	cat, e := catalog.Parse([]byte(`{"version":2,"apps":{"mechanical-frontier":{"entitlements":{"defense_crystals_100":{"type":"consumable","google_play":"crystals_100","app_store":"crystals_100"}}}}}`), nil)
	if e != nil {
		t.Fatal(e)
	}
	app := registry.App{AppID: "mechanical-frontier", Features: map[string]bool{"iap": true}}
	app.IAP.RequireLinkedAccount = true
	apps := &fakeApps{app: app}
	svc, e := verify.New(verify.Config{Ledger: l, AppLedgers: map[string]verify.Ledger{"mechanical-frontier": l}, Catalog: cat, Apps: apps})
	if e != nil {
		t.Fatal(e)
	}
	sess := paidSession()
	sess.AppID = app.AppID
	sess.IsLinkedAccount = true
	sess.PlatformUserID = fmt.Sprintf("pu-runtime-%d", time.Now().UnixNano())
	h := NewHandler(svc, &fakeSessions{sess: sess}).WithApps(apps)
	mux := http.NewServeMux()
	h.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	call := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, e := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		response, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(response.Body)
		if e != nil {
			t.Fatal(e)
		}
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("financial HTTP cache policy missing")
		}
		return response.StatusCode, raw
	}
	read := func() ledger.DefenseSnapshot {
		t.Helper()
		code, raw := call(http.MethodGet, "/v1/iap/defense", "")
		var out struct {
			OK     bool                   `json:"ok"`
			Result ledger.DefenseSnapshot `json:"result"`
		}
		if e := json.Unmarshal(raw, &out); e != nil || code != 200 || !out.OK {
			t.Fatalf("GET status=%d err=%v body=%s", code, e, raw)
		}
		return out.Result
	}
	before := read()
	if before.State.Balance != 0 {
		t.Fatalf("new account %+v", before.State)
	}
	now := time.Now().UTC().Truncate(time.Second)
	purchase := domain.VerifiedPurchase{Platform: domain.PlatformGooglePlay, ProductID: "crystals_100", CanonicalID: fmt.Sprintf("proof-%d", time.Now().UnixNano()), ProviderOrderID: "test-order", PurchasedAt: now, ObservedAt: now, State: domain.StateActive}
	if _, e = l.Grant(ctx, ledger.GrantInput{PlatformUserID: sess.PlatformUserID, EntitlementID: "defense_crystals_100", Purchase: purchase}); e != nil {
		t.Fatal(e)
	}
	funded := read()
	if funded.State.Balance != 100 {
		t.Fatalf("verified funds %+v", funded.State)
	}
	code, first := call(http.MethodPost, "/v1/iap/defense/actions", defenseBody)
	if code != 200 {
		t.Fatalf("action status=%d body=%s", code, first)
	}
	code, replay := call(http.MethodPost, "/v1/iap/defense/actions", defenseBody)
	if code != 200 || !bytes.Equal(first, replay) {
		t.Fatalf("HTTP replay differs first=%s replay=%s", first, replay)
	}
	if s := read(); s.State.Balance != 75 || s.State.Upgrades["damage"] != 1 {
		t.Fatalf("replay spent twice %+v", s.State)
	}
	code, _ = call(http.MethodPost, "/v1/iap/defense/actions", `{"requestId":"action_0002","actionId":"core","catalogVersion":"defense-1","cost":0}`)
	if code != 400 {
		t.Fatal("HTTP accepted client price")
	}
	refunded := purchase
	refunded.State = domain.StateRevoked
	refunded.ObservedAt = now.Add(time.Second)
	if e = l.RevokeByCanonicalID(ctx, refunded); e != nil {
		t.Fatal(e)
	}
	state := read()
	if state.State.Balance != -25 || state.State.Debt != 25 || state.State.Enabled {
		t.Fatalf("HTTP refund state %+v", state.State)
	}
	code, replay = call(http.MethodPost, "/v1/iap/defense/actions", defenseBody)
	if code != 200 || !bytes.Equal(first, replay) {
		t.Fatalf("refund changed original receipt first=%s replay=%s", first, replay)
	}
	var receipt struct {
		Result ledger.DefenseReceipt `json:"result"`
	}
	if e = json.Unmarshal(replay, &receipt); e != nil {
		t.Fatal(e)
	}
	if state.State.Version <= receipt.Result.State.Version {
		t.Fatal("refund snapshot did not outrank immutable receipt")
	}
}
