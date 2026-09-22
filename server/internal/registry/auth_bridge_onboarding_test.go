package registry

import (
	"context"
	"os"
	"reflect"
	"testing"
)

// 신규 앱은 인증 브리지만 platform으로 넘긴다. App Check, 이벤트, 결제, 광고는
// 각 앱 클라이언트와 실기기 검증이 끝난 뒤 별도 변경으로 활성화한다.
func TestAuthBridgeOnboardingRegistryContract(t *testing.T) {
	tests := []struct {
		appID             string
		displayName       string
		firebaseProjectID string
		serviceAccount    string
		corsOrigins       []string
	}{
		{
			appID:             "foam-party",
			displayName:       "버블 버블 거품 세차",
			firebaseProjectID: "foam-party",
			serviceAccount:    "platform-auth@foam-party.iam.gserviceaccount.com",
			// AppsInToss web-framework 3.x는 미니앱을 *.web.tossmini.com 에서 서빙한다.
			// 앱은 이미 3.x 번들로 나갔고 아직 Platform API를 부르지 않지만, 호출을
			// 붙이는 시점에 preflight가 막히지 않도록 미리 등록해 둔다. 2.x 호스트는
			// 롤백 여지가 남아 있는 동안 함께 유지한다.
			corsOrigins: []string{
				"https://foam-party.apps.tossmini.com",
				"https://foam-party.private-apps.tossmini.com",
				"https://foam-party.web.tossmini.com",
				"https://foam-party.private-web.tossmini.com",
			},
		},
		{
			appID:             "match-picture-app",
			displayName:       "같은그림찾기",
			firebaseProjectID: "match-picture-app",
			serviceAccount:    "platform-auth@match-picture-app.iam.gserviceaccount.com",
			corsOrigins: []string{
				"http://localhost:5173",
				"capacitor://localhost",
				"https://match-picture-app.apps.tossmini.com",
				"https://match-picture-app.private-apps.tossmini.com",
			},
		},
		{
			appID:             "lucid-reversi",
			displayName:       "루시드 리버시",
			firebaseProjectID: "lucid-reversi",
			serviceAccount:    "platform-auth@lucid-reversi.iam.gserviceaccount.com",
			corsOrigins: []string{
				"https://lucid-reversi.apps.tossmini.com",
				"https://lucid-reversi.private-apps.tossmini.com",
			},
		},
	}

	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*App, len(apps))
	for i := range apps {
		byID[apps[i].AppID] = &apps[i]
	}

	for _, tt := range tests {
		t.Run(tt.appID, func(t *testing.T) {
			app := byID[tt.appID]
			if app == nil {
				t.Fatalf("%s registry가 없다", tt.appID)
			}
			if err := app.Validate(); err != nil {
				t.Fatalf("레지스트리 항목이 유효하지 않다: %v", err)
			}
			if app.DisplayName != tt.displayName || app.FirebaseProjectID != tt.firebaseProjectID ||
				app.FirebaseCustomTokenServiceAccount != tt.serviceAccount {
				t.Fatalf("앱 또는 Firebase 계약이 다르다: %#v", app)
			}
			if !app.FeatureEnabled("firebase_custom_token_bridge") {
				t.Fatal("인증 브리지가 비활성이다")
			}
			for _, feature := range []string{"config", "events", "iap", "ads"} {
				if app.FeatureEnabled(feature) {
					t.Fatalf("%s 기능이 활성화됐다: %#v", feature, app.Features)
				}
			}
			if app.RequireAppCheck {
				t.Fatal("클라이언트 검증 전에 App Check가 강제됐다")
			}
			if len(app.PlatformEventAllowlist) != 0 {
				t.Fatalf("events가 비활성인데 allowlist가 있다: %#v", app.PlatformEventAllowlist)
			}
			if !reflect.DeepEqual(app.CORSOrigins, tt.corsOrigins) {
				t.Fatalf("클라이언트 origin이 다르다\n got: %#v\nwant: %#v", app.CORSOrigins, tt.corsOrigins)
			}
		})
	}
}
