package registry

import "testing"

func TestAppleSandboxRequiresExplicitProductionApp(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mutate         func(*App)
		valid, sandbox bool
	}{
		{"legacy production", func(a *App) { a.IAP.AppleSandboxEnabled = false }, true, false},
		{"explicit Apple sandbox", func(*App) {}, true, true},
		{"IAP disabled", func(a *App) { a.Features["iap"] = false }, false, false},
		{"Apple disabled", func(a *App) { a.IAP.Markets = nil }, false, false},
		{"sandbox default with extra flag", func(a *App) { a.IAP.LedgerEnvironment = LedgerSandbox }, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := validAppForTest()
			a.IAP.LedgerEnvironment = LedgerProduction
			a.IAP.Markets = []string{"app_store"}
			a.IAP.GooglePlayPackageName = ""
			a.IAP.AppStoreBundleID = "com.seorilabs.testapp"
			a.IAP.AppleSandboxEnabled = true
			tc.mutate(&a)
			if err := a.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate: %v, want valid %v", err, tc.valid)
			}
			if a.IAPEnvironmentAllowed(LedgerSandbox) != tc.sandbox || a.IAPEnvironmentAllowed("staging") || a.IAPEnvironmentAllowed("") {
				t.Fatal("environment allowlist broadened")
			}
		})
	}
}
