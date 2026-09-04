package registry

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestBabycareRegistryEventContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var babycare *App
	for i := range apps {
		if apps[i].AppID == "babycare" {
			babycare = &apps[i]
			break
		}
	}
	if babycare == nil {
		t.Fatal("babycare registry가 없다")
	}

	if !babycare.FeatureEnabled("events") ||
		!babycare.FeatureEnabled("firebase_custom_token_bridge") ||
		babycare.FeatureEnabled("config") || babycare.FeatureEnabled("iap") {
		t.Fatalf("events와 인증 브리지 외 기능이 활성화됐다: %#v", babycare.Features)
	}
	if babycare.GA4.EventPrefix != "bc_" {
		t.Fatalf("GA4 event prefix = %q, want bc_", babycare.GA4.EventPrefix)
	}

	wantAllowlist := []string{
		"seori_session_start",
		"seori_sdk_error",
		"core_screen_view",
		"core_ad_request",
		"core_ad_impression",
		"core_ad_reward",
		"bc_onboarding_complete",
		"bc_group_created",
		"bc_invite_created",
		"bc_invite_shared",
		"bc_invite_joined",
		"bc_first_log",
		"bc_log_create",
		"bc_log_update",
		"bc_log_delete",
	}
	if !reflect.DeepEqual(babycare.PlatformEventAllowlist, wantAllowlist) {
		t.Fatalf("allowlist가 다르다\n got: %#v\nwant: %#v", babycare.PlatformEventAllowlist, wantAllowlist)
	}
	if babycare.EventAllowed("bc_stats_bucket_rendered") ||
		babycare.EventAllowed("bc_sync_retry") {
		t.Fatal("고빈도 또는 운영 잡음 이벤트가 허용됐다")
	}
	if got := babycare.StripEventPrefix("bc_first_log"); got != "first_log" {
		t.Fatalf("stored event name = %q, want first_log", got)
	}
}
