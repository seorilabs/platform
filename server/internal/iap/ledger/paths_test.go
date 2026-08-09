package ledger

import (
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
)

func TestAppScopedPathsKeepLegacyPathUnchanged(t *testing.T) {
	legacy, err := newPathBuilder(domain.EnvSandbox).publicEntitlement("pu_1", "ad_free")
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := newAppPathBuilder(domain.EnvProduction, "happy-farm").publicEntitlement("pu_1", "ad_free")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := legacy.String(), "iap_environments/sandbox/users/pu_1/entitlements/ad_free"; got != want {
		t.Fatalf("legacy path = %q, want %q", got, want)
	}
	if got, want := scoped.String(), "iap_apps/happy-farm/users/pu_1/entitlements/ad_free"; got != want {
		t.Fatalf("scoped path = %q, want %q", got, want)
	}
}
