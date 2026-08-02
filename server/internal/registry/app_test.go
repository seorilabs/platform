package registry

import (
	"strings"
	"testing"
)

func validAppForTest() App {
	return App{
		AppID:             "test-app",
		DisplayName:       "테스트 앱",
		FirebaseProjectID: "test-app",
		Status:            StatusActive,
		Features:          map[string]bool{"iap": true},
		IAP: IAPConfig{
			LedgerEnvironment: LedgerSandbox,
			EntitlementIDs:    []string{"premium"},
		},
	}
}

func TestIAPEntitlementAllowlistValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*App)
		wantErr string
	}{
		{
			name: "IAP 활성 앱은 빈 allowlist 거부",
			mutate: func(app *App) {
				app.IAP.EntitlementIDs = nil
			},
			wantErr: "entitlement_ids가 필요",
		},
		{
			name: "중복 entitlement 거부",
			mutate: func(app *App) {
				app.IAP.EntitlementIDs = []string{"premium", "premium"}
			},
			wantErr: "중복",
		},
		{
			name: "형식 밖 entitlement 거부",
			mutate: func(app *App) {
				app.IAP.EntitlementIDs = []string{"bad entitlement"}
			},
			wantErr: "올바르지 않다",
		},
		{
			name: "IAP 비활성 앱은 빈 allowlist 허용",
			mutate: func(app *App) {
				app.Features["iap"] = false
				app.IAP.EntitlementIDs = nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := validAppForTest()
			tt.mutate(&app)
			err := app.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestEntitlementAllowedIsAppScoped(t *testing.T) {
	app := validAppForTest()
	if !app.EntitlementAllowed("premium") {
		t.Fatal("앱 allowlist entitlement가 거부됐다")
	}
	if app.EntitlementAllowed("other-app-premium") {
		t.Fatal("다른 앱 entitlement가 허용됐다")
	}
}
