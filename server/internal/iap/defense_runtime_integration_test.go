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
	entitlements := map[string]any{}
	var entitlementIDs []string
	for _, units := range []int{100, 550, 1150, 2400, 6200} {
		id, sku := fmt.Sprintf("defense_crystals_%d", units), fmt.Sprintf("crystals_%d", units)
		entitlementIDs = append(entitlementIDs, id)
		entitlements[id] = map[string]any{"type": "consumable", "google_play": sku, "app_store": sku}
	}
	rawCatalog, e := json.Marshal(map[string]any{"version": 2, "apps": map[string]any{"mechanical-frontier": map[string]any{"entitlements": entitlements}}})
	if e != nil {
		t.Fatal(e)
	}
	cat, e := catalog.Parse(rawCatalog, nil)
	if e != nil {
		t.Fatal(e)
	}
	app := registry.App{AppID: "mechanical-frontier", Features: map[string]bool{"iap": true}}
	app.IAP.RequireLinkedAccount = true
	app.IAP.EntitlementIDs = entitlementIDs
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
	for range 3 {
		if unchanged := read(); unchanged.State.Version != before.State.Version {
			t.Fatalf("repeated empty GET advanced version %+v -> %+v", before.State, unchanged.State)
		}
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
	for range 3 {
		if unchanged := read(); unchanged.State.Version != funded.State.Version {
			t.Fatalf("repeated funded GET advanced version %+v -> %+v", funded.State, unchanged.State)
		}
	}
	for _, body := range []string{
		`{"requestId":"short","actionId":"damage","catalogVersion":"defense-1"}`,
		`{"requestId":"invalid_action_0001","actionId":"grant-money","catalogVersion":"defense-1"}`,
		`{"requestId":"invalid_run_0001","actionId":"revive","catalogVersion":"defense-1","runId":"short"}`,
		`{"requestId":"missing_run_0001","actionId":"revive","catalogVersion":"defense-1"}`,
	} {
		status, raw := call(http.MethodPost, "/v1/iap/defense/actions", body)
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if e := json.Unmarshal(raw, &failure); e != nil || status != 400 || failure.Error.Code != "request_invalid" {
			t.Fatalf("invalid request status=%d body=%s err=%v", status, raw, e)
		}
	}
	status, raw := call(http.MethodPost, "/v1/iap/defense/actions", `{"requestId":"insufficient_0001","actionId":"storm","catalogVersion":"defense-1"}`)
	var rejected struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if e := json.Unmarshal(raw, &rejected); e != nil || status != 403 || rejected.Error.Code != "content_ticket_empty" {
		t.Fatalf("insufficient funds status=%d body=%s err=%v", status, raw, e)
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
