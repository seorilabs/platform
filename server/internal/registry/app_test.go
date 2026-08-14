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
			LedgerEnvironment:     LedgerSandbox,
			Markets:               []string{"google_play"},
			GooglePlayPackageName: "com.seorilabs.testapp",
			EntitlementIDs:        []string{"premium"},
		},
	}
}

func TestGooglePlayPackageValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*App)
		wantErr string
	}{
		{
			name:    "활성 Play 앱은 package 필수",
			mutate:  func(app *App) { app.IAP.GooglePlayPackageName = "" },
			wantErr: "google_play_package_name이 필요",
		},
		{
			name:    "잘못된 package 거부",
			mutate:  func(app *App) { app.IAP.GooglePlayPackageName = "not-a-package" },
			wantErr: "google_play_package_name이 필요",
		},
		{
			name:    "Play가 없으면 package 잔여값 거부",
			mutate:  func(app *App) { app.IAP.Markets = []string{"app_store"} },
			wantErr: "비활성인데 package name이 설정",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := validAppForTest()
			tt.mutate(&app)
			err := app.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.wantErr)
			}
		})
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
				app.IAP.GooglePlayPackageName = ""
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

func TestFirebaseCustomTokenBridgeConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*App)
		wantErr string
	}{
		{
			name: "활성 bridge는 같은 Firebase 프로젝트 SA를 허용",
			mutate: func(app *App) {
				app.Features["firebase_custom_token_bridge"] = true
				app.FirebaseCustomTokenServiceAccount = "platform-auth@test-app.iam.gserviceaccount.com"
			},
		},
		{
			name: "활성 bridge에 SA가 없으면 거부",
			mutate: func(app *App) {
				app.Features["firebase_custom_token_bridge"] = true
			},
			wantErr: "service account가 필요",
		},
		{
			name: "다른 프로젝트 SA는 거부",
			mutate: func(app *App) {
				app.Features["firebase_custom_token_bridge"] = true
				app.FirebaseCustomTokenServiceAccount = "platform-auth@other-app.iam.gserviceaccount.com"
			},
			wantErr: "Firebase 프로젝트와 다르다",
		},
		{
			name: "비활성 bridge의 잔여 SA는 거부",
			mutate: func(app *App) {
				app.FirebaseCustomTokenServiceAccount = "platform-auth@test-app.iam.gserviceaccount.com"
			},
			wantErr: "bridge가 비활성",
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

func TestValidateAppSetRejectsCrossAppAdMobUnitReuse(t *testing.T) {
	first := validAppForTest()
	first.AppID = "ads-one"
	first.FirebaseProjectID = "ads-one"
	first.Features = map[string]bool{"ads": true}
	first.IAP = IAPConfig{}
	first.Ads = rewardedAdsForTest("ca-app-pub-0000000000000000/1234567890")

	second := first
	second.AppID = "ads-two"
	second.FirebaseProjectID = "ads-two"
	second.Ads = rewardedAdsForTest("ca-app-pub-0000000000000000/1234567890")

	err := ValidateAppSet([]App{first, second})
	if err == nil || !strings.Contains(err.Error(), "앱 사이에 중복") {
		t.Fatalf("ValidateAppSet() error = %v", err)
	}
}

func TestValidateAppSetAllowsUnitReuseInsideOneApp(t *testing.T) {
	app := validAppForTest()
	app.AppID = "ads-one"
	app.FirebaseProjectID = "ads-one"
	app.Features = map[string]bool{"ads": true}
	app.IAP = IAPConfig{}
	app.Ads = rewardedAdsForTest("ca-app-pub-0000000000000000/1234567890")
	second := app.Ads.Placements[0]
	second.ID = "reward-two"
	app.Ads.Placements = append(app.Ads.Placements, second)

	if err := ValidateAppSet([]App{app}); err != nil {
		t.Fatalf("ValidateAppSet() error = %v", err)
	}
}

func rewardedAdsForTest(unit string) AdsConfig {
	return AdsConfig{
		Providers: []string{"admob"},
		Placements: []AdsPlacementConfig{{
			ID: "reward-one", Format: "rewarded", DailyLimit: 3, CooldownSeconds: 30,
			Providers: map[string]AdsProviderConfig{"admob": {AndroidAdUnitID: unit}},
			Reward:    &AdsRewardConfig{Key: "credit", MinAmount: 1, MaxAmount: 10},
		}},
	}
}
