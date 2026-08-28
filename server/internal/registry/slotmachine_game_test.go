package registry

import (
	"context"
	"os"
	"testing"
)

// slotmachine-game은 GDScript Presence 확대 대상이다. Phase A가 병합되고 앱
// 원장의 opt-in이 켜진 뒤에만 registry를 연다. 이 계약이 풀리면 앱이 요청해도
// token 발급이 비활성으로 돌아가거나, 반대로 준비되지 않은 앱에 열린다.
//
// 후보: seorilabs/slotmachine-game 756427b00253954445e232465fb7122761bc87b9
// (presenceEnabled=true, Platform GDScript SDK 0.6.6)
func TestSlotmachineGameRegistryPresenceContract(t *testing.T) {
	source := NewFSSource(os.DirFS("../../../registry"), "apps")
	apps, err := source.LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var slotmachine *App
	for i := range apps {
		if apps[i].AppID == "slotmachine-game" {
			slotmachine = &apps[i]
			break
		}
	}
	if slotmachine == nil {
		t.Fatal("slotmachine-game registry가 없다")
	}

	if !slotmachine.FeatureEnabled("presence") {
		t.Fatal("Presence 확대 후보의 presence가 활성화되지 않았다")
	}

	// Presence는 익명 세션 heartbeat만 쓴다. 기존 기능 경계를 함께 바꾸지 않는다.
	if !slotmachine.FeatureEnabled("events") {
		t.Fatal("events 기능이 꺼졌다")
	}
	if !slotmachine.FeatureEnabled("ads") {
		t.Fatal("ads 기능이 꺼졌다")
	}
	if slotmachine.FeatureEnabled("iap") {
		t.Fatal("iap 기능이 의도치 않게 켜졌다")
	}
	if slotmachine.FeatureEnabled("config") {
		t.Fatal("config 기능이 의도치 않게 켜졌다")
	}
}
