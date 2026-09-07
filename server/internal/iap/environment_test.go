package iap

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/registry"
)

func TestIAPEnvironmentSelection(t *testing.T) {
	for _, endpoint := range []struct{ method, path, body string }{
		{"POST", "/v1/iap/verify", `{"platform":"app_store","productId":"test.sku","token":"test-proof"}`},
		{"GET", "/v1/iap/entitlements", ""},
		{"POST", "/v1/iap/account-references", ""},
	} {
		for _, tc := range []struct {
			name                string
			headers             []string
			allowed, configured bool
			want                string
		}{
			{"legacy", nil, false, false, "production"},
			{"explicit production", []string{"production"}, true, true, "production"},
			{"sandbox", []string{"sandbox"}, true, true, "sandbox"},
			{"not allowed", []string{"sandbox"}, false, true, "error"},
			{"not configured", []string{"sandbox"}, true, false, "error"},
			{"empty", []string{""}, true, true, "error"},
			{"unknown", []string{"staging"}, true, true, "error"},
			{"duplicate", []string{"sandbox", "sandbox"}, true, true, "error"},
			{"conflicting", []string{"production", "sandbox"}, true, true, "error"},
			{"combined", []string{"production,sandbox"}, true, true, "error"},
		} {
			t.Run(endpoint.path+"/"+tc.name, func(t *testing.T) {
				prod, sandbox := &fakeService{}, &fakeService{}
				session := paidSession()
				app := registry.App{AppID: session.AppID, Features: map[string]bool{"iap": true},
					IAP: registry.IAPConfig{LedgerEnvironment: registry.LedgerProduction, Markets: []string{"app_store"}, AppleSandboxEnabled: tc.allowed}}
				h := NewHandler(prod, &fakeSessions{sess: session}).WithApps(&fakeApps{app: app})
				if tc.configured {
					h.WithEnvironmentServices(map[domain.Scope]Service{{AppID: session.AppID, Environment: domain.EnvSandbox}: sandbox})
				}
				mux := http.NewServeMux()
				h.Register(mux)
				r := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(endpoint.body))
				r.Header.Set("Content-Type", "application/json")
				for _, value := range tc.headers {
					r.Header.Add("X-Seori-IAP-Environment", value)
				}
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if tc.want == "error" {
					if w.Code < 400 || prod.gotPUID != "" || sandbox.gotPUID != "" {
						t.Fatalf("unsafe dispatch: %d prod=%q sandbox=%q", w.Code, prod.gotPUID, sandbox.gotPUID)
					}
					return
				}
				if w.Code != http.StatusOK {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				if tc.want == "production" && (prod.gotPUID != session.PlatformUserID || sandbox.gotPUID != "") {
					t.Fatal("production request crossed environments")
				}
				if tc.want == "sandbox" && (sandbox.gotPUID != session.PlatformUserID || prod.gotPUID != "") {
					t.Fatal("sandbox request crossed environments")
				}
			})
		}
	}
}
