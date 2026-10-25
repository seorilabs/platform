//go:build integration

package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/seorilabs/platform/server/internal/admin"
	"github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/blocklist"
	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/operational"
	"github.com/seorilabs/platform/server/internal/registry"
)

// Google OIDC의 외부 신원 확인만 대체하고 읽기/쓰기 권한은 제품 인증기로 판정한다.
type runtimeAdminIdentity struct{}

func TestServiceRuntimeAdminStarterInboxOutboxWithoutSender(t *testing.T) {
	f := newRuntimeFixture(t)
	f.app.AppID = "starter-game"
	f.enableRuntimeAds()
	f.app.Features["iap"] = true
	f.app.IAP = registry.IAPConfig{LedgerEnvironment: registry.LedgerSandbox, Markets: []string{"google_play"}, GooglePlayPackageName: "com.seorilabs.runtimefixture", EntitlementIDs: []string{"starter_test_ticket_1"}}
	cat, err := catalog.Parse([]byte(`{"version":1,"entitlements":{"starter_test_ticket_1":{"google_play":"starter_ticket_sku"}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := admin.NewAuthenticator(runtimeAdminIdentity{}, []string{"reader@runtime.example"}, []string{"writer@runtime.example"})
	if err != nil {
		t.Fatal(err)
	}
	f.register = func(mux *http.ServeMux) {
		adsvc, e := ads.NewService(ads.NewStoreRepository(f.store), f.registry, newAdsEntitlements(f.store), f.users)
		if e != nil {
			t.Fatal(e)
		}
		// 외부 OIDC만 대체하고 실제 운영 라우트 등록부터 실행한다.
		// Admin은 전송 secret과 dispatcher 없이 outbox를 기록해야 한다.
		d := &deps{store: f.store, registry: f.registry, adminUsers: f.users, config: f.config,
			blocklist: blocklist.NewService(blocklist.NewStoreSource(f.store)),
			iap:       &iapParts{ledger: ledger.New(f.store, domain.EnvSandbox), catalog: cat}, ads: &adsParts{service: adsvc}}
		if e = registerAdminHandlers(mux, d, auth); e != nil {
			t.Fatal(e)
		}
	}
	f.saveApp()
	f.restart()
	puid := f.session("starter-admin-inbox-user")["platformUserId"].(string)
	requestID := "runtime-starter-inbox-event"
	in := map[string]any{"requestId": requestID, "platformUserId": puid, "title": "Runtime", "body": "Owned fixture", "reason": ledger.AdminReasonInternalValidation, "confirmation": "ISSUE INBOX starter-game " + puid,
		"rewards": []any{map[string]any{"kind": "entitlement", "entitlementId": "starter_test_ticket_1", "quantity": 1}}}
	for range 2 {
		f.request("POST", "/v1/admin/apps/starter-game/inbox", "fixture-writer", in, 200)
	}
	eventID := operational.StableEventID("inbox_issue", f.app.AppID, string(domain.EnvSandbox), puid, requestID)
	path, err := fspath.Parse("operational_event_outbox/" + eventID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Get(t.Context(), path); err != nil {
		t.Fatalf("admin inbox issue committed without its operational event: %v", err)
	}
}

func (runtimeAdminIdentity) Validate(_ context.Context, token string) (string, error) {
	switch token {
	case "fixture-reader":
		return "reader@runtime.example", nil
	case "fixture-writer":
		return "writer@runtime.example", nil
	}
	return "", fmt.Errorf("unknown fixture identity")
}

func TestServiceRuntimeAdminPermissionsConfigBlocksAndInbox(t *testing.T) {
	f := newRuntimeFixture(t)
	f.enableRuntimeAds()
	f.app.Features["iap"] = true
	f.app.IAP = registry.IAPConfig{LedgerEnvironment: registry.LedgerSandbox, Markets: []string{"google_play"}, GooglePlayPackageName: "com.seorilabs.runtimefixture", EntitlementIDs: []string{"starter"}}
	cat, err := catalog.Parse([]byte(`{"version":1,"entitlements":{"starter":{"google_play":"starter_sku"}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := admin.NewAuthenticator(runtimeAdminIdentity{}, []string{"reader@runtime.example"}, []string{"writer@runtime.example"})
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.NewForApp(f.store, domain.EnvSandbox, f.app.AppID)
	f.register = func(mux *http.ServeMux) {
		h, e := admin.NewHandler(ledger.New(f.store, domain.EnvSandbox), f.config, f.users, f.registry, cat, auth, nil)
		if e != nil {
			t.Fatal(e)
		}
		scoped, e := admin.NewHandler(led, f.config, f.users, f.registry, cat, auth, nil)
		if e != nil {
			t.Fatal(e)
		}
		if e = h.WithAppHandlers(map[domain.Scope]*admin.Handler{{AppID: f.app.AppID, Environment: domain.EnvSandbox}: scoped}); e != nil {
			t.Fatal(e)
		}
		h.Register(mux)
		admin.RegisterInbox(mux, auth, f.registry, f.users, nil, func(registry.App) admin.InboxIssuer { return led })
		if e = admin.RegisterBlocks(mux, auth, blocklist.NewService(blocklist.NewStoreSource(f.store)), f.registry, nil); e != nil {
			t.Fatal(e)
		}
		adsvc, e := ads.NewService(ads.NewStoreRepository(f.store), f.registry, newAdsEntitlements(f.store), f.users)
		if e != nil {
			t.Fatal(e)
		}
		if e = admin.RegisterAds(mux, auth, adsvc); e != nil {
			t.Fatal(e)
		}
	}
	f.saveApp()
	sess := f.session("admin-user")
	puid := sess["platformUserId"].(string)
	token := sess["platformToken"].(string)
	for _, path := range []string{"/v1/admin/health", "/v1/admin/metrics", "/v1/admin/orders/recent", "/v1/admin/users/" + puid, "/v1/admin/users/" + puid + "/entitlements", "/v1/admin/operator-grants", "/v1/admin/apps/" + f.app.AppID + "/iap/catalog", "/v1/admin/apps/" + f.app.AppID + "/config/update-policy", "/v1/admin/ads/health", "/v1/admin/apps/" + f.app.AppID + "/ads/config", "/v1/admin/apps/" + f.app.AppID + "/ads/health", "/v1/admin/users/" + puid + "/ads", "/v1/admin/ads/reward-claims"} {
		f.request("GET", path, "fixture-reader", nil, 200)
	}
	f.request("GET", "/v1/admin/health", "", nil, 401)
	grant := map[string]any{"requestId": "runtime-operator-grant", "platformUserId": puid, "entitlementId": "starter", "reason": ledger.AdminReasonCustomerSupportCompensation, "appId": f.app.AppID, "expectedEnvironment": "sandbox", "confirmation": "GRANT " + f.app.AppID + " " + puid + " starter"}
	f.request("POST", "/v1/admin/entitlements/grant", "fixture-reader", grant, 403)
	if f.request("POST", "/v1/admin/entitlements/grant", "fixture-writer", grant, 200)["applied"] != true {
		t.Fatal("operator grant must apply")
	}
	f.restart()
	f.request("POST", "/v1/admin/entitlements/grant", "fixture-writer", grant, 200)
	revoke := map[string]any{"requestId": "runtime-operator-revoke", "grantRequestId": "runtime-operator-grant", "platformUserId": puid, "entitlementId": "starter", "reason": ledger.AdminReasonIncorrectGrantCorrection, "appId": f.app.AppID, "expectedEnvironment": "sandbox", "confirmation": "REVOKE " + f.app.AppID + " " + puid + " starter runtime-operator-grant"}
	f.request("POST", "/v1/admin/entitlements/revoke", "fixture-writer", revoke, 200)
	active, err := led.ListActive(t.Context(), puid)
	if err != nil || len(active) != 0 {
		t.Fatal("operator revoke must remove projection, preserve history")
	}
	f.request("POST", "/v1/admin/config/maintenance", "fixture-writer", map[string]any{"appId": f.app.AppID, "minutes": 5}, 200)
	f.restart()
	if f.request("GET", "/v1/config", token, nil, 200)["maintenance"].(map[string]any)["active"] != true {
		t.Fatal("maintenance must survive restart")
	}
	f.request("POST", "/v1/admin/config/maintenance", "fixture-writer", map[string]any{"appId": f.app.AppID, "minutes": 0}, 200)
	blockPath := "/v1/admin/apps/" + f.app.AppID + "/blocks"
	f.request("POST", blockPath, "fixture-writer", map[string]string{"uid": "admin-user", "reason": "runtime isolation verification"}, 200)
	f.request("GET", blockPath, "fixture-reader", nil, 200)
	f.restart()
	f.request("POST", "/v1/auth/session", "", map[string]any{"appId": f.app.AppID, "credential": map[string]string{"kind": "firebase-id-token", "value": f.signed("admin-user", "", false)}}, 403)
	f.request("DELETE", blockPath+"/admin-user", "fixture-writer", nil, 200)
	f.restart()
	f.session("admin-user")
	mail := map[string]any{"requestId": "runtime-admin-inbox", "platformUserId": puid, "title": "Runtime", "body": "Runtime fixture", "reason": ledger.AdminReasonCustomerSupportCompensation, "confirmation": "ISSUE INBOX " + f.app.AppID + " " + puid, "rewards": []any{map[string]any{"kind": "entitlement", "entitlementId": "starter", "quantity": 1}}}
	f.request("POST", "/v1/admin/apps/"+f.app.AppID+"/inbox", "fixture-writer", mail, 200)
	f.request("POST", "/v1/inbox/runtime-admin-inbox/claim", token, struct{}{}, 200)
	f.request("POST", "/v1/inbox/claim-batch", token, map[string]any{"ids": []string{"runtime-admin-inbox"}}, 200)
	t.Log("HTTP operator read/write isolation → grant/replay/revoke → maintenance/restart → block/restart/unblock → issue/read/claim inbox; Google OIDC provider is a fixture boundary")
}
