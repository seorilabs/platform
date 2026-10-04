package registry

import (
	"context"
	"os"
	"reflect"
	"testing"
)

// 블룸핸드 친구 상자(ADR 0033)는 게스트도 사고 열 수 있고,
// 계정 연결은 App Check 를 요구한다. 상자 카탈로그 상품과 결제 경계가 어긋나면 지급이 막힌다.
func TestBloomhandIAPAllowsGuestWithOptionalAppCheckedAccountLink(t *testing.T) {
	apps, err := NewFSSource(os.DirFS("../../../registry"), "apps").LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.AppID != "bloomhand" {
			continue
		}
		if !app.FeatureEnabled("iap") || app.IAP.RequireLinkedAccount || !app.Auth.RequireAccountLinkAppCheck {
			t.Fatalf("블룸핸드 결제·계정 연결 경계가 다르다: %#v %#v", app.IAP, app.Auth)
		}
		if got := app.Auth.AccountProviders["apple"].Audience; got != "com.seorilabs.bloomhand" {
			t.Fatalf("Apple audience = %q", got)
		}
		if got := app.Auth.AccountProviders["google"].Audience; got != "1033323141138-rlk0felp5pt0hp79vrd28o9v58e9aoct.apps.googleusercontent.com" {
			t.Fatalf("Google audience = %q", got)
		}
		if !reflect.DeepEqual(app.IAP.EntitlementIDs, []string{"friend_box_1", "friend_box_5"}) {
			t.Fatalf("상자 entitlement = %v", app.IAP.EntitlementIDs)
		}
		if !reflect.DeepEqual(app.IAP.Markets, []string{"google_play", "app_store"}) || app.IAP.LegacyUnscopedLedger {
			t.Fatalf("마켓·원장 경로가 다르다: %#v", app.IAP)
		}
		return
	}
	t.Fatal("Bloomhand registry가 없다")
}
