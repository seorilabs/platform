package catalog

import (
	"os"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
)

func TestLizardCollectionCatalogKeepsExistingProducts(t *testing.T) {
	raw, err := os.ReadFile("../../../../registry/iap-catalog/lizard-tycoon.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw, []domain.Platform{domain.PlatformGooglePlay, domain.PlatformAppStore})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(c.IDs()); got != 15 {
		t.Fatalf("catalog has %d products, want 10 existing and 5 crystals", got)
	}
	for _, id := range []string{"sp_galaxy_gecko", "ft_rack_pack_1", "ck_starlight_accessory_set"} {
		product, err := c.ProductForApp("lizard-tycoon", domain.PlatformGooglePlay, id)
		if err != nil || product.EntitlementID != id || product.Type != domain.ProductNonConsumable {
			t.Fatalf("existing product %s changed: %+v %v", id, product, err)
		}
		if _, ok := c.SKUForApp("lizard-tycoon", id, domain.PlatformAppsInToss); !ok {
			t.Fatalf("existing AppsInToss SKU missing: %s", id)
		}
	}
	for _, id := range []string{"crystal_300", "crystal_1000", "crystal_3200", "crystal_5500", "crystal_starter"} {
		wantType := domain.ProductConsumable
		if id == "crystal_starter" {
			wantType = domain.ProductNonConsumable
		}
		for _, market := range []domain.Platform{domain.PlatformGooglePlay, domain.PlatformAppStore} {
			product, err := c.ProductForApp("lizard-tycoon", market, id)
			if err != nil || product.EntitlementID != id || product.Type != wantType {
				t.Fatalf("%s/%s: %+v %v", market, id, product, err)
			}
		}
		if _, ok := c.SKUForApp("lizard-tycoon", id, domain.PlatformAppsInToss); ok {
			t.Fatalf("crystal SKU unexpectedly available in AppsInToss: %s", id)
		}
	}
}
