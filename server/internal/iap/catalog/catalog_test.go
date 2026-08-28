package catalog

import (
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

const validJSON = `{
  "version": 1,
  "entitlements": {
    "sp_galaxy_gecko": {
      "google_play": "gecko_galaxy",
      "app_store": "com.seorilabs.gecko.galaxy",
      "apps_in_toss": "ait_gecko_galaxy"
    },
    "sp_shootingstar_tokay": {
      "google_play": "tokay_star",
      "app_store": "com.seorilabs.tokay.star"
    }
  }
}`

func mustParse(t *testing.T, raw string, required ...domain.Platform) *Catalog {
	t.Helper()
	c, err := Parse([]byte(raw), required)
	if err != nil {
		t.Fatalf("파싱 실패: %v", err)
	}
	return c
}

func TestParseAndLookup(t *testing.T) {
	c := mustParse(t, validJSON, domain.PlatformGooglePlay, domain.PlatformAppStore)

	t.Run("마켓 SKU로 entitlement를 찾는다", func(t *testing.T) {
		got, err := c.EntitlementFor(domain.PlatformGooglePlay, "gecko_galaxy")
		if err != nil {
			t.Fatalf("조회 실패: %v", err)
		}
		if got != "sp_galaxy_gecko" {
			t.Errorf("= %q, want sp_galaxy_gecko", got)
		}
	})

	t.Run("기존 상품 유형은 비소모성으로 유지된다", func(t *testing.T) {
		product, err := c.ProductForApp("", domain.PlatformGooglePlay, "gecko_galaxy")
		if err != nil {
			t.Fatalf("상품 조회 실패: %v", err)
		}
		if product.EntitlementID != "sp_galaxy_gecko" || product.Type != domain.ProductNonConsumable {
			t.Fatalf("product=%+v", product)
		}
	})

	t.Run("같은 SKU라도 마켓이 다르면 안 찾힌다", func(t *testing.T) {
		// Play SKU를 App Store로 조회하면 없어야 한다
		if _, err := c.EntitlementFor(domain.PlatformAppStore, "gecko_galaxy"); err == nil {
			t.Error("마켓이 다른데 찾혔다")
		}
	})

	t.Run("모르는 SKU는 product_not_allowed", func(t *testing.T) {
		_, err := c.EntitlementFor(domain.PlatformGooglePlay, "없는상품")
		if code := platformerr.CodeOf(err); code != platformerr.CodeProductNotAllowed {
			t.Errorf("code = %q, want product_not_allowed", code)
		}
	})

	t.Run("역방향 조회", func(t *testing.T) {
		sku, ok := c.SKUFor("sp_galaxy_gecko", domain.PlatformAppsInToss)
		if !ok || sku != "ait_gecko_galaxy" {
			t.Errorf("SKUFor = (%q, %v)", sku, ok)
		}
		// AIT SKU가 없는 상품
		if _, ok := c.SKUFor("sp_shootingstar_tokay", domain.PlatformAppsInToss); ok {
			t.Error("없는 SKU를 있다고 한다")
		}
	})

	t.Run("IDs는 정렬된다", func(t *testing.T) {
		ids := c.IDs()
		if len(ids) != 2 || ids[0] != "sp_galaxy_gecko" {
			t.Errorf("IDs = %v", ids)
		}
	})
}

// 마켓별 단계적 출시를 지원한다.
// 아직 AIT에 안 올린 상품이 있어도 Play와 App Store는 동작해야 한다.
func TestRequiredPlatforms(t *testing.T) {
	t.Run("필수 마켓의 SKU가 없으면 거부", func(t *testing.T) {
		_, err := Parse([]byte(validJSON), []domain.Platform{domain.PlatformAppsInToss})
		if err == nil {
			t.Fatal("AIT SKU가 없는 상품이 있는데 통과했다")
		}
		if code := platformerr.CodeOf(err); code != platformerr.CodeCatalogIncomplete {
			t.Errorf("code = %q, want catalog_incomplete", code)
		}
	})

	t.Run("필수가 아니면 없어도 된다", func(t *testing.T) {
		mustParse(t, validJSON, domain.PlatformGooglePlay)
	})
}

// placeholder가 남은 채 배포되면 런타임에 이상하게 동작한다.
// 부팅 시점에 잡는다.
func TestRejectsPlaceholders(t *testing.T) {
	raw := `{
      "version": 1,
      "entitlements": {
        "sp_a": {"google_play": "확정 필요", "app_store": "com.x.a"}
      }
    }`
	_, err := Parse([]byte(raw), []domain.Platform{domain.PlatformGooglePlay})
	if err == nil {
		t.Fatal("placeholder를 통과시켰다")
	}
	if code := platformerr.CodeOf(err); code != platformerr.CodeCatalogIncomplete {
		t.Errorf("code = %q, want catalog_incomplete", code)
	}
}

// 같은 SKU가 두 entitlement에 붙으면 어느 쪽을 줄지 알 수 없다.
func TestRejectsDuplicateSKU(t *testing.T) {
	raw := `{
      "version": 1,
      "entitlements": {
        "sp_a": {"google_play": "same_sku"},
        "sp_b": {"google_play": "same_sku"}
      }
    }`
	_, err := Parse([]byte(raw), nil)
	if err == nil {
		t.Fatal("중복 SKU를 통과시켰다")
	}
	if code := platformerr.CodeOf(err); code != platformerr.CodeCatalogDuplicate {
		t.Errorf("code = %q, want catalog_duplicate", code)
	}
}

func TestParseValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		code platformerr.Code
	}{
		{"빈 카탈로그", `{"version":1,"entitlements":{}}`, platformerr.CodeCatalogIncomplete},
		{"깨진 JSON", `{"version":`, platformerr.CodeCatalogInvalid},
		{
			"모르는 필드",
			`{"version":1,"entitlements":{},"오타필드":1}`,
			platformerr.CodeCatalogInvalid,
		},
		{
			"entitlement 이름에 허용되지 않는 문자",
			`{"version":1,"entitlements":{"sp a":{"google_play":"x"}}}`,
			platformerr.CodeCatalogInvalid,
		},
		{
			"지원하지 않는 상품 유형",
			`{"version":1,"entitlements":{"sp_a":{"type":"subscription","google_play":"x"}}}`,
			platformerr.CodeCatalogInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.raw), nil)
			if err == nil {
				t.Fatal("거부하지 않았다")
			}
			if code := platformerr.CodeOf(err); code != tt.code {
				t.Errorf("code = %q, want %q", code, tt.code)
			}
		})
	}
}

