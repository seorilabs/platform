// Package webhook은 마켓 알림을 받는다.
//
// 알림 처리에는 공통 골격이 있다.
//
//	파싱 → lease 점유 → 마켓 재검증 → 원장 반영 → lease 완료
//
// 알림 내용을 그대로 믿지 않고 마켓에 다시 물어보는 것이 핵심이다.
// 알림은 "무엇이 바뀌었는지" 알려주는 신호일 뿐이고, 진실은 마켓에 있다.
// 재검증하면 알림이 지연·재정렬되어 도착해도 최종 상태가 맞는다.
package webhook

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// Events는 알림 멱등성을 관리한다.
//
// 소비자인 이 패키지가 인터페이스를 정의한다. ledger.Ledger가 구현한다.
type Events interface {
	ClaimEvent(ctx context.Context, eventKey, provider string) (ledger.EventClaim, error)
	CompleteEvent(ctx context.Context, eventKey, leaseID string) error
	ReleaseEvent(ctx context.Context, eventKey, leaseID string) error
}

// Reconciler는 알림을 원장에 반영한다.
type Reconciler interface {
	ReconcileByCanonicalID(ctx context.Context, p domain.VerifiedPurchase) (ledger.ReconcileResult, error)
}

// Verifier는 마켓에 구매를 재확인한다.
//
// providers의 검증기가 그대로 구현한다.
type Verifier interface {
	Verify(ctx context.Context, proof domain.Proof) (domain.VerifiedPurchase, error)
}

// Auditor는 감사 원장에 기록한다.
type Auditor interface {
	Record(ctx context.Context, action, appID, puid, outcome string, detail map[string]any)
}

// notification은 파싱된 알림이다.
//
// 마켓마다 형식이 달라도 여기까지 오면 같은 모양이다.
type notification struct {
	// EventKey는 멱등 키다. Apple은 notificationUUID, Play는 messageId다.
	EventKey string
	// Kind는 알림 종류다. 로그와 감사에만 쓴다.
	Kind string
	// Proof가 비어 있으면 재검증할 것이 없는 알림이다.
	//
	// 구독 관련이나 테스트 알림이 여기 해당한다. 점유만 하고 완료한다.
	Proof domain.Proof
	// ObservedAt은 마켓이 알림을 만든 시각이다.
	ObservedAt time.Time
}

// hasProof는 재검증할 구매가 있는지 본다.
//
// 상품은 없어도 된다. Play 환불 알림에는 sku가 실려오지 않는다.
// 마켓 조회는 토큰만으로 되고 상품은 응답에서 온다.
func (n notification) hasProof() bool {
	return n.Proof.Token != ""
}

// processor는 알림 하나를 lease 아래에서 처리한다.
type processor struct {
	events     Events
	reconciler Reconciler
	auditor    Auditor
	now        func() time.Time
}

// process는 파싱된 알림을 처리한다.
//
// 반환값이 nil이면 마켓에 200을 준다. 재전송이 멈춘다.
// 에러를 주면 재시도 가능한 것만 마켓이 다시 보낸다.
func (p *processor) process(
	ctx context.Context,
	provider string,
	n notification,
	verifier Verifier,
) error {
	if n.EventKey == "" {
		return platformerr.New(platformerr.CodeEventReplayMismatch,
			"알림 식별자가 없어요")
	}

	claim, err := p.events.ClaimEvent(ctx, n.EventKey, provider)
	if err != nil {
		return err
	}

	// 이미 처리한 알림이다. 에러가 아니다.
	// 재시도 가능한 에러를 주면 같은 알림이 영원히 돌아온다.
	if claim.AlreadyProcessed {
		slog.InfoContext(ctx, "이미 처리한 알림",
			"provider", provider, "kind", n.Kind)
		return nil
	}

	err = p.reconcile(ctx, provider, n, verifier)
	if err == nil {
		return p.events.CompleteEvent(ctx, n.EventKey, claim.LeaseID)
	}

	// 다시 시도해도 결과가 같은 실패는 완료로 남긴다.
	// 그러지 않으면 마켓이 같은 알림을 영원히 재전송한다.
	if isPermanent(err) {
		slog.ErrorContext(ctx, "알림 처리 영구 실패",
			"provider", provider, "kind", n.Kind,
			"code", string(platformerr.CodeOf(err)))

		if cerr := p.events.CompleteEvent(ctx, n.EventKey, claim.LeaseID); cerr != nil {
			return cerr
		}
		// 마켓에는 성공을 알린다. 재전송해도 결과가 같다.
		return nil
	}

	// 재시도 가치가 있다. 점유를 풀고 마켓이 다시 보내게 한다.
	if rerr := p.events.ReleaseEvent(ctx, n.EventKey, claim.LeaseID); rerr != nil {
		slog.ErrorContext(ctx, "알림 점유 해제 실패", "err", rerr)
	}
	return err
}

