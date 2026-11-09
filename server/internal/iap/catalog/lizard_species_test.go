package catalog

import (
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"os"
	"testing"
)

func TestLizardNewSpeciesNativeOnlyAndAppIsolation(t *testing.T) {
	raw, err := os.ReadFile("../../../../registry/iap-catalog/lizard-tycoon.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw, []domain.Platform{domain.PlatformGooglePlay, domain.PlatformAppStore})
	if err != nil {
		t.Fatal(err)
	}
	for _, species := range []string{"bearded_dragon", "blue_tongue_skink", "veiled_chameleon"} {
		id := "sp_" + species
		for market, sku := range map[domain.Platform]string{domain.PlatformGooglePlay: id, domain.PlatformAppStore: "com.seorilabs.lizardtycoon.premium." + species} {
			p, err := c.ProductForApp("lizard-tycoon", market, sku)
			if err != nil || p.EntitlementID != id || p.Type != domain.ProductNonConsumable {
				t.Fatalf("%s/%s: %+v %v", market, id, p, err)
			}
			for _, app := range []string{"happy-farm", "other-app", ""} {
				if _, err := c.ProductForApp(app, market, sku); platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
					t.Fatalf("cross-app %s/%s: %v", app, id, err)
				}
			}
			if _, err := c.ProductForApp("lizard-tycoon", domain.PlatformAppsInToss, sku); platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
				t.Fatalf("AppsInToss accepted %s", sku)
			}
		}
		if _, ok := c.SKUForApp("lizard-tycoon", id, domain.PlatformAppsInToss); ok {
			t.Fatalf("AppsInToss SKU present: %s", id)
		}
	}
}
