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

func TestCORSOriginValidation(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
		wantErr string
	}{
		{
			name: "정확한 HTTPS origin 허용",
			origins: []string{
				"https://test-app.apps.tossmini.com",
				"https://test-app.private-apps.tossmini.com",
			},
		},
		{name: "Capacitor iOS 기본 origin 허용", origins: []string{"capacitor://localhost"}},
		{name: "경로가 있는 URL 거부", origins: []string{"https://example.com/path"}, wantErr: "올바른 origin"},
		{name: "임의 custom scheme 거부", origins: []string{"ungeul://localhost"}, wantErr: "올바른 origin"},
		{name: "Capacitor 임의 host 거부", origins: []string{"capacitor://attacker"}, wantErr: "올바른 origin"},
		{name: "중복 거부", origins: []string{"https://example.com", "https://example.com"}, wantErr: "중복"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := validAppForTest()
			app.CORSOrigins = tt.origins
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

func TestAccountProviderAndLinkedIAPValidation(t *testing.T) {
	validAccountApp := func() App {
		app := validAppForTest()
		app.Features["firebase_custom_token_bridge"] = true
		app.FirebaseCustomTokenServiceAccount = "platform-auth@test-app.iam.gserviceaccount.com"
		app.RequireAppCheck = true
		app.Auth.AccountProviders = map[string]AuthProviderConfig{
			"kakao":  {Audience: "123456789"},
			"apple":  {Audience: "com.seorilabs.testapp"},
			"google": {Audience: "123456789-web.apps.googleusercontent.com"},
		}
		app.IAP.RequireLinkedAccount = true
		return app
	}
	if err := validAccountApp().Validate(); err != nil {
		t.Fatalf("valid account config: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*App)
		wantErr string
	}{
		{
			name: "App Check 없는 provider 거부",
			mutate: func(app *App) {
				app.RequireAppCheck = false
			},
			wantErr: "App Check",
		},
		{
			name: "지원하지 않는 provider 거부",
			mutate: func(app *App) {
				app.Auth.AccountProviders["naver"] = AuthProviderConfig{Audience: "naver-client"}
			},
			wantErr: "지원하지 않는 auth provider",
		},
		{
			name: "비공개 값처럼 보이는 audience 거부",
			mutate: func(app *App) {
				app.Auth.AccountProviders["kakao"] = AuthProviderConfig{Audience: "https://secret.example/client"}
			},
			wantErr: "auth audience",
		},
		{
			name: "연결 계정 정책에 provider 필수",
			mutate: func(app *App) {
				app.Auth.AccountProviders = nil
			},
			wantErr: "연결 계정 필수 IAP",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := validAccountApp()
			tc.mutate(&app)
			err := app.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestContentConfigValidation(t *testing.T) {
	validContentApp := func() App {
		app := validAppForTest()
		app.Features["content"] = true
		app.Features["firebase_custom_token_bridge"] = true
		app.FirebaseCustomTokenServiceAccount = "platform-auth@test-app.iam.gserviceaccount.com"
		app.RequireAppCheck = true
		app.Content = ContentConfig{
			Bucket: "seorilabs-private-content", Prefix: "ungeul",
			ReadingDailyLimit: 10, TermDailyLimit: 100,
			TicketEntitlementID: "premium", TicketUnitsPerPurchase: 5,
			SeasonEntitlements: map[string]string{"2026": "premium"},
		}
		return app
	}

	if err := validContentApp().Validate(); err != nil {
		t.Fatalf("valid content config: %v", err)
	}

	app := validContentApp()
	app.RequireAppCheck = false
	if err := app.Validate(); err == nil || !strings.Contains(err.Error(), "App Check") {
		t.Fatalf("App Check 없는 content error = %v", err)
	}

	app = validContentApp()
	app.Content.Prefix = "production/../ungeul"
	if err := app.Validate(); err == nil || !strings.Contains(err.Error(), "content.prefix") {
		t.Fatalf("위험한 prefix error = %v", err)
	}

	for _, prefix := range []string{"production", "production/ungeul", "staging", "staging/ungeul"} {
		app = validContentApp()
		app.Content.Prefix = prefix
		if err := app.Validate(); err == nil || !strings.Contains(err.Error(), "content.prefix") {
			t.Fatalf("환경을 포함한 prefix %q error = %v", prefix, err)
		}
	}

	app = validContentApp()
	app.Content.PairingEnabled = true
	if err := app.Validate(); err != nil {
		t.Fatalf("pairing_enabled 있는 content config: %v", err)
	}

	// 궁합 킬 스위치도 content 설정이다. content가 꺼진 앱에 남아 있으면 배선 실수다.
	app = validAppForTest()
	app.Content = ContentConfig{PairingEnabled: true}
	if err := app.Validate(); err == nil || !strings.Contains(err.Error(), "content가 비활성") {
		t.Fatalf("content 비활성 앱의 pairing_enabled error = %v", err)
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

func TestAdsRequestCooldownBounds(t *testing.T) {
	for _, seconds := range []int{-1, 0, 30, 86400, 86401} {
		app := validAppForTest()
		app.Features["ads"] = true
		app.Ads = rewardedAdsForTest("ca-app-pub-1111111111111111/1111111111")
		app.Ads.Placements[0].RequestCooldownSeconds = seconds
		err := app.Validate()
		wantValid := seconds >= 0 && seconds <= 86400
		if (err == nil) != wantValid {
			t.Fatalf("seconds=%d valid=%v err=%v", seconds, wantValid, err)
		}
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

// 스토어 주소는 강제 업데이트 화면이 유저를 보내는 유일한 목적지다.
// 오타 하나가 유저를 엉뚱한 앱으로 보낸다.
func TestValidateStore(t *testing.T) {
	const playURL = "https://play.google.com/store/apps/details?id=com.seorilabs.happyfarm"
	const appStoreURL = "https://apps.apple.com/kr/app/happy-farm/id1234567890"

	base := func() App {
		return App{
			AppID:             "happy-farm",
			DisplayName:       "해피 팜",
			FirebaseProjectID: "happy-farm-tycoon",
			Status:            StatusActive,
			Features:          map[string]bool{},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*App)
		wantErr bool
	}{
		{
			name:   "둘 다 비어 있으면 통과한다",
			mutate: func(a *App) {},
		},
		{
			name:   "정상 주소",
			mutate: func(a *App) { a.Store = StoreConfig{GooglePlayURL: playURL, AppStoreURL: appStoreURL} },
		},
		{
			name: "국가 코드와 앱 이름이 없는 App Store 주소도 받는다",
			mutate: func(a *App) {
				a.Store.AppStoreURL = "https://apps.apple.com/app/id1234567890"
			},
		},
		{
			name:    "http는 거부한다",
			mutate:  func(a *App) { a.Store.GooglePlayURL = "http://play.google.com/store/apps/details?id=com.a.b" },
			wantErr: true,
		},
		{
			name:    "추적 파라미터가 붙으면 거부한다",
			mutate:  func(a *App) { a.Store.GooglePlayURL = playURL + "&hl=ko" },
			wantErr: true,
		},
		{
			name:    "다른 호스트는 거부한다",
			mutate:  func(a *App) { a.Store.GooglePlayURL = "https://example.com/store/apps/details?id=com.a.b" },
			wantErr: true,
		},
		{
			name:    "placeholder는 거부한다",
			mutate:  func(a *App) { a.Store.AppStoreURL = "확정 필요" },
			wantErr: true,
		},
		{
			name:    "App Store 숫자 ID가 없으면 거부한다",
			mutate:  func(a *App) { a.Store.AppStoreURL = "https://apps.apple.com/kr/app/happy-farm" },
			wantErr: true,
		},
		{
			name: "Play 주소가 IAP 패키지명과 다르면 거부한다",
			mutate: func(a *App) {
				a.Features = map[string]bool{"iap": true}
				a.IAP = IAPConfig{
					LedgerEnvironment:     LedgerProduction,
					Markets:               []string{"google_play"},
					GooglePlayPackageName: "com.seorilabs.other",
					EntitlementIDs:        []string{"coin_pack"},
				}
				a.Store.GooglePlayURL = playURL
			},
			wantErr: true,
		},
		{
			name: "Play 주소가 IAP 패키지명과 같으면 통과한다",
			mutate: func(a *App) {
				a.Features = map[string]bool{"iap": true}
				a.IAP = IAPConfig{
					LedgerEnvironment:     LedgerProduction,
					Markets:               []string{"google_play"},
					GooglePlayPackageName: "com.seorilabs.happyfarm",
					EntitlementIDs:        []string{"coin_pack"},
				}
				a.Store.GooglePlayURL = playURL
			},
		},
		{
			name: "무과금 앱도 스토어 주소를 가질 수 있다",
			mutate: func(a *App) {
				a.Store.GooglePlayURL = playURL
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := base()
			tt.mutate(&app)
			err := app.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("에러를 기대했는데 통과했다")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("통과를 기대했는데 실패했다: %v", err)
			}
		})
	}
}

func TestUpdateURL(t *testing.T) {
	app := App{Store: StoreConfig{
		GooglePlayURL: "https://play.google.com/store/apps/details?id=com.a.b",
		AppStoreURL:   "https://apps.apple.com/app/id1234567890",
	}}

	if got := app.UpdateURL("android"); got != app.Store.GooglePlayURL {
		t.Errorf("android = %q", got)
	}
	if got := app.UpdateURL("ios"); got != app.Store.AppStoreURL {
		t.Errorf("ios = %q", got)
	}
	// 설치본이 없는 플랫폼에는 보낼 곳이 없다.
	for _, platform := range []string{"ait", "web", "", "windows"} {
		if got := app.UpdateURL(platform); got != "" {
			t.Errorf("UpdateURL(%q) = %q, want 빈 문자열", platform, got)
		}
	}
}

// 은퇴 unit은 SSV 대조를 일부러 느슨하게 만드는 값이다. 느슨해지는 것은 의도지만
// 모호해지면 안 된다. 잘못된 목록이 파일 단계에서 걸리는지 본다.
func TestValidateRetiredAdMobUnits(t *testing.T) {
	const current = "ca-app-pub-1111111111111111/1111111111"
	for _, tc := range []struct {
		name    string
		mutate  func(*AdsProviderConfig)
		wantErr string
	}{
		{
			name: "정상 전환",
			mutate: func(c *AdsProviderConfig) {
				c.RetiredAndroidAdUnitIDs = []string{"ca-app-pub-0000000000000000/1234567890"}
			},
		},
		{
			name:    "형식 오류",
			mutate:  func(c *AdsProviderConfig) { c.RetiredAndroidAdUnitIDs = []string{"1234567890"} },
			wantErr: "은퇴 AdMob unit이 올바르지 않다",
		},
		{
			name:    "현재 unit과 중복",
			mutate:  func(c *AdsProviderConfig) { c.RetiredAndroidAdUnitIDs = []string{current} },
			wantErr: "겹친다",
		},
		{
			name: "목록 안 중복",
			mutate: func(c *AdsProviderConfig) {
				c.RetiredAndroidAdUnitIDs = []string{
					"ca-app-pub-0000000000000000/1234567890",
					"ca-app-pub-2222222222222222/1234567890",
				}
			},
			wantErr: "겹친다",
		},
		{
			name: "현재 unit 없이 은퇴 unit만",
			mutate: func(c *AdsProviderConfig) {
				c.AndroidAdUnitID = ""
				c.IOSAdUnitID = "ca-app-pub-1111111111111111/2222222222"
				c.RetiredAndroidAdUnitIDs = []string{"ca-app-pub-0000000000000000/1234567890"}
			},
			wantErr: "현재 unit이 있을 때만",
		},
		{
			name: "상한 초과",
			mutate: func(c *AdsProviderConfig) {
				c.RetiredAndroidAdUnitIDs = []string{
					"ca-app-pub-0000000000000000/1000000001",
					"ca-app-pub-0000000000000000/1000000002",
					"ca-app-pub-0000000000000000/1000000003",
					"ca-app-pub-0000000000000000/1000000004",
					"ca-app-pub-0000000000000000/1000000005",
				}
			},
			wantErr: "넘을 수 없다",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := validAppForTest()
			app.Features["ads"] = true
			app.Ads = rewardedAdsForTest(current)
			cfg := app.Ads.Placements[0].Providers["admob"]
			tc.mutate(&cfg)
			app.Ads.Placements[0].Providers["admob"] = cfg

			err := app.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

// 은퇴 unit으로도 실제 콜백이 들어오므로 앱 사이 귀속 경계는 현재 unit과 같아야 한다.
// 다른 앱이 이 unit을 현재 unit으로 들고 있으면 콜백이 어느 앱 것인지 모호해진다.
func TestValidateAppSetCountsRetiredAdMobUnits(t *testing.T) {
	const retired = "ca-app-pub-0000000000000000/1234567890"

	first := validAppForTest()
	first.AppID = "ads-one"
	first.FirebaseProjectID = "ads-one"
	first.Features = map[string]bool{"ads": true}
	first.IAP = IAPConfig{}
	first.Ads = rewardedAdsForTest("ca-app-pub-1111111111111111/1111111111")
	cfg := first.Ads.Placements[0].Providers["admob"]
	cfg.RetiredAndroidAdUnitIDs = []string{retired}
	first.Ads.Placements[0].Providers["admob"] = cfg

	second := validAppForTest()
	second.AppID = "ads-two"
	second.FirebaseProjectID = "ads-two"
	second.Features = map[string]bool{"ads": true}
	second.IAP = IAPConfig{}
	second.Ads = rewardedAdsForTest(retired)

	err := ValidateAppSet([]App{first, second})
	if err == nil || !strings.Contains(err.Error(), "앱 사이에 중복") {
		t.Fatalf("ValidateAppSet() error = %v", err)
	}
}

func TestAccountDeletionFeatureValidation(t *testing.T) {
	deletionApp := func() App {
		app := validAppForTest()
		app.Features["firebase_custom_token_bridge"] = true
		app.Features["account_deletion"] = true
		app.FirebaseCustomTokenServiceAccount = "platform-auth@test-app.iam.gserviceaccount.com"
		app.GA4.PropertyID = "123456789"
		return app
	}
	tests := []struct {
		name    string
		mutate  func(*App)
		wantErr string
	}{
		{
			// 삭제 워커는 IAP 원장을 보존하므로 IAP 앱도 게스트 계정 삭제를 쓸 수 있다.
			name:   "IAP 앱의 게스트 계정 삭제 허용",
			mutate: func(app *App) {},
		},
		{
			name:   "IAP 없는 게스트 앱 허용",
			mutate: func(app *App) { app.Features["iap"] = false; app.IAP = IAPConfig{} },
		},
		{
			name: "bridge 없으면 거부",
			mutate: func(app *App) {
				app.Features["firebase_custom_token_bridge"] = false
				app.FirebaseCustomTokenServiceAccount = ""
			},
			wantErr: "account deletion supports Firebase guest apps",
		},
		{
			name:    "content 앱 거부",
			mutate:  func(app *App) { app.Features["content"] = true },
			wantErr: "account deletion supports Firebase guest apps",
		},
		{
			name: "외부 계정 연결 앱 거부",
			mutate: func(app *App) {
				app.Auth.AccountProviders = map[string]AuthProviderConfig{"kakao": {Audience: "kakao-app"}}
			},
			wantErr: "account deletion supports Firebase guest apps",
		},
		{
			name:    "GA4 속성 없으면 거부",
			mutate:  func(app *App) { app.GA4.PropertyID = "" },
			wantErr: "needs a GA4 property",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := deletionApp()
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
