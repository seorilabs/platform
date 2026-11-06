//go:build integration

package iap

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type independentSaltProvider struct {
	purchase  domain.VerifiedPurchase
	completed int
}

func (f *independentSaltProvider) Platform() domain.Platform { return domain.PlatformGooglePlay }
func (f *independentSaltProvider) Verify(context.Context, domain.Proof) (domain.VerifiedPurchase, error) {
	return f.purchase, nil
}
func (f *independentSaltProvider) CompleteGrant(context.Context, domain.VerifiedPurchase) error {
	f.completed++
	return nil
}
func TestSaltRegressionTCPHTTPVerifySnapshotRefundAndHeldCompletion(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("isolated emulator required")
	}
	ctx := context.Background()
	now := time.Unix(1793941200, 0).UTC()
	st, err := store.New(ctx, "demo-salt-http-independent", fmt.Sprintf("salt_http_%d_", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	l := ledger.NewForApp(st, domain.EnvSandbox, "salt-compass").WithClock(func() time.Time { return now })
	cat, err := catalog.Parse([]byte(`{"version":2,"apps":{"salt-compass":{"entitlements":{"saltcompass_seal_s":{"type":"consumable","google_play":"saltcompass_seal_s"},"saltcompass_starter":{"type":"consumable","google_play":"saltcompass_starter"}}}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	apps := &fakeApps{app: registry.App{AppID: "salt-compass", Features: map[string]bool{"iap": true}, IAP: registry.IAPConfig{Markets: []string{"google_play"}, EntitlementIDs: []string{"saltcompass_seal_s", "saltcompass_starter"}}}}
	testPurchase := true
	provider := &independentSaltProvider{purchase: domain.VerifiedPurchase{Platform: domain.PlatformGooglePlay, Environment: domain.EnvSandbox, ProductID: "saltcompass_seal_s", CanonicalID: "independent_http_order", ProviderOrderID: "independent_http_provider_order", State: domain.StateActive, PurchasedAt: now, ObservedAt: now, Completion: domain.CompletionGoogleConsume, IsTestPurchase: &testPurchase}}
	svc, err := verify.New(verify.Config{Ledger: l, AppLedgers: map[string]verify.Ledger{"salt-compass": l}, Catalog: cat, Apps: apps, Verifiers: []verify.Verifier{provider}, Outbox: l})
	if err != nil {
		t.Fatal(err)
	}
	sess := paidSession()
	sess.AppID = "salt-compass"
	h := NewHandler(svc, &fakeSessions{sess: sess}).WithApps(apps)
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	call := func(method, path, body string) (int, map[string]json.RawMessage) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, parsed
	}
	status, _ := call("POST", "/v1/iap/salt-compass/transactions", `{"requestId":"boot","action":"bootstrap","legacySeals":5}`)
	if status != 200 {
		t.Fatalf("bootstrap status=%d", status)
	}
	proof := `{"platform":"google_play","productId":"saltcompass_seal_s","token":"independent_synthetic_proof"}`
	for range 2 {
		status, _ = call("POST", "/v1/iap/verify", proof)
		if status != 200 {
			t.Fatalf("verify status=%d", status)
		}
	}
	status, body := call("GET", "/v1/iap/salt-compass", "")
	var snapshot ledger.SaltSnapshot
	if err := json.Unmarshal(body["result"], &snapshot); err != nil {
		t.Fatal(err)
	}
	if status != 200 || snapshot.Available != 15 || provider.completed != 2 {
		t.Fatalf("HTTP normal/replay available=%d completed=%d status=%d", snapshot.Available, provider.completed, status)
	}
	revoked := provider.purchase
	revoked.State = domain.StateRevoked
	revoked.ObservedAt = now.Add(time.Minute)
	if err := l.RevokeByCanonicalID(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	status, body = call("GET", "/v1/iap/salt-compass", "")
	if err := json.Unmarshal(body["result"], &snapshot); err != nil {
		t.Fatal(err)
	}
	if status != 200 || snapshot.Available != 5 {
		t.Fatalf("HTTP refund available=%d status=%d", snapshot.Available, status)
	}
	status, _ = call("POST", "/v1/iap/verify", proof)
	if status == 200 || provider.completed != 2 {
		t.Fatalf("refunded replay completed: status=%d completions=%d", status, provider.completed)
	}
	provider.purchase.ProductID = "saltcompass_starter"
	provider.purchase.CanonicalID = "independent_http_unreserved_starter"
	status, _ = call("POST", "/v1/iap/verify", `{"platform":"google_play","productId":"saltcompass_starter","token":"independent_synthetic_starter"}`)
	if status == 200 || provider.completed != 2 {
		t.Fatalf("unreserved starter completed: status=%d completions=%d", status, provider.completed)
	}
	status, body = call("GET", "/v1/iap/salt-compass", "")
	if err := json.Unmarshal(body["result"], &snapshot); err != nil {
		t.Fatal(err)
	}
	if status != 200 || snapshot.Available != 5 || snapshot.Starter.Purchased {
		t.Fatalf("held purchase changed state available=%d purchased=%v", snapshot.Available, snapshot.Starter.Purchased)
	}
}
