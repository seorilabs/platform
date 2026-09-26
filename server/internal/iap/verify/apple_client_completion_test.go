package verify

import (
	"context"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/binding"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/registry"
)

type applePolicyApps struct{ app registry.App }

func (a applePolicyApps) GetUsable(context.Context, string) (registry.App, error) { return a.app, nil }

func TestAppleClientCompletionWaitsForAppGrant(t *testing.T) {
	for _, clientCompletion := range []bool{false, true} {
		for _, account := range []string{"buyer", "other", ""} {
			for _, env := range []domain.Environment{domain.EnvProduction, domain.EnvSandbox, ""} {
				t.Run(string(env)+"/"+account+"/client="+map[bool]string{true: "true", false: "false"}[clientCompletion], func(t *testing.T) {
					keyring, err := binding.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
					if err != nil {
						t.Fatal(err)
					}
					p := activePurchase()
					p.Platform = domain.PlatformAppStore
					p.ProductID = "com.x.gems"
					p.Environment = env
					p.Completion = domain.CompletionAppleFinish
					if account != "" {
						p.PlatformAccountID = keyring.AppleAccountToken(account)
					}
					v := &fakeVerifier{platform: domain.PlatformAppStore, purchase: p}
					l := &fakeLedger{}
					outbox := &fakeOutbox{}
					cat, err := catalog.Parse([]byte(`{"version":1,"entitlements":{"gems":{"type":"consumable","app_store":"com.x.gems"}}}`), nil)
					if err != nil {
						t.Fatal(err)
					}
					apps := applePolicyApps{registry.App{AppID: "app", Features: map[string]bool{"iap": true}, IAP: registry.IAPConfig{Markets: []string{"app_store"}, EntitlementIDs: []string{"gems"}, AppStoreRequireAccountToken: true, AppStoreClientCompletion: clientCompletion}}}
					svc, err := New(Config{Verifiers: []Verifier{v}, Ledger: l, Catalog: cat, Keyring: keyring, Apps: apps, Outbox: outbox})
					if err != nil {
						t.Fatal(err)
					}
					got, err := svc.VerifyPurchase(context.Background(), "app", "buyer", domain.Proof{Platform: domain.PlatformAppStore, ProductID: p.ProductID, Token: "100001"})
					if account != "buyer" || env == "" {
						if err == nil || len(l.granted) != 0 || v.completed != 0 {
							t.Fatal("unverified buyer/environment reached grant or completion")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if got.Environment != env {
						t.Fatalf("lost verified environment: %v", got)
					}
					if clientCompletion {
						if v.completed != 0 || outbox.enqueued != 0 || got.Completion.Action != domain.ActionAppStoreFinishTransaction || got.Completion.OrderID != p.ProviderOrderID {
							t.Fatal("server finished before app wallet committed")
						}
					} else if v.completed != 1 || got.Completion.Action != domain.ActionNone {
						t.Fatal("existing completion policy changed")
					}
				})
			}
		}
	}
}
