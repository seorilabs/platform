package operational

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSenderSignsSafeEnvelope(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		timestamp := r.Header.Get("X-Seori-Timestamp")
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(timestamp + "."))
		_, _ = mac.Write(body)
		if got, want := r.Header.Get("X-Seori-Signature"), "v1="+hex.EncodeToString(mac.Sum(nil)); got != want {
			t.Fatalf("signature=%q want=%q", got, want)
		}
		var envelope map[string]any
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["version"] != float64(1) || envelope["appId"] != "happy-farm" {
			t.Fatalf("envelope=%v", envelope)
		}
		if _, found := envelope["platformUserId"]; found {
			t.Fatal("platformUserId must not be sent")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	sender, err := NewSender(server.URL, secret, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	sender.now = func() time.Time { return now }
	err = sender.Send(context.Background(), Event{
		EventID:    StableEventID("identity", "happy-farm", "pu_sensitive"),
		OccurredAt: now, Type: "identity.created", AppID: "happy-farm", Outcome: "created",
		Attributes: map[string]any{"authType": "firebase"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStableEventIDDoesNotExposeInput(t *testing.T) {
	id := StableEventID("identity", "happy-farm", "pu_sensitive")
	if id == "" || id == "identity_happy-farm_pu_sensitive" {
		t.Fatalf("unsafe id=%q", id)
	}
	if got := StableEventID("identity", "happy-farm", "pu_sensitive"); got != id {
		t.Fatalf("unstable id=%q got=%q", id, got)
	}
}

func TestEventContractRejectsPIIAndRawIdentifiers(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	safe := Event{
		EventID:    StableEventID("identity", "happy-farm", "pu_sensitive"),
		OccurredAt: now, Type: "identity.created", AppID: "happy-farm", Outcome: "created",
		Attributes: map[string]any{"authType": "firebase", "anonymous": false},
	}
	if err := validateEvent(safe); err != nil {
		t.Fatalf("safe event rejected: %v", err)
	}
	withReferrer := safe
	withReferrer.Attributes = map[string]any{"authType": "apps_in_toss", "referrer": "SANDBOX"}
	if err := validateEvent(withReferrer); err != nil {
		t.Fatalf("referrer attribute를 거부했다: %v", err)
	}
	unsafeAttribute := safe
	unsafeAttribute.Attributes = map[string]any{"platformUserId": "pu_sensitive"}
	if err := validateEvent(unsafeAttribute); err == nil {
		t.Fatal("platformUserId attribute를 허용했다")
	}
	unsafeID := safe
	unsafeID.EventID = "identity_pu_sensitive"
	if err := validateEvent(unsafeID); err == nil {
		t.Fatal("원본 식별자가 드러나는 event ID를 허용했다")
	}
}

func TestIdentityEventContractAcceptsClientBuild(t *testing.T) {
	// 신규 가입과 outbox 검증은 같은 트랜잭션이다. SDK가 보내는 빌드 정보가
	// 이 계약에서 빠지면 정상 자격증명도 500으로 끝나고 사용자가 생성되지 않는다.
	for _, runtime := range []string{"godot-native-android", "godot-native-ios", "ait-web", "web"} {
		t.Run(runtime, func(t *testing.T) {
			event := Event{
				EventID:    StableEventID("identity", "lizard-tycoon", "test-user"),
				OccurredAt: time.Date(2026, 9, 7, 3, 47, 0, 0, time.UTC),
				Type:       "identity.created", AppID: "lizard-tycoon", Outcome: "created",
				Attributes: map[string]any{
					"authType": "firebase", "signInProvider": "anonymous", "anonymous": true,
					"appVersion": "1.4.1", "runtime": runtime,
				},
			}
			if err := validateEvent(event); err != nil {
				t.Fatalf("정상 신규 계정의 빌드 정보를 거부했다: %v", err)
			}
			for _, key := range []string{"appVersion", "runtime"} {
				original := event.Attributes[key]
				event.Attributes[key] = strings.Repeat("x", 121)
				if err := validateEvent(event); err == nil {
					t.Fatalf("%s의 문자열 상한을 넘겼다", key)
				}
				event.Attributes[key] = original
			}
			event.Attributes["platformUserId"] = "test-user"
			if err := validateEvent(event); err == nil {
				t.Fatal("빌드 정보와 함께 원본 식별자까지 허용했다")
			}
		})
	}
}

func iapGrantEvent(attributes map[string]any) Event {
	return Event{
		EventID:    StableEventID("iap", "lizard-tycoon", "order-1", "test-user"),
		OccurredAt: time.Date(2026, 9, 16, 12, 40, 55, 0, time.UTC),
		Type:       "iap.granted", AppID: "lizard-tycoon", Outcome: "granted",
		Attributes: attributes,
	}
}

func TestIAPGrantContractAcceptsTestPurchaseFlag(t *testing.T) {
	// 지급과 outbox 검증은 같은 트랜잭션이다. 이 계약이 값을 거부하면 사용자가
	// 돈을 내고 물건을 못 받는다.
	for _, flag := range []bool{true, false} {
		event := iapGrantEvent(map[string]any{
			"platform": "app_store", "entitlementId": "sp_aurora_skink", "isTestPurchase": flag,
		})
		if err := validateEvent(event); err != nil {
			t.Fatalf("isTestPurchase=%v를 거부했다: %v", flag, err)
		}
	}

	// AppsInToss는 provider가 이 값을 만들지 않아 키가 아예 오지 않는다.
	// 미확인이 지급을 막으면 앱인토스 결제가 통째로 롤백된다.
	unknown := iapGrantEvent(map[string]any{
		"platform": "apps_in_toss", "entitlementId": "ft_rack_pack_1",
	})
	if err := validateEvent(unknown); err != nil {
		t.Fatalf("미확인 경로를 거부했다: %v", err)
	}

	// 계약 밖 키는 계속 거부한다.
	extra := iapGrantEvent(map[string]any{
		"platform": "app_store", "entitlementId": "sp_aurora_skink", "productId": "sku_1",
	})
	if err := validateEvent(extra); err == nil {
		t.Fatal("계약 밖 attribute를 허용했다")
	}
}

func TestIAPGrantContractRejectsPointerAttribute(t *testing.T) {
	// safeScalar의 type switch에는 포인터 case가 없다. `case nil`은 인터페이스 자체가
	// nil일 때만 맞으므로 *bool은 nil이든 아니든 default로 떨어져 false를 돌려준다.
	//
	// 즉 호출부가 역참조를 빼먹으면 지급이 통째로 롤백된다. 이 저장소에서 가장 비싼
	// 실패라, 계약이 포인터를 확실히 막는다는 사실을 여기 고정한다.
	flag := true
	for name, value := range map[string]any{
		"typed nil": (*bool)(nil),
		"non-nil":   &flag,
	} {
		event := iapGrantEvent(map[string]any{
			"platform": "app_store", "entitlementId": "sp_aurora_skink", "isTestPurchase": value,
		})
		if err := validateEvent(event); err == nil {
			t.Fatalf("%s 포인터를 그대로 허용했다 — 역참조 누락이 배포까지 간다", name)
		}
	}
}
