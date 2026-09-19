package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/registry"
)

func TestAdminEnvironmentBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, header, identity, path, body, want string
		allowed                                  bool
	}{
		{name: "legacy health", path: "/v1/admin/health", want: "production"},
		{name: "sandbox health", header: "sandbox", path: "/v1/admin/health", want: "sandbox", allowed: true},
		{name: "sandbox grant", header: "sandbox", path: "/v1/admin/entitlements/grant", body: grantBody("g", testPUID, "sp_a", testGrantReason), want: "grant", allowed: true},
		{name: "sandbox reset", header: "sandbox", path: "/v1/admin/iap/sandbox-reset", body: sandboxResetBody("r", testPUID, testResetReason, true), want: "reset", allowed: true},
		{name: "omitted header cannot reset production", path: "/v1/admin/iap/sandbox-reset", body: sandboxResetBody("r", testPUID, testResetReason, true), want: "error", allowed: true},
		{name: "header body mismatch", header: "production", path: "/v1/admin/entitlements/grant", body: grantBody("g", testPUID, "sp_a", testGrantReason), want: "error", allowed: true},
		{name: "unapproved app", header: "sandbox", path: "/v1/admin/entitlements/grant", body: grantBody("g", testPUID, "sp_a", testGrantReason), want: "error"},
		{name: "read identity cannot grant", header: "sandbox", identity: backofficeReadSA, path: "/v1/admin/entitlements/grant", body: grantBody("g", testPUID, "sp_a", testGrantReason), want: "error", allowed: true},
		{name: "unknown environment", header: "staging", path: "/v1/admin/health", want: "error", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity := tc.identity
			if identity == "" {
				identity = backofficeSA
			}
			prod, sandbox := &fakeLedger{env: domain.EnvProduction}, &fakeLedger{env: domain.EnvSandbox}
			h := newHandler(t, prod, &fakeValidator{email: identity}, &fakeAuditor{})
			apps := &fakeApps{app: registry.App{AppID: "a", Status: registry.StatusActive, Features: map[string]bool{"iap": true},
				IAP: registry.IAPConfig{LedgerEnvironment: registry.LedgerProduction, LegacyUnscopedLedger: true, Markets: []string{"app_store"}, AppleSandboxEnabled: tc.allowed, EntitlementIDs: []string{"sp_a"}}}}
			h.apps = apps
			other, err := NewHandler(sandbox, h.config, h.users, apps, h.catalog, h.auth, h.auditor)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.WithEnvironmentHandlers(map[domain.Environment]*Handler{domain.EnvSandbox: other}); err != nil {
				t.Fatal(err)
			}
			method := http.MethodGet
			if tc.body != "" {
				method = http.MethodPost
			}
			r := httptest.NewRequest(method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer test-token")
			r.Header.Set("X-Seori-Actor", "test-operator")
			if tc.header != "" {
				r.Header.Set("X-Seori-IAP-Environment", tc.header)
			}
			mux := http.NewServeMux()
			h.Register(mux)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if len(prod.grantCalls)+len(prod.resetCalls) != 0 {
				t.Fatal("sandbox operation touched production")
			}
			if tc.want == "error" {
				if w.Code < 400 || len(sandbox.grantCalls)+len(sandbox.resetCalls) != 0 {
					t.Fatalf("unsafe dispatch: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			switch tc.want {
			case "grant":
				if len(sandbox.grantCalls) != 1 || sandbox.grantCalls[0].ActorLogin != "test-operator" {
					t.Fatal("sandbox grant lost its target or actor")
				}
			case "reset":
				if len(sandbox.resetCalls) != 1 {
					t.Fatal("sandbox reset did not reach sandbox")
				}
			default:
				_, result, _ := decodeEnvelope(t, w)
				if result["environment"] != tc.want {
					t.Fatalf("wrong health environment: %v", result)
				}
			}
		})
	}
}
