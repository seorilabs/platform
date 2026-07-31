package webhook

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

var hookNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

// fakeEvents는 lease 저장소를 대신한다.
type fakeEvents struct {
	claim    ledger.EventClaim
	claimErr error

	completed   []string
	released    []string
	completeErr error
}

func (f *fakeEvents) ClaimEvent(_ context.Context, eventKey, _ string) (ledger.EventClaim, error) {
	if f.claimErr != nil {
		return ledger.EventClaim{}, f.claimErr
	}
	c := f.claim
	if c.EventKey == "" {
		c.EventKey = eventKey
	}
	if c.LeaseID == "" {
		c.LeaseID = "lease-1"
	}
	return c, nil
}

func (f *fakeEvents) CompleteEvent(_ context.Context, eventKey, _ string) error {
	f.completed = append(f.completed, eventKey)
	return f.completeErr
}

func (f *fakeEvents) ReleaseEvent(_ context.Context, eventKey, _ string) error {
	f.released = append(f.released, eventKey)
	return nil
}

// fakeReconciler는 원장 반영을 대신한다.
type fakeReconciler struct {
	res  ledger.ReconcileResult
	err  error
	got  domain.VerifiedPurchase
	call int
}

func (f *fakeReconciler) ReconcileByCanonicalID(
	_ context.Context, p domain.VerifiedPurchase,
) (ledger.ReconcileResult, error) {
	f.call++
	f.got = p
	return f.res, f.err
}

// fakeVerifier는 마켓 재검증을 대신한다.
type fakeVerifier struct {
	out  domain.VerifiedPurchase
	err  error
	got  domain.Proof
	call int
}

func (f *fakeVerifier) Verify(_ context.Context, proof domain.Proof) (domain.VerifiedPurchase, error) {
	f.call++
	f.got = proof
	return f.out, f.err
}

func newProcessor(ev *fakeEvents, rc *fakeReconciler) *processor {
	return &processor{
		events:     ev,
		reconciler: rc,
		now:        func() time.Time { return hookNow },
	}
}

func refundNotification() notification {
	return notification{
		EventKey:   "evt-1",
		Kind:       "REFUND",
		ObservedAt: hookNow,
		Proof: domain.Proof{
			Platform:  domain.PlatformAppStore,
			ProductID: "com.seorilabs.gecko",
			Token:     "2000000900000001",
		},
	}
}

func revokedPurchase() domain.VerifiedPurchase {
	return domain.VerifiedPurchase{
		Platform:    domain.PlatformAppStore,
		ProductID:   "com.seorilabs.gecko",
		CanonicalID: "2000000800000000",
		State:       domain.StateRevoked,
		ObservedAt:  hookNow,
	}
}

// 알림 내용을 그대로 믿지 않고 마켓에 다시 묻는다.
func TestReconcileReverifiesWithMarket(t *testing.T) {
	ev := &fakeEvents{}
	rc := &fakeReconciler{res: ledger.ReconcileResult{Known: true, PlatformUserID: "pu_1"}}
	v := &fakeVerifier{out: revokedPurchase()}

	if err := newProcessor(ev, rc).process(
		context.Background(), "app_store", refundNotification(), v); err != nil {
		t.Fatalf("처리 실패: %v", err)
	}

	if v.call != 1 {
		t.Errorf("마켓 재검증 %d회, want 1", v.call)
	}
	// 재검증은 알림에 실린 토큰으로 한다
	if v.got.Token != "2000000900000001" {
		t.Errorf("재검증 토큰 = %q", v.got.Token)
	}
	// 원장에 반영되는 것은 알림이 아니라 마켓 응답이다
	if rc.got.CanonicalID != "2000000800000000" {
		t.Errorf("반영된 canonicalId = %q, want 마켓 응답", rc.got.CanonicalID)
	}
	if len(ev.completed) != 1 {
		t.Errorf("완료 처리 %v", ev.completed)
	}
}

// 같은 알림이 두 번 와도 한 번만 처리한다.
func TestAlreadyProcessedIsNotAnError(t *testing.T) {
	ev := &fakeEvents{claim: ledger.EventClaim{AlreadyProcessed: true, LeaseID: "l"}}
	rc := &fakeReconciler{}
	v := &fakeVerifier{out: revokedPurchase()}

	// 에러를 주면 마켓이 같은 알림을 영원히 재전송한다
	if err := newProcessor(ev, rc).process(
		context.Background(), "app_store", refundNotification(), v); err != nil {
		t.Fatalf("이미 처리한 알림을 에러로 만들었다: %v", err)
	}
	if v.call != 0 || rc.call != 0 {
		t.Errorf("이미 처리한 알림을 다시 반영했다 (verify=%d, reconcile=%d)", v.call, rc.call)
	}
}

