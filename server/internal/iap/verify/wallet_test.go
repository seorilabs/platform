package verify

import (
	"context"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

const starterCatalog = `{"version":2,"apps":{"starter-game":{"entitlements":{"starter_test_ticket_1":{"type":"consumable","google_play":"starter_test_ticket_1","app_store":"starter_test_ticket_1"},"starter_badge":{"type":"non_consumable","google_play":"starter_badge","app_store":"starter_badge"}}}}}`

type ticketLedger struct {
	fakeLedger
	gotPUID, gotEntitlement string
	gotUnits                int
}

func (l *ticketLedger) ContentUnitsRemaining(_ context.Context, puid, entitlement string, units int) (int, error) {
	l.gotPUID, l.gotEntitlement, l.gotUnits = puid, entitlement, units
	return 3, nil
}

func starterFixture(t *testing.T, v *fakeVerifier, l *ticketLedger) *Service {
	t.Helper()
	cat, err := catalog.Parse([]byte(starterCatalog), nil)
	if err != nil {
		t.Fatal(err)
	}
	app := registry.App{AppID: starterAppID, Features: map[string]bool{"iap": true}, IAP: registry.IAPConfig{
		LedgerEnvironment: registry.LedgerSandbox, Markets: []string{"google_play", "app_store"}, EntitlementIDs: []string{starterTicketID, "starter_badge"},
	}}
	svc, err := New(Config{Verifiers: []Verifier{v}, Ledger: l, AppLedgers: map[string]Ledger{starterAppID: l}, Catalog: cat, Apps: boxApps{app: app}})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestWalletUsesAppLedgerAndServerFixedUnits(t *testing.T) {
	l := &ticketLedger{}
	svc := starterFixture(t, &fakeVerifier{platform: domain.PlatformGooglePlay}, l)
	balances, err := svc.WalletSnapshot(context.Background(), starterAppID, "owner")
	if err != nil || balances[starterTicketID] != 3 || l.gotPUID != "owner" || l.gotEntitlement != starterTicketID || l.gotUnits != 1 {
		t.Fatalf("balances=%v ledger=%+v err=%v", balances, l, err)
	}
	if _, err := svc.WalletSnapshot(context.Background(), "other-app", "owner"); platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
		t.Fatalf("another app entered starter wallet: %v", err)
	}
	svc.appLedgers = nil
	if _, err := svc.WalletSnapshot(context.Background(), starterAppID, "owner"); platformerr.CodeOf(err) != platformerr.CodeRuntimeConfigInvalid {
		t.Fatalf("missing scoped ledger fell back to global ledger: %v", err)
	}
}

func TestStarterRejectsRealAndUnobservedPurchasesBeforeGrant(t *testing.T) {
	for _, market := range []domain.Platform{domain.PlatformGooglePlay, domain.PlatformAppStore} {
		for _, flag := range []string{"test", "real", "unknown"} {
			t.Run(string(market)+"/"+flag, func(t *testing.T) {
				purchase := activePurchase()
				purchase.Platform = market
				purchase.ProductID = starterTicketID
				purchase.Completion = domain.CompletionNone
				if flag != "unknown" {
					test := flag == "test"
					purchase.IsTestPurchase = &test
				}
				v := &fakeVerifier{platform: market, purchase: purchase}
				l := &ticketLedger{}
				svc := starterFixture(t, v, l)
				_, err := svc.VerifyPurchase(context.Background(), starterAppID, "owner", domain.Proof{Platform: market, ProductID: starterTicketID, Token: "test-proof"})
				if flag == "test" {
					if err != nil || len(l.granted) != 1 {
						t.Fatalf("test purchase not granted: %v", err)
					}
				} else if platformerr.CodeOf(err) != platformerr.CodeEnvironmentMismatch || len(l.granted) != 0 || v.completed != 0 {
					t.Fatalf("real or unknown purchase reached grant/completion: err=%v grants=%d completions=%d", err, len(l.granted), v.completed)
				}
			})
		}
	}
}
