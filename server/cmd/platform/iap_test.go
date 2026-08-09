package main

import (
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

func TestNewAuditRowPromotesIndexedAdminFields(t *testing.T) {
	detail := map[string]any{
		"actor":      "syous",
		"request_id": "request-1",
		"reason":     "internal_validation",
	}
	row := newAuditRow("iap.operator_grant", "app-a", "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV", "ok", detail)
	if row.Actor != "syous" || row.RequestID != "request-1" {
		t.Fatalf("indexed fields = actor %q, requestID %q", row.Actor, row.RequestID)
	}
	if row.Detail["reason"] != "internal_validation" {
		t.Errorf("detail = %v", row.Detail)
	}
}

func TestNewAuditRowIgnoresNonStringIndexedValues(t *testing.T) {
	row := newAuditRow("iap.verified", "app-a", "", "ok", map[string]any{
		"actor":      1,
		"request_id": true,
	})
	if row.Actor != "" || row.RequestID != "" {
		t.Fatalf("non-string detail promoted: %+v", row)
	}
}

func TestValidateAppCatalogRequiresOnlyConfiguredVerifiers(t *testing.T) {
	cat, err := catalog.Parse([]byte(`{
      "version": 2,
      "apps": {
        "lizard-tycoon": {
          "entitlements": {
            "sp_galaxy_gecko": {
              "google_play": "sp_galaxy_gecko",
              "app_store": "com.seorilabs.lizardtycoon.premium.galaxy_gecko"
            }
          }
        }
      }
    }`), nil)
	if err != nil {
		t.Fatalf("catalog parse: %v", err)
	}
	app := registry.App{
		AppID: "lizard-tycoon",
		IAP: registry.IAPConfig{
			Markets:        []string{"google_play", "app_store", "apps_in_toss"},
			EntitlementIDs: []string{"sp_galaxy_gecko"},
		},
	}

	if err := validateAppCatalog(cat, app, []domain.Platform{
		domain.PlatformGooglePlay,
		domain.PlatformAppStore,
	}); err != nil {
		t.Fatalf("자격증명 없는 AIT SKU 때문에 실패했다: %v", err)
	}

	err = validateAppCatalog(cat, app, []domain.Platform{
		domain.PlatformGooglePlay,
		domain.PlatformAppStore,
		domain.PlatformAppsInToss,
	})
	if code := platformerr.CodeOf(err); code != platformerr.CodeCatalogIncomplete {
		t.Fatalf("AIT verifier가 있을 때 code = %q, want %q", code, platformerr.CodeCatalogIncomplete)
	}
}
