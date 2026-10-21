//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap"
	"github.com/seorilabs/platform/server/internal/iap/binding"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/providers/play"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/registry"
)

// 기간 이용권: 실제 HTTP 검증 → 상태 조회 → 하루 몫 수령 → 재시작 뒤 같은 요청 재시도 →
// 같은 날 다른 요청과 주입 필드 거부 → 재검증으로 날이 초기화되지 않음 → 취소 반영. ADR 0036.
func TestServiceRuntimePassVerifyClaimRestartAndRefund(t *testing.T) {
	f := newRuntimeFixture(t)
	f.app.Features["iap"] = true
	f.app.IAP = registry.IAPConfig{LedgerEnvironment: registry.LedgerSandbox, Markets: []string{"google_play"}, GooglePlayPackageName: "com.seorilabs.runtimefixture", EntitlementIDs: []string{"amber_pass_30d"}}
	var accountRef atomic.Value
	accountRef.Store("")
	var cancelled atomic.Bool
	market := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(204)
			return
		}
		state := 0
		if cancelled.Load() {
			state = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"purchaseTimeMillis": fmt.Sprint(time.Now().Add(-time.Minute).UnixMilli()), "purchaseState": state, "consumptionState": 0, "orderId": "runtime-pass-order", "obfuscatedExternalAccountId": accountRef.Load(), "quantity": 1})
	}))
	t.Cleanup(market.Close)
	provider, err := play.New(f.app.IAP.GooglePlayPackageName, market.Client(), play.WithBaseURL(market.URL))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Parse([]byte(`{"version":2,"apps":{"runtime-fixture":{"entitlements":{"amber_pass_30d":{"type":"consumable","google_play":"pass_sku","pass":{"immediate_amount":300,"daily_amount":50,"days":30,"missed_days":"forfeit"}}}}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := binding.NewKeyring(f.secret)
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.NewForApp(f.store, domain.EnvSandbox, f.app.AppID)
	f.register = func(mux *http.ServeMux) {
		svc, e := verify.New(verify.Config{Verifiers: []verify.Verifier{provider}, Ledger: led, Catalog: cat, Keyring: keyring, Outbox: led, Apps: f.registry, AppLedgers: map[string]verify.Ledger{f.app.AppID: led}})
		if e != nil {
			t.Fatal(e)
		}
		iap.NewHandler(svc, f.identity).WithApps(f.registry).Register(mux)
	}
	f.saveApp()
	f.restart()
	token := f.session("pass-user")["platformToken"].(string)
	accountRef.Store(f.request("POST", "/v1/iap/account-references", token, nil, 200)["googlePlayObfuscatedAccountId"].(string))
	purchase := map[string]string{"platform": "google_play", "productId": "pass_sku", "token": "fixture-pass-token"}
	if got := f.request("POST", "/v1/iap/verify", token, purchase, 200); got["status"] != "verified" || got["granted"] != true {
		t.Fatalf("verify=%v", got)
	}
	pass := func() map[string]any {
		passes, _ := f.request("GET", "/v1/iap/passes", token, nil, 200)["passes"].([]any)
		if len(passes) != 1 {
			t.Fatalf("passes=%v", passes)
		}
		return passes[0].(map[string]any)
	}
	if p := pass(); p["claimable"] != true || p["immediateAmount"] != float64(300) || p["dailyAmount"] != float64(50) || p["remainingDays"] != float64(30) {
		t.Fatalf("구매 직후=%v", p)
	}
	claim := map[string]string{"entitlementId": "amber_pass_30d", "requestId": "runtime-pass-claim-1"}
	if got := f.request("POST", "/v1/iap/passes/claim", token, claim, 200); got["applied"] != true || got["amount"] != float64(50) {
		t.Fatalf("claim=%v", got)
	}
	f.restart()
	if got := f.request("POST", "/v1/iap/passes/claim", token, claim, 200); got["applied"] != false || got["amount"] != float64(50) {
		t.Fatalf("replay=%v", got)
	}
	f.request("POST", "/v1/iap/passes/claim", token, map[string]string{"entitlementId": "amber_pass_30d", "requestId": "runtime-pass-claim-2"}, 409)
	f.request("POST", "/v1/iap/passes/claim", token, map[string]any{"entitlementId": "amber_pass_30d", "requestId": "runtime-pass-claim-3", "amount": 999}, 400)
	f.request("POST", "/v1/iap/verify", token, purchase, 200)
	f.request("POST", "/v1/iap/passes/claim", token, map[string]string{"entitlementId": "amber_pass_30d", "requestId": "runtime-pass-claim-4"}, 409)
	if p := pass(); p["claimedDays"] != float64(1) || p["remainingDays"] != float64(29) {
		t.Fatalf("재검증 뒤=%v", p)
	}
	cancelled.Store(true)
	if got := f.request("POST", "/v1/iap/verify", token, purchase, 200); got["status"] != "revoked" {
		t.Fatalf("cancel=%v", got)
	}
	if p := pass(); p["active"] != false || p["remainingDays"] != float64(0) {
		t.Fatalf("취소 뒤=%v", p)
	}
	t.Log("HTTP verify consumable pass → status → claim → restart replay → same-day/injected reject → reverify → cancel; real market purchase untested")
}
