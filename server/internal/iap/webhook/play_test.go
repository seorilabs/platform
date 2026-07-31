package webhook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

const testPackageName = "com.seorilabs.lizardtycoon"

// fakeValidator는 Pub/Sub OIDC 검증을 대신한다.
type fakeValidator struct {
	email string
	err   error
	got   string
}

func (f *fakeValidator) Validate(_ context.Context, token string) (string, error) {
	f.got = token
	return f.email, f.err
}

func newPlayHandler(t *testing.T, v *fakeValidator, ver Verifier, allowed ...string) (*PlayHandler, *fakeEvents, *fakeReconciler) {
	t.Helper()

	ev := &fakeEvents{}
	rc := &fakeReconciler{res: ledger.ReconcileResult{Known: true}}

	h, err := NewPlayHandler(PlayConfig{
		Validator:     v,
		Verifier:      ver,
		Events:        ev,
		Reconciler:    rc,
		PackageName:   testPackageName,
		AllowedEmails: allowed,
	})
	if err != nil {
		t.Fatalf("핸들러 생성 실패: %v", err)
	}
	return h, ev, rc
}

// pushBody는 Pub/Sub push 본문을 만든다.
func pushBody(t *testing.T, messageID string, dn map[string]any) string {
	t.Helper()

	inner, err := json.Marshal(dn)
	if err != nil {
		t.Fatalf("알림 직렬화 실패: %v", err)
	}

	env := map[string]any{
		"message": map[string]any{
			"data":      base64.StdEncoding.EncodeToString(inner),
			"messageId": messageID,
		},
		"subscription": "projects/seorilabs-platform/subscriptions/play-iap-rtdn",
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("push 직렬화 실패: %v", err)
	}
	return string(raw)
}

func servePlay(t *testing.T, h *PlayHandler, body, authHeader string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	h.Register(mux)

	r := httptest.NewRequest(http.MethodPost, "/v1/iap/webhooks/play", strings.NewReader(body))
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func voidedNotification() map[string]any {
	return map[string]any{
		"version":         "1.0",
		"packageName":     testPackageName,
		"eventTimeMillis": "1785500000000",
		"voidedPurchaseNotification": map[string]any{
			"purchaseToken": "purchase-token-1",
			"orderId":       "GPA.1234",
			"productType":   2,
			"refundType":    refundTypeFull,
		},
	}
}

func TestPlayVoidedPurchase(t *testing.T) {
	v := &fakeValidator{email: "pubsub@seorilabs-platform.iam.gserviceaccount.com"}
	ver := &fakeVerifier{out: domain.VerifiedPurchase{
		Platform:    domain.PlatformGooglePlay,
		CanonicalID: "purchase-token-1",
		State:       domain.StateRevoked,
		ObservedAt:  hookNow,
	}}
	h, ev, rc := newPlayHandler(t, v, ver)

	w := servePlay(t, h, pushBody(t, "msg-1", voidedNotification()), "Bearer tok")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	// 멱등 키는 Pub/Sub messageId다
	if len(ev.completed) != 1 || ev.completed[0] != "msg-1" {
		t.Errorf("완료 처리 = %v", ev.completed)
	}
	// 환불 알림에는 sku가 없다. 토큰만으로 재검증해야 한다
	if ver.got.Token != "purchase-token-1" {
		t.Errorf("재검증 토큰 = %q", ver.got.Token)
	}
	if rc.call != 1 {
		t.Errorf("원장 반영 %d회", rc.call)
	}
}

// 부분 환불은 수량 기반이라 비소비성 entitlement에 대응되지 않는다.
//
// 조용히 전부 회수하면 산 것을 잃는다.
func TestPlayPartialRefundRejected(t *testing.T) {
	dn := voidedNotification()
	dn["voidedPurchaseNotification"].(map[string]any)["refundType"] = refundTypePartial

	v := &fakeValidator{email: "pubsub@x.iam.gserviceaccount.com"}
	h, ev, rc := newPlayHandler(t, v, &fakeVerifier{})

	w := servePlay(t, h, pushBody(t, "msg-partial", dn), "Bearer tok")

	// Pub/Sub은 2xx가 아니면 무조건 재전송한다. 4xx로 "버려라"를
	// 표현할 수 없어서, 처리할 수 없는 알림에도 200을 준다.
	// 실제로 422를 줬다가 같은 메시지가 무한 재전송됐다.
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (Pub/Sub은 2xx만 멈춘다)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "dropped") {
		t.Errorf("버렸다는 표시가 없다: %s", w.Body.String())
	}
	if rc.call != 0 {
		t.Error("부분 환불을 원장에 반영했다")
	}
	if len(ev.completed) != 0 {
		t.Errorf("점유하지도 않았는데 완료로 남겼다: %v", ev.completed)
	}
}