// reconcile은 마켓에 재확인하고 원장에 반영한다.
func (p *processor) reconcile(
	ctx context.Context,
	provider string,
	n notification,
	verifier Verifier,
) error {
	// 재검증할 것이 없는 알림이다. 점유만 하고 끝낸다.
	// 구독이나 테스트 알림이 여기 온다.
	if !n.hasProof() {
		return nil
	}
	if verifier == nil {
		return platformerr.New(platformerr.CodePlatformUnavailable,
			"해당 마켓 검증기가 준비되지 않았어요")
	}

	// 알림을 믿지 않고 마켓에 다시 묻는다.
	// 알림이 지연되거나 순서가 뒤바뀌어 도착해도 최종 상태가 맞는다.
	purchase, err := verifier.Verify(ctx, n.Proof)
	if err != nil {
		// 마켓이 모르는 구매다. 환불 후 삭제된 경우가 있다.
		// 재시도해도 같으므로 여기서 끝낸다.
		if platformerr.CodeOf(err) == platformerr.CodePurchaseNotFound {
			p.audit(ctx, n, "purchase_not_found", "")
			return nil
		}
		return err
	}

	// 마켓 응답에 시각이 없으면 알림 시각을 쓴다.
	// 원장의 stale 억제가 시각에 기대기 때문에 0값이 새면 안 된다.
	if purchase.ObservedAt.IsZero() {
		purchase.ObservedAt = n.ObservedAt
	}
	if purchase.ObservedAt.IsZero() {
		purchase.ObservedAt = p.now()
	}

	res, err := p.reconciler.ReconcileByCanonicalID(ctx, purchase)
	if err != nil {
		return err
	}

	outcome := "reconciled"
	if !res.Known {
		// 불변식 10. 모르는 주문에 알림만으로 지급하지 않는다.
		outcome = "unknown_order"
	}
	p.audit(ctx, n, outcome, res.PlatformUserID)

	slog.InfoContext(ctx, "알림 반영",
		"provider", provider,
		"kind", n.Kind,
		"state", string(purchase.State),
		"known", res.Known,
	)
	return nil
}

// isPermanent는 다시 시도해도 소용없는 실패인지 본다.
//
// 분류되지 않은 에러는 재시도 가능으로 본다. 기본값이 반대면
// Firestore 장애 같은 일시적 실패가 "영구 실패"로 완료 처리되어
// 환불이 영원히 반영되지 않는다. 알림 처리는 멱등이므로
// 잃는 것보다 중복이 낫다.
func isPermanent(err error) bool {
	var pe *platformerr.Error
	if !errors.As(err, &pe) {
		return false
	}

	// 자격증명 문제는 검증 경로에서는 재시도 무의미다 — 사용자를
	// 기다리게 할 이유가 없다. 하지만 알림 경로에서는 다르다.
	// 운영자가 설정을 고치면 처리할 수 있는데, 완료로 남기면
	// 그동안 온 알림을 전부 잃는다. 환불이 반영되지 않는다.
	if platformerr.CodeOf(err) == platformerr.CodeProviderAuthFailed {
		return false
	}

	return !platformerr.IsRetryableErr(err)
}

func (p *processor) audit(ctx context.Context, n notification, outcome, puid string) {
	if p.auditor == nil {
		return
	}
	// appID는 알림에 없다. 주문에서 소유자를 찾아야 알 수 있는데
	// 알림 경로에서는 그 조회가 항상 성공하지 않는다.
	p.auditor.Record(ctx, "iap.notification", "", puid, outcome, map[string]any{
		"platform": string(n.Proof.Platform),
		"kind":     n.Kind,
	})
}
