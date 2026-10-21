package catalog

import (
	"os"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
)

// Amber Lizards 카탈로그 후보. 기존 호박 묶음 5종은 상점에서 숨기지만 처리 중인 구매를
// 끝까지 지급하도록 매핑을 남긴다(economy-v5 §9). 새 SKU는 가격을 바꾸지 않고 따로 등록한다.
func TestMergeLizardCatalogCandidate(t *testing.T) {
	raw, err := os.ReadFile("../../../../registry/iap-catalog/merge-lizard.json")
	if err != nil {
		t.Fatal(err)
	}
	// 운영 부팅은 필수 마켓 없이 해석한다. 후보는 두 마켓 SKU를 모두 갖춰야 한다.
	c, err := Parse(raw, []domain.Platform{domain.PlatformGooglePlay, domain.PlatformAppStore})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		sku  string
		kind domain.ProductType
	}{
		"amber_pack_s":      {"merge_lizard_amber_pack_s", domain.ProductConsumable},
		"amber_pack_m":      {"merge_lizard_amber_pack_m", domain.ProductConsumable},
		"amber_pack_l":      {"merge_lizard_amber_pack_l", domain.ProductConsumable},
		"amber_pack_xl":     {"merge_lizard_amber_pack_xl", domain.ProductConsumable},
		"amber_pack_xxl":    {"merge_lizard_amber_pack_xxl", domain.ProductConsumable},
		"starter_pack":      {"merge_lizard_starter_pack", domain.ProductConsumable},
		"growth_boost_25":   {"merge_lizard_growth_pack", domain.ProductNonConsumable},
		"nest_slots_plus_2": {"merge_lizard_nest_expansion", domain.ProductNonConsumable},
		"amber_v2_s":        {"merge_lizard_amber_v2_s", domain.ProductConsumable},
		"amber_v2_m":        {"merge_lizard_amber_v2_m", domain.ProductConsumable},
		"amber_v2_l":        {"merge_lizard_amber_v2_l", domain.ProductConsumable},
		"amber_v2_xl":       {"merge_lizard_amber_v2_xl", domain.ProductConsumable},
		"amber_v2_xxl":      {"merge_lizard_amber_v2_xxl", domain.ProductConsumable},
		// 30일 호박은 1회 구매로 지급되는 소모품으로만 등록한다. 남은 날의 일일 지급
		// 원장은 Platform에 없다.
		"amber_pass_30d": {"merge_lizard_amber_pass_30d", domain.ProductConsumable},
		"premium_land":   {"merge_lizard_premium_land", domain.ProductNonConsumable},
	}
	if got := len(c.IDs()); got != len(want) {
		t.Fatalf("catalog has %d products, want %d", got, len(want))
	}
	for id, w := range want {
		for _, market := range []domain.Platform{domain.PlatformGooglePlay, domain.PlatformAppStore} {
			product, err := c.ProductForApp("merge-lizard", market, w.sku)
			if err != nil || product.EntitlementID != id || product.Type != w.kind {
				t.Fatalf("%s/%s: %+v %v", market, w.sku, product, err)
			}
			if !c.HasForApp("merge-lizard", id) {
				t.Fatalf("%s가 앱 범위에 없다", id)
			}
		}
		if _, ok := c.SKUForApp("merge-lizard", id, domain.PlatformAppsInToss); ok {
			t.Fatalf("AppsInToss는 출시 범위가 아니다: %s", id)
		}
	}
	// 다른 앱 범위로 새지 않는다.
	if _, err := c.ProductForApp("lizard-tycoon", domain.PlatformGooglePlay, "merge_lizard_amber_v2_s"); err == nil {
		t.Fatal("merge-lizard SKU가 다른 앱에서 허용됐다")
	}
}