// 구독 환불 알림은 무시한다.
//
// productType은 1이 구독, 2가 일회성이다. 순서가 직관과 반대라
// 틀리기 쉽고, 실제로 1을 보내 기존 Functions가 조용히 무시하는
// 것을 본 뒤에야 우리 쪽에 이 검사가 없다는 것을 알았다.
//
// 처리하면 원장에 없는 주문이라 tombstone만 쌓이고, 나중에 구독을
// 도입할 때 그 tombstone이 신규 지급을 막는다.
func TestPlayVoidedSubscriptionIgnored(t *testing.T) {
	dn := voidedNotification()
	dn["voidedPurchaseNotification"].(map[string]any)["productType"] = productTypeSubscription

	v := &fakeValidator{email: "pubsub@x.iam.gserviceaccount.com"}
	ver := &fakeVerifier{}
	h, ev, rc := newPlayHandler(t, v, ver)

	w := servePlay(t, h, pushBody(t, "msg-sub-void", dn), "Bearer tok")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if ver.call != 0 {
		t.Error("구독 환불인데 마켓을 불렀다")
	}
	if rc.call != 0 {
		t.Error("구독 환불을 원장에 반영했다")
	}
	// 무시하되 점유는 한다. 그러지 않으면 Pub/Sub이 계속 보낸다.
	if len(ev.completed) != 1 {
		t.Errorf("완료로 남기지 않았다: %v", ev.completed)
	}
}

func TestPlayOneTimeProduct(t *testing.T) {
	dn := map[string]any{
		"packageName":     testPackageName,
		"eventTimeMillis": "1785500000000",
		"oneTimeProductNotification": map[string]any{
			"notificationType": 1,
			"purchaseToken":    "purchase-token-2",
			"sku":              "gecko_galaxy",
		},
	}

	v := &fakeValidator{email: "pubsub@x.iam.gserviceaccount.com"}
	ver := &fakeVerifier{out: domain.VerifiedPurchase{
		Platform:    domain.PlatformGooglePlay,
		CanonicalID: "purchase-token-2",
		State:       domain.StateActive,
		ObservedAt:  hookNow,
	}}
	h, _, _ := newPlayHandler(t, v, ver)

	if w := servePlay(t, h, pushBody(t, "msg-2", dn), "Bearer tok"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if ver.got.ProductID != "gecko_galaxy" {
		t.Errorf("재검증 상품 = %q", ver.got.ProductID)
	}
}

// 인증 없이 알림을 받으면 아무나 환불을 흉내 내 entitlement를 회수할 수 있다.
func TestPlayRequiresOIDCToken(t *testing.T) {
	tests := []struct {
		name       string
		authHeader string
		validator  *fakeValidator
	}{
		{"헤더 없음", "", &fakeValidator{email: "ok@x.com"}},
		{"Bearer 아님", "Basic abc", &fakeValidator{email: "ok@x.com"}},
		{"토큰이 빈 문자열", "Bearer   ", &fakeValidator{email: "ok@x.com"}},
		{"검증 실패", "Bearer bad", &fakeValidator{err: errors.New("invalid token")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ev, rc := newPlayHandler(t, tt.validator, &fakeVerifier{})

			w := servePlay(t, h, pushBody(t, "msg-x", voidedNotification()), tt.authHeader)

			if w.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", w.Code)
			}
			if rc.call != 0 || len(ev.completed) != 0 {
				t.Error("인증 실패인데 처리가 진행됐다")
			}
		})
	}
}

