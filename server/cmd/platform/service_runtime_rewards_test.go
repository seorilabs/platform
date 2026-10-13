//go:build integration

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap"
	"github.com/seorilabs/platform/server/internal/iap/binding"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/providers/play"
	"github.com/seorilabs/platform/server/internal/iap/refundreview"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/iap/webhook"
	"github.com/seorilabs/platform/server/internal/iap/worker"
	"github.com/seorilabs/platform/server/internal/registry"
)

func (f *runtimeFixture) saveApp() {
	f.t.Helper()
	p, _ := fspath.Parse("apps/" + f.app.AppID)
	if err := f.store.Set(f.t.Context(), p, f.app); err != nil {
		f.t.Fatal(err)
	}
	f.restart()
}

func (f *runtimeFixture) enableRuntimeAds() {
	f.app.Features["ads"] = true
	f.app.Ads = registry.AdsConfig{Providers: []string{"admob"}, Placements: []registry.AdsPlacementConfig{{ID: "runtime_reward", Format: "rewarded", Providers: map[string]registry.AdsProviderConfig{"admob": {AndroidAdUnitID: "ca-app-pub-3940256099942544/5224354917", IOSAdUnitID: "ca-app-pub-3940256099942544/1712485313"}}, Reward: &registry.AdsRewardConfig{Key: "coin", MinAmount: 1, MaxAmount: 1}, DailyLimit: 3}}}
}

func TestServiceRuntimeAdsSignedSSVAndDelivery(t *testing.T) {
	f := newRuntimeFixture(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"keyId": 7, "pem": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}}})
	}))
	t.Cleanup(keys.Close)
	f.enableRuntimeAds()
	f.register = func(mux *http.ServeMux) {
		svc, e := ads.NewService(ads.NewStoreRepository(f.store), f.registry, newAdsEntitlements(f.store), f.users)
		if e != nil {
			t.Fatal(e)
		}
		ads.NewHandler(svc, f.identity, ads.NewAdMobVerifier(keys.Client(), keys.URL)).Register(mux)
	}
	f.saveApp()
	sess := f.session("ads-user")
	token := sess["platformToken"].(string)
	if f.request("GET", "/v1/ads/policy", token, nil, 200)["adsEnabled"] != true {
		t.Fatal("reward policy must be enabled")
	}
	body := map[string]any{"requestId": "runtime-ad-request", "placement": "runtime_reward", "provider": "admob", "clientPlatform": "android", "reward": map[string]any{"key": "coin", "amount": 1}}
	claim := f.request("POST", "/v1/ads/reward-claims", token, body, 201)
	id := claim["claimId"].(string)
	if f.request("POST", "/v1/ads/reward-claims", token, body, 201)["claimId"] != id {
		t.Fatal("same request must preserve claim")
	}
	f.request("POST", "/v1/ads/reward-claims/"+id+"/ack", token, nil, 409)
	f.request("POST", "/v1/ads/reward-claims/"+id+"/confirm", token, map[string]string{"transactionId": "client-cannot-confirm-admob"}, 422)
	raw := strings.Join([]string{"ad_network=5450213213286189855", "ad_unit=5224354917", "custom_data=" + url.QueryEscape(id), "reward_amount=1", "reward_item=coin", fmt.Sprintf("timestamp=%d", time.Now().UnixMilli()), "transaction_id=runtime-ssv-transaction", "user_id=" + url.QueryEscape(sess["platformUserId"].(string))}, "&")
	digest := sha256.Sum256([]byte(raw))
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	query := raw + "&signature=" + url.QueryEscape(base64.RawURLEncoding.EncodeToString(signature)) + "&key_id=7"
	f.request("GET", "/v1/ads/admob/ssv/"+f.app.AppID+"?"+query, "", nil, 200)
	f.restart()
	f.request("GET", "/v1/ads/reward-claims/"+id, token, nil, 200)
	if f.request("POST", "/v1/ads/reward-claims/"+id+"/ack", token, nil, 200)["state"] != "delivered" {
		t.Fatal("verified reward must be delivered")
	}
	f.request("POST", "/v1/ads/reward-claims/"+id+"/ack", token, nil, 200)
	other := f.session("other-ads-user")
	f.request("GET", "/v1/ads/reward-claims/"+id, other["platformToken"].(string), nil, 404)
	t.Log("HTTP policy/claim/replay → real ECDSA SSV → restart → deliver/replay/ownership; native ad display untested")
}

