//go:build integration

package ledger

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/seorilabs/platform/server/internal/iap/refundreview"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// 환불 검토 알림부터 immutable 결정, worker lease, 종결까지 실제
// Firestore 트랜잭션과 복합 쿼리로 검증한다.
func TestRefundReviewLifecycle(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	l.WithClock(func() time.Time { return now })
	reviewID := refundreview.ReviewID(uniqueID("pending-token"))
	input := PendingRefundReviewInput{
		ReviewID: reviewID, AppID: "integration-" + strconv.FormatInt(nextIntegrationID(), 36),
		PackageName: "com.seorilabs.lizardtycoon",
		OrderIDHash: refundreview.OrderIDHash(uniqueID("order")),
		Environment: lEnvSandbox, RefundReason: 1, ReceivedAt: now,
		Secret: refundreview.Envelope{KeyID: "test", Nonce: "nonce", Ciphertext: "ciphertext"},
	}
	if err := l.RecordPendingRefundReview(ctx, input); err != nil {
		t.Fatalf("알림 기록 실패: %v", err)
	}
	if err := l.RecordPendingRefundReview(ctx, input); err != nil {
		t.Fatalf("동일 알림 멱등 재시도 실패: %v", err)
	}
	mismatch := input
	mismatch.RefundReason = 2
	if err := l.RecordPendingRefundReview(ctx, mismatch); platformerr.CodeOf(err) != platformerr.CodeRefundReviewReplayMismatch {
		t.Fatalf("알림 충돌 code=%s", platformerr.CodeOf(err))
	}

	pending, err := l.ListRefundReviews(ctx, input.AppID, RefundReviewPending, 10)
	if err != nil {
		t.Fatalf("대기열 조회 실패: %v", err)
	}
	if len(pending) != 1 || pending[0].ReviewID != reviewID || pending[0].SampleContentProvided != nil {
		t.Fatalf("대기열=%#v", pending)
	}

	decision := RefundReviewDecisionInput{
		RequestID: uuid.NewString(), ReviewID: reviewID,
		AppID: input.AppID, ExpectedEnvironment: input.Environment,
		RefundPreference: RefundPreferenceDecline, SampleContentProvided: false,
		Reason: RefundReasonVerifiedFulfillment, ActorLogin: "integration-test",
	}
	first, err := l.DecideRefundReview(ctx, decision)
	if err != nil {
		t.Fatalf("결정 실패: %v", err)
	}
	if !first.Applied || first.State != RefundReviewDecided {
		t.Fatalf("첫 결정=%#v", first)
	}
	replayed, found, err := l.FindRefundReviewDecisionReplay(ctx, decision)
	if err != nil || !found || replayed.Applied {
		t.Fatalf("결정 replay=%#v found=%v err=%v", replayed, found, err)
	}

	// requestId는 모든 Admin mutation에서 한 전역 멱등 namespace다.
	_, _, err = l.FindOperatorReplay(ctx, OperatorInput{
		RequestID: decision.RequestID, PlatformUserID: "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		EntitlementID: "sp_galaxy_gecko", ActorLogin: "integration-test",
		Reason: AdminReasonInternalValidation, AppID: input.AppID,
	}, false)
	if platformerr.CodeOf(err) != platformerr.CodeOperatorReplayMismatch {
		t.Fatalf("cross-operation collision code=%s", platformerr.CodeOf(err))
	}

	work, found, err := l.ClaimNextRefundReview(ctx)
	if err != nil || !found {
		t.Fatalf("worker claim found=%v err=%v", found, err)
	}
	if work.ReviewID != reviewID || work.RefundPreference != RefundPreferenceDecline || work.Secret.Ciphertext == "" {
		t.Fatalf("worker 항목=%#v", work)
	}
	if err := l.CompleteRefundReview(ctx, work.ReviewID, work.LeaseID); err != nil {
		t.Fatalf("worker 완료 실패: %v", err)
	}

	responded, err := l.ListRefundReviews(ctx, input.AppID, RefundReviewResponded, 10)
	if err != nil {
		t.Fatalf("응답 완료 조회 실패: %v", err)
	}
	if len(responded) != 1 || responded[0].State != RefundReviewResponded {
		t.Fatalf("응답 완료=%#v", responded)
	}
}