// 다른 처리자가 점유 중이면 재시도 가능한 에러가 나가야 한다.
func TestBusyEventIsRetryable(t *testing.T) {
	busy := platformerr.New(platformerr.CodeEventBusy, "처리 중이에요")
	ev := &fakeEvents{claimErr: busy}

	err := newProcessor(ev, &fakeReconciler{}).process(
		context.Background(), "app_store", refundNotification(), &fakeVerifier{})

	if !platformerr.IsRetryableErr(err) {
		t.Errorf("점유 충돌을 재시도 불가로 분류했다: %v", err)
	}
}

// 재시도해도 소용없는 실패는 완료로 남긴다.
//
// 그러지 않으면 마켓이 같은 알림을 영원히 재전송한다.
func TestPermanentFailureCompletesEvent(t *testing.T) {
	ev := &fakeEvents{}
	rc := &fakeReconciler{}
	v := &fakeVerifier{err: platformerr.New(platformerr.CodeProductTypeMismatch, "구독이에요")}

	// 마켓에는 성공을 알린다
	if err := newProcessor(ev, rc).process(
		context.Background(), "app_store", refundNotification(), v); err != nil {
		t.Fatalf("영구 실패를 마켓에 에러로 알렸다: %v", err)
	}
	if len(ev.completed) != 1 {
		t.Errorf("영구 실패를 완료로 남기지 않았다: %v", ev.completed)
	}
	if len(ev.released) != 0 {
		t.Errorf("영구 실패인데 점유를 풀었다: %v", ev.released)
	}
}

// 재시도 가치가 있는 실패는 점유를 풀고 에러를 올린다.
func TestRetryableFailureReleasesEvent(t *testing.T) {
	ev := &fakeEvents{}
	v := &fakeVerifier{err: platformerr.New(platformerr.CodeProviderUnavailable, "마켓 장애")}

	err := newProcessor(ev, &fakeReconciler{}).process(
		context.Background(), "app_store", refundNotification(), v)

	if err == nil {
		t.Fatal("재시도 가능한 실패를 성공으로 처리했다")
	}
	if len(ev.released) != 1 {
		t.Errorf("점유를 풀지 않았다: %v", ev.released)
	}
	if len(ev.completed) != 0 {
		t.Errorf("재시도 가능한데 완료로 남겼다: %v", ev.completed)
	}
}

// 마켓이 모르는 구매는 여기서 끝낸다. 환불 후 삭제된 경우가 있다.
func TestPurchaseNotFoundEndsProcessing(t *testing.T) {
	ev := &fakeEvents{}
	rc := &fakeReconciler{}
	v := &fakeVerifier{err: platformerr.New(platformerr.CodePurchaseNotFound, "없어요")}

	if err := newProcessor(ev, rc).process(
		context.Background(), "app_store", refundNotification(), v); err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if rc.call != 0 {
		t.Error("모르는 구매를 원장에 반영했다")
	}
	if len(ev.completed) != 1 {
		t.Errorf("완료로 남기지 않았다: %v", ev.completed)
	}
}

// 불변식 10. 모르는 주문은 알림만으로 지급하지 않는다.
func TestUnknownOrderIsNotGranted(t *testing.T) {
	ev := &fakeEvents{}
	rc := &fakeReconciler{res: ledger.ReconcileResult{Known: false}}

	active := revokedPurchase()
	active.State = domain.StateActive
	v := &fakeVerifier{out: active}

	// 실패가 아니다. 반영할 소유자가 없을 뿐이다.
	if err := newProcessor(ev, rc).process(
		context.Background(), "app_store", refundNotification(), v); err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if len(ev.completed) != 1 {
		t.Errorf("완료로 남기지 않았다: %v", ev.completed)
	}
}

// 재검증할 것이 없는 알림은 점유만 하고 끝낸다.
//
// 테스트 알림과 구독 알림이 여기 온다. 실패로 만들면 재전송이 멈추지 않는다.
func TestNotificationWithoutProofCompletes(t *testing.T) {
	ev := &fakeEvents{}
	rc := &fakeReconciler{}
	v := &fakeVerifier{}

	n := notification{EventKey: "evt-test", Kind: "test"}

	if err := newProcessor(ev, rc).process(
		context.Background(), "app_store", n, v); err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if v.call != 0 || rc.call != 0 {
		t.Error("재검증할 것이 없는데 마켓을 불렀다")
	}
	if len(ev.completed) != 1 {
		t.Errorf("완료로 남기지 않았다: %v", ev.completed)
	}
}