func TestServiceRuntimePurchaseCompletionWorkerAndRefund(t *testing.T) {
	f := newRuntimeFixture(t)
	f.app.Features["iap"] = true
	f.app.IAP = registry.IAPConfig{LedgerEnvironment: registry.LedgerSandbox, Markets: []string{"google_play"}, GooglePlayPackageName: "com.seorilabs.runtimefixture", EntitlementIDs: []string{"starter"}}
	var completes atomic.Int32
	var accountRef atomic.Value
	accountRef.Store("")
	var refunded atomic.Bool
	market := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			if completes.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(204)
			return
		}
		state := "PURCHASED"
		if refunded.Load() {
			state = "CANCELLED"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"purchaseStateContext": map[string]string{"purchaseState": state}, "orderId": "runtime-fixture-order", "obfuscatedExternalAccountId": accountRef.Load(), "productLineItem": []any{map[string]any{"productId": "starter_sku", "productOfferDetails": map[string]int{"quantity": 1}}}, "purchaseCompletionTime": time.Now().UTC().Format(time.RFC3339), "acknowledgementState": "ACKNOWLEDGEMENT_STATE_PENDING"})
	}))
	t.Cleanup(market.Close)
	provider, err := play.New(f.app.IAP.GooglePlayPackageName, market.Client(), play.WithBaseURL(market.URL))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Parse([]byte(`{"version":1,"entitlements":{"starter":{"google_play":"starter_sku"}}}`), nil)
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
		sealer, e := refundreview.NewKeyring(refundreview.Key{ID: "runtime", Material: f.secret})
		if e != nil {
			t.Fatal(e)
		}
		wh, e := webhook.NewPlayHandler(webhook.PlayConfig{Validator: runtimeAdminIdentity{}, Verifier: provider, Events: led, Reconciler: led, PackageName: f.app.IAP.GooglePlayPackageName, AllowedEmails: []string{"writer@runtime.example"}, Apps: f.registry, RefundReviews: led, RefundSealer: sealer})
		if e != nil {
			t.Fatal(e)
		}
		wh.Register(mux)
	}
	f.saveApp()
	sess := f.session("purchase-user")
	token := sess["platformToken"].(string)
	puid := sess["platformUserId"].(string)
	accountRef.Store(f.request("POST", "/v1/iap/account-references", token, nil, 200)["googlePlayObfuscatedAccountId"].(string))
	body := map[string]string{"platform": "google_play", "productId": "starter_sku", "token": "fixture-purchase-token"}
	if f.request("POST", "/v1/iap/verify", token, body, 200)["status"] != "verified" {
		t.Fatal("completion failure must not undo grant")
	}
	f.restart()
	w, err := worker.New(worker.Config{Outbox: ledger.NewForApp(f.store, domain.EnvSandbox, f.app.AppID), Completers: map[domain.Platform]worker.Completer{domain.PlatformGooglePlay: provider}, MaxAttempts: 3, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := w.RunOnce(t.Context())
	if err != nil || stats.Completed != 1 {
		t.Fatalf("worker stats=%+v err=%v", stats, err)
	}
	f.request("POST", "/v1/iap/verify", token, body, 200)
	active, err := led.ListActive(t.Context(), puid)
	if err != nil || len(active) != 1 {
		t.Fatal("purchase replay duplicated entitlement")
	}
	refunded.Store(true)
	notification, _ := json.Marshal(map[string]any{"version": "1.0", "packageName": f.app.IAP.GooglePlayPackageName, "eventTimeMillis": fmt.Sprint(time.Now().Add(time.Second).UnixMilli()), "oneTimeProductNotification": map[string]any{"notificationType": 2, "purchaseToken": "fixture-purchase-token", "sku": "starter_sku"}})
	push := map[string]any{"message": map[string]string{"data": base64.StdEncoding.EncodeToString(notification), "messageId": "runtime-refund-event"}, "subscription": "fixture-subscription"}
	f.request("POST", "/v1/iap/webhooks/play", "", push, 401)
	f.request("POST", "/v1/iap/webhooks/play", "fixture-writer", push, 200)
	f.restart()
	f.request("POST", "/v1/iap/webhooks/play", "fixture-writer", push, 200)
	f.restart()
	f.request("GET", "/v1/iap/entitlements", token, nil, 200)
	active, err = led.ListActive(t.Context(), puid)
	if err != nil || len(active) != 0 {
		t.Fatal("refund must persist without deleting order history")
	}
	t.Log("refs → production Play adapter HTTP fixture → persisted grant/completion failure → restart → outbox worker → replay/authenticated RTDN refund/restart/replay; real market purchase untested")
}
