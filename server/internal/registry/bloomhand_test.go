package registry

import (
	"context"
	"os"
	"testing"
)

func TestBloomhandMobileSDKDoesNotRelayProductEvents(t *testing.T) {
	apps, err := NewFSSource(os.DirFS("../../../registry"), "apps").LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.AppID != "bloomhand" {
			continue
		}
		if !app.FeatureEnabled("events") || app.GA4.PropertyID != "555645988" || app.GA4.MeasurementID != "" {
			t.Fatalf("Bloomhand 모바일 SDK 전송 설정이 다르다: %#v", app.GA4)
		}
		for _, event := range []string{"run_start", "run_end", "purchase", "ad_impression"} {
			if app.EventAllowed(event) {
				t.Fatalf("Bloomhand 제품 이벤트 %q가 Platform에서 허용됐다", event)
			}
		}
		if !app.EventAllowed("seori_sdk_error") || !app.EventAllowed("seori_session_start") {
			t.Fatal("Platform 운영 이벤트가 허용되지 않았다")
		}
		if app.EventAllowed("email") {
			t.Fatal("등록하지 않은 이벤트가 허용됐다")
		}
		return
	}
	t.Fatal("Bloomhand registry가 없다")
}