// 상품 없이 토큰만 있어도 재검증한다.
//
// Play 환불 알림에는 sku가 실려오지 않는다.
// 여기서 걸러버리면 환불이 원장에 반영되지 않는다.
func TestTokenOnlyProofIsVerified(t *testing.T) {
	ev := &fakeEvents{}
	rc := &fakeReconciler{res: ledger.ReconcileResult{Known: true}}
	v := &fakeVerifier{out: revokedPurchase()}

	n := notification{
		EventKey: "evt-void",
		Kind:     "voided_purchase",
		Proof: domain.Proof{
			Platform: domain.PlatformGooglePlay,
			Token:    "purchase-token-1",
		},
	}

	if err := newProcessor(ev, rc).process(
		context.Background(), "google_play", n, v); err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if v.call != 1 {
		t.Error("상품이 없다고 재검증을 건너뛰었다")
	}
}

// 마켓 응답에 시각이 없으면 알림 시각으로 채운다.
//
// 원장의 stale 억제가 시각에 기대므로 0값이 새면 안 된다.
func TestObservedAtFallback(t *testing.T) {
	tests := []struct {
		name         string
		purchaseTime time.Time
		notifiedAt   time.Time
		want         time.Time
	}{
		{"마켓 시각을 우선한다", hookNow.Add(-time.Hour), hookNow, hookNow.Add(-time.Hour)},
		{"없으면 알림 시각", time.Time{}, hookNow.Add(-time.Minute), hookNow.Add(-time.Minute)},
		{"둘 다 없으면 현재 시각", time.Time{}, time.Time{}, hookNow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &fakeReconciler{res: ledger.ReconcileResult{Known: true}}

			p := revokedPurchase()
			p.ObservedAt = tt.purchaseTime
			v := &fakeVerifier{out: p}

			n := refundNotification()
			n.ObservedAt = tt.notifiedAt

			if err := newProcessor(&fakeEvents{}, rc).process(
				context.Background(), "app_store", n, v); err != nil {
				t.Fatalf("처리 실패: %v", err)
			}
			if !rc.got.ObservedAt.Equal(tt.want) {
				t.Errorf("observedAt = %v, want %v", rc.got.ObservedAt, tt.want)
			}
			if rc.got.ObservedAt.IsZero() {
				t.Error("observedAt이 0값으로 새어나갔다")
			}
		})
	}
}

func TestEmptyEventKeyIsRejected(t *testing.T) {
	err := newProcessor(&fakeEvents{}, &fakeReconciler{}).process(
		context.Background(), "app_store", notification{}, &fakeVerifier{})

	if err == nil {
		t.Fatal("식별자 없는 알림을 통과시켰다")
	}
}

// 검증기가 없는 마켓의 알림은 재시도 가능한 실패다.
//
// 자격증명이 없어 조립하지 못한 상태다. 배포가 고쳐지면 처리된다.
func TestMissingVerifierIsRetryable(t *testing.T) {
	ev := &fakeEvents{}

	err := newProcessor(ev, &fakeReconciler{}).process(
		context.Background(), "apps_in_toss", refundNotification(), nil)

	if err == nil {
		t.Fatal("검증기 없이 성공했다")
	}
	if !platformerr.IsRetryableErr(err) {
		t.Errorf("재시도 불가로 분류했다: %v", err)
	}
	if len(ev.released) != 1 {
		t.Errorf("점유를 풀지 않았다: %v", ev.released)
	}
}

// 원장 반영 실패가 성공으로 둔갑하면 안 된다.
func TestReconcilerErrorPropagates(t *testing.T) {
	ev := &fakeEvents{}
	rc := &fakeReconciler{err: errors.New("firestore down")}
	v := &fakeVerifier{out: revokedPurchase()}

	if err := newProcessor(ev, rc).process(
		context.Background(), "app_store", refundNotification(), v); err == nil {
		t.Fatal("원장 실패를 성공으로 처리했다")
	}
	if len(ev.completed) != 0 {
		t.Errorf("원장 반영에 실패했는데 완료로 남겼다: %v", ev.completed)
	}
}
