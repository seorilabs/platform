//go:build integration

package main

import (
	platformads "github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServiceRuntimeRoleRoutingAndRegistry(t *testing.T) {
	f := newRuntimeFixture(t)
	f.enableRuntimeAds()
	f.saveApp()
	svc, err := platformads.NewService(platformads.NewStoreRepository(f.store), f.registry, newAdsEntitlements(f.store), f.users)
	if err != nil {
		t.Fatal(err)
	}
	d := &deps{store: f.store, registry: f.registry, identity: f.identity, config: f.config, ads: &adsParts{service: svc}}
	handler, err := buildHandler(config.Config{Role: config.RoleAds}, d)
	if err != nil {
		t.Fatal(err)
	}
	f.server.Close()
	f.server = httptest.NewServer(handler)
	f.request("GET", "/health/ready", "", nil, 200)
	session := f.session("role-routing-user")
	token := session["platformToken"].(string)
	f.request("GET", "/v1/ads/policy", token, nil, 200)
	f.request("GET", "/v1/content/version", token, nil, http.StatusNotFound)
	if _, err = buildHandler(config.Config{Role: config.RoleIAP}, d); err == nil {
		t.Fatal("missing IAP wiring accepted")
	}
	t.Log("actual role composition/middleware → Firestore registry readiness → signed session → owned route → cross-role route denied; invalid dependency composition fails")
}