func TestConsumableProductType(t *testing.T) {
	c := mustParse(t, `{
      "version": 2,
      "entitlements": {
        "content_ticket": {
          "type": "consumable",
          "google_play": "ungeul_content_ticket",
          "app_store": "com.seorilabs.ungeul.contentticket"
        }
      }
    }`)

	product, err := c.ProductForApp("", domain.PlatformGooglePlay, "ungeul_content_ticket")
	if err != nil {
		t.Fatal(err)
	}
	if product.EntitlementID != "content_ticket" || product.Type != domain.ProductConsumable {
		t.Fatalf("product=%+v", product)
	}
}

func TestAppScopedCatalogKeepsSameEntitlementSeparate(t *testing.T) {
	c := mustParse(t, `{
      "version": 2,
      "apps": {
        "lizard-tycoon": {"entitlements":{"ad_free":{"google_play":"lizard_ad_free"}}},
        "happy-farm": {"entitlements":{"ad_free":{"google_play":"ad_free","app_store":"com.seorilabs.happyfarm.premium.ad_free"}}}
      }
    }`)

	if got, err := c.EntitlementForApp("happy-farm", domain.PlatformGooglePlay, "ad_free"); err != nil || got != "ad_free" {
		t.Fatalf("Happy Farm lookup = %q, %v", got, err)
	}
	if _, err := c.EntitlementForApp("lizard-tycoon", domain.PlatformGooglePlay, "ad_free"); platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
		t.Fatalf("cross-app product code = %q", platformerr.CodeOf(err))
	}
	if got, ok := c.SKUForApp("lizard-tycoon", "ad_free", domain.PlatformGooglePlay); !ok || got != "lizard_ad_free" {
		t.Fatalf("lizard SKUForApp = %q, %v", got, ok)
	}
	if got, ok := c.SKUForApp("happy-farm", "ad_free", domain.PlatformGooglePlay); !ok || got != "ad_free" {
		t.Fatalf("happy SKUForApp = %q, %v", got, ok)
	}
}
