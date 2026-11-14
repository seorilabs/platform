package verify

import (
	"context"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type mailboxLedger struct {
	fakeLedger
	users map[string][]string
}

func (l *mailboxLedger) ListActive(_ context.Context, puid string) ([]string, error) {
	return l.users[puid], nil
}

func TestInboxOnlyEntitlementsAreScopedAndReadOnly(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Parse([]byte(catalogJSON), nil)
	if err != nil {
		t.Fatal(err)
	}
	app := registry.App{AppID: "mail-app", Features: map[string]bool{"inbox": true, "iap": false}}
	scoped := &mailboxLedger{users: map[string][]string{"user-a": {"neon_garden"}}}
	svc, err := New(Config{Ledger: &fakeLedger{active: []string{"another-app"}}, AppLedgers: map[string]Ledger{"mail-app": scoped}, Catalog: cat, Apps: applePolicyApps{app: app}})
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListEntitlementsForApp(ctx, "mail-app", "user-a")
	if err != nil || len(list) != 1 || list[0] != "neon_garden" {
		t.Fatalf("mail grant not restored: %v %v", list, err)
	}
	list, err = svc.ListEntitlementsForApp(ctx, "mail-app", "user-b")
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("other user rights leaked: %v %v", list, err)
	}
	delete(scoped.users, "user-a")
	list, err = svc.ListEntitlementsForApp(ctx, "mail-app", "user-a")
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("revoked mail grant retained: %v %v", list, err)
	}
	_, err = svc.VerifyPurchase(ctx, "mail-app", "user-a", domain.Proof{Platform: domain.PlatformGooglePlay, ProductID: "premium"})
	if platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
		t.Fatalf("mail enabled purchase: %v", err)
	}
	if len(scoped.granted) != 0 {
		t.Fatal("read granted a purchase")
	}
	_, _, err = svc.AccountReferencesForApp(ctx, "mail-app", "user-a")
	if platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
		t.Fatalf("mail enabled market account reference: %v", err)
	}
}

func TestInboxOnlyMissingLedgerCannotFallBackToAnotherApp(t *testing.T) {
	cat, err := catalog.Parse([]byte(catalogJSON), nil)
	if err != nil {
		t.Fatal(err)
	}
	app := registry.App{AppID: "mail-app", Features: map[string]bool{"inbox": true, "iap": false}}
	svc, err := New(Config{Ledger: &fakeLedger{active: []string{"another-app"}}, Catalog: cat, Apps: applePolicyApps{app: app}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ListEntitlementsForApp(context.Background(), "mail-app", "user-a")
	if platformerr.CodeOf(err) != platformerr.CodeRuntimeConfigInvalid {
		t.Fatalf("unscoped fallback or wrong error: %v", err)
	}
	app.Features["inbox"] = false
	_, err = svc.ListEntitlementsForApp(context.Background(), "mail-app", "user-a")
	if platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
		t.Fatalf("unadopted service exposed: %v", err)
	}
}
