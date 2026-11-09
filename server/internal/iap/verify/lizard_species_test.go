package verify

import (
	"context"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"os"
	"testing"
)

// 기존 비소모품 검증·지급·권리 조회 경로에 실제 새 카탈로그를 넣는다.
func TestLizardSpeciesVerificationGrantAndRestoreList(t *testing.T) {
	raw, err := os.ReadFile("../../../../registry/iap-catalog/lizard-tycoon.json")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Parse(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, species := range []string{"bearded_dragon", "blue_tongue_skink", "veiled_chameleon"} {
		id := "sp_" + species
		for market, sku := range map[domain.Platform]string{domain.PlatformGooglePlay: id, domain.PlatformAppStore: "com.seorilabs.lizardtycoon.premium." + species} {
			t.Run(string(market)+"/"+species, func(t *testing.T) {
				purchase := activePurchase()
				purchase.Platform = market
				purchase.ProductID = sku
				v := &fakeVerifier{platform: market, purchase: purchase}
				l := &fakeLedger{active: []string{"sp_galaxy_gecko"}}
				s, err := New(Config{Verifiers: []Verifier{v}, Ledger: l, Catalog: cat})
				if err != nil {
					t.Fatal(err)
				}
				out, err := s.VerifyPurchase(context.Background(), "lizard-tycoon", "isolated-test-user", domain.Proof{Platform: market, ProductID: sku, Token: "test-proof"})
				if err != nil || out.Status != "verified" || out.EntitlementID != id || len(l.granted) != 1 {
					t.Fatalf("out=%+v err=%v grants=%d", out, err, len(l.granted))
				}
				if v.gotProof.ProductType != domain.ProductNonConsumable {
					t.Fatalf("type=%s", v.gotProof.ProductType)
				}
				rights, err := s.ListEntitlements(context.Background(), "isolated-test-user")
				if err != nil || len(rights) != 2 || rights[0] != "sp_galaxy_gecko" || rights[1] != id {
					t.Fatalf("rights=%v err=%v", rights, err)
				}
			})
		}
	}
}