// 허용 목록을 지정하면 그 서비스 계정만 알림을 보낼 수 있다.
func TestPlayAllowedSenders(t *testing.T) {
	const allowed = "pubsub@seorilabs-platform.iam.gserviceaccount.com"

	t.Run("허용된 발신자", func(t *testing.T) {
		v := &fakeValidator{email: allowed}
		ver := &fakeVerifier{out: domain.VerifiedPurchase{
			Platform: domain.PlatformGooglePlay, State: domain.StateRevoked, ObservedAt: hookNow,
		}}
		h, _, _ := newPlayHandler(t, v, ver, allowed)

		if w := servePlay(t, h, pushBody(t, "m1", voidedNotification()), "Bearer t"); w.Code != http.StatusOK {
			t.Errorf("status = %d, body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("다른 발신자는 거부", func(t *testing.T) {
		v := &fakeValidator{email: "attacker@evil.example"}
		h, _, rc := newPlayHandler(t, v, &fakeVerifier{}, allowed)

		if w := servePlay(t, h, pushBody(t, "m2", voidedNotification()), "Bearer t"); w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
		if rc.call != 0 {
			t.Error("허용되지 않은 발신자의 알림을 처리했다")
		}
	})
}

// 다른 앱의 알림은 우리 원장과 무관하다.
func TestPlayRejectsOtherPackage(t *testing.T) {
	dn := voidedNotification()
	dn["packageName"] = "com.someone.else"

	v := &fakeValidator{email: "pubsub@x.com"}
	h, _, rc := newPlayHandler(t, v, &fakeVerifier{})

	w := servePlay(t, h, pushBody(t, "msg-other", dn), "Bearer t")

	// 재전송해도 결과가 같으므로 200으로 끊는다.
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if rc.call != 0 {
		t.Error("다른 앱 알림을 반영했다")
	}
}

// 우리가 다루지 않는 알림은 점유만 하고 200을 준다.
//
// 에러로 만들면 Pub/Sub이 계속 재전송한다.
func TestPlayIgnoredNotificationsSucceed(t *testing.T) {
	tests := []struct {
		name string
		dn   map[string]any
	}{
		{"테스트 알림", map[string]any{
			"packageName":      testPackageName,
			"testNotification": map[string]any{"version": "1.0"},
		}},
		{"구독 알림", map[string]any{
			"packageName": testPackageName,
			"subscriptionNotification": map[string]any{
				"notificationType": 4, "purchaseToken": "t", "subscriptionId": "s",
			},
		}},
		{"빈 알림", map[string]any{"packageName": testPackageName}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &fakeValidator{email: "pubsub@x.com"}
			ver := &fakeVerifier{}
			h, ev, _ := newPlayHandler(t, v, ver)

			w := servePlay(t, h, pushBody(t, "msg-"+tt.name, tt.dn), "Bearer t")

			if w.Code != http.StatusOK {
				t.Errorf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if ver.call != 0 {
				t.Error("다루지 않는 알림인데 마켓을 불렀다")
			}
			if len(ev.completed) != 1 {
				t.Errorf("완료로 남기지 않았다: %v", ev.completed)
			}
		})
	}
}

func TestPlayRejectsMalformedPush(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"JSON이 아니다", "쓰레기"},
		{"messageId가 없다", `{"message":{"data":"e30="}}`},
		{"data가 base64가 아니다", `{"message":{"messageId":"m","data":"!!!"}}`},
		{"data가 JSON이 아니다",
			`{"message":{"messageId":"m","data":"` +
				base64.StdEncoding.EncodeToString([]byte("not json")) + `"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &fakeValidator{email: "pubsub@x.com"}
			h, _, rc := newPlayHandler(t, v, &fakeVerifier{})

			w := servePlay(t, h, tt.body, "Bearer t")

			// 깨진 메시지를 재전송해도 계속 깨져 있다.
			// 200으로 끊지 않으면 Pub/Sub이 영원히 밀어넣는다.
			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", w.Code)
			}
			if rc.call != 0 {
				t.Error("깨진 알림을 반영했다")
			}
		})
	}
}

// data가 비면 연결 확인용 빈 메시지다. 200을 줘야 한다.
func TestPlayEmptyDataSucceeds(t *testing.T) {
	v := &fakeValidator{email: "pubsub@x.com"}
	h, ev, _ := newPlayHandler(t, v, &fakeVerifier{})

	w := servePlay(t, h, `{"message":{"messageId":"m-empty","data":""}}`, "Bearer t")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(ev.completed) != 1 {
		t.Errorf("완료로 남기지 않았다: %v", ev.completed)
	}
}

// 마켓 장애는 5xx로 알려 Pub/Sub이 재전송하게 한다.
func TestPlayRetryableFailureReturns5xx(t *testing.T) {
	v := &fakeValidator{email: "pubsub@x.com"}
	ver := &fakeVerifier{err: platformerr.New(platformerr.CodeProviderUnavailable, "Play 장애")}
	h, ev, _ := newPlayHandler(t, v, ver)

	w := servePlay(t, h, pushBody(t, "msg-retry", voidedNotification()), "Bearer t")

	if w.Code < 500 {
		t.Errorf("status = %d, want 5xx", w.Code)
	}
	if len(ev.released) != 1 {
		t.Errorf("점유를 풀지 않았다: %v", ev.released)
	}
}

func TestNewPlayHandlerValidation(t *testing.T) {
	base := PlayConfig{
		Validator:   &fakeValidator{},
		Events:      &fakeEvents{},
		Reconciler:  &fakeReconciler{},
		PackageName: testPackageName,
	}

	if _, err := NewPlayHandler(base); err != nil {
		t.Fatalf("정상 설정을 거부했다: %v", err)
	}

	t.Run("검증기 없음", func(t *testing.T) {
		cfg := base
		cfg.Validator = nil
		if _, err := NewPlayHandler(cfg); err == nil {
			t.Error("OIDC 검증기 없이 통과시켰다")
		}
	})

	t.Run("패키지 이름 없음", func(t *testing.T) {
		cfg := base
		cfg.PackageName = ""
		if _, err := NewPlayHandler(cfg); err == nil {
			t.Error("패키지 이름 없이 통과시켰다")
		}
	})
}

// Pub/Sub은 2xx가 아니면 무조건 재전송한다.
//
// Apple은 4xx를 받으면 멈추지만 Pub/Sub은 그렇지 않다. 이 차이를
// 놓쳐서 부분 환불 알림 하나가 무한 재전송됐다.
func TestPubSubRetrySemantics(t *testing.T) {
	t.Run("재시도 불가는 200으로 끊는다", func(t *testing.T) {
		v := &fakeValidator{email: "pubsub@x.com"}
		ver := &fakeVerifier{
			err: platformerr.New(platformerr.CodeProductTypeMismatch, "구독이에요"),
		}
		h, _, _ := newPlayHandler(t, v, ver)

		w := servePlay(t, h, pushBody(t, "m-perm", voidedNotification()), "Bearer t")

		if w.Code != http.StatusOK {
			t.Errorf("status = %d — 2xx가 아니면 Pub/Sub이 영원히 재전송한다", w.Code)
		}
	})

	t.Run("재시도 가능은 5xx로 다시 받는다", func(t *testing.T) {
		v := &fakeValidator{email: "pubsub@x.com"}
		ver := &fakeVerifier{
			err: platformerr.New(platformerr.CodeProviderUnavailable, "Play 장애"),
		}
		h, _, _ := newPlayHandler(t, v, ver)

		w := servePlay(t, h, pushBody(t, "m-retry", voidedNotification()), "Bearer t")

		if w.Code < 500 {
			t.Errorf("status = %d, want 5xx", w.Code)
		}
	})

	// 자격증명 문제는 운영자가 고치면 처리할 수 있다.
	// 버리면 그동안 온 환불 알림을 전부 잃는다.
	t.Run("자격증명 실패는 버리지 않는다", func(t *testing.T) {
		v := &fakeValidator{email: "pubsub@x.com"}
		ver := &fakeVerifier{
			err: platformerr.New(platformerr.CodeProviderAuthFailed, "권한 없음"),
		}
		h, ev, _ := newPlayHandler(t, v, ver)

		w := servePlay(t, h, pushBody(t, "m-auth", voidedNotification()), "Bearer t")

		if w.Code < 500 {
			t.Errorf("status = %d — 자격증명을 고치면 처리할 수 있다", w.Code)
		}
		if len(ev.completed) != 0 {
			t.Error("자격증명 실패를 완료로 남겼다. 알림이 유실된다")
		}
	})
}
