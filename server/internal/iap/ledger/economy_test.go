package ledger

import (
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
)

func TestEconomyTestPurchaseMarketEvidence(t *testing.T) {
	production := New(nil, domain.EnvProduction).WithAppID("lizard-tycoon")
	sandbox := New(nil, domain.EnvSandbox).WithAppID("lizard-tycoon")
	testFlag, normalFlag := true, false
	cases := []struct {
		name     string
		ledger   *Ledger
		purchase domain.VerifiedPurchase
		want     bool
	}{
		{"Play license test", production, domain.VerifiedPurchase{Platform: domain.PlatformGooglePlay, IsTestPurchase: &testFlag}, true},
		{"Play real purchase", production, domain.VerifiedPurchase{Platform: domain.PlatformGooglePlay, IsTestPurchase: &normalFlag}, false},
		{"Play unmarked purchase", production, domain.VerifiedPurchase{Platform: domain.PlatformGooglePlay}, false},
		{"Apple TestFlight sandbox", sandbox, domain.VerifiedPurchase{Platform: domain.PlatformAppStore}, true},
		{"Apple production", production, domain.VerifiedPurchase{Platform: domain.PlatformAppStore}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ledger.isEconomyTestPurchase(tc.purchase); got != tc.want {
				t.Fatalf("test purchase=%v want=%v", got, tc.want)
			}
		})
	}
}
