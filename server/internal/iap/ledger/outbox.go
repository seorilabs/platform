package ledger

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

// 마켓 완료 처리 재시도 대기열이다.
//
// 지급은 끝났는데 마켓에 "완료했다"고 알리지 못한 주문이 여기 쌓인다.
// 지급을 롤백하지 않는 것이 불변식 7이다. 반대로 하면
// "돈은 나갔는데 물건이 없다"가 된다.
//
// Play는 3일 안에 acknowledge하지 않으면 자동 환불한다.
// 그래서 이 대기열이 비어 있는지가 실제 매출에 직결된다.

// outbox 상태다.
const (
	outboxPending    = "pending"
	outboxProcessing = "processing"
	outboxDeadLetter = "dead_letter"
)

// backoffBase는 첫 재시도 간격이다.
const backoffBase = 60 * time.Second

// backoffMax는 재시도 간격 상한이다.
//
// Play의 3일 자동 환불 안에 여러 번 시도할 수 있어야 한다.
const backoffMax = 6 * time.Hour

// outboxDoc은 완료 재시도 항목이다.
type outboxDoc struct {
	Platform  domain.Platform   `firestore:"platform"`
	Action    domain.Completion `firestore:"action"`
	ProductID string            `firestore:"productId"`
	// CanonicalID와 ProviderOrderID는 마켓 완료 호출에 필요하다.
	// Apple은 transactionId를 쓰므로 둘 다 갖고 있어야 한다.
	CanonicalID     string `firestore:"canonicalId"`
	ProviderOrderID string `firestore:"providerOrderId"`

	Status       string `firestore:"status"`
	AttemptCount int    `firestore:"attemptCount"`

	LeaseID        string    `firestore:"leaseId"`
	ClaimExpiresAt time.Time `firestore:"claimExpiresAt"`
	NextAttemptAt  time.Time `firestore:"nextAttemptAt"`

	LastErrorCode string    `firestore:"lastErrorCode"`
	CreatedAt     time.Time `firestore:"createdAt"`
	UpdatedAt     time.Time `firestore:"updatedAt"`
}

// OutboxItem은 워커가 집어온 완료 대기 항목이다.
type OutboxItem struct {
	OrderKey     string
	LeaseID      string
	AttemptCount int
	Purchase     domain.VerifiedPurchase
	CreatedAt    time.Time
}

// Enqueue는 완료 재시도를 예약한다.
//
// 이미 있으면 덮어쓰지 않는다. 재시도 횟수와 백오프가 초기화되면
// 실패하는 항목이 영원히 첫 간격으로 되돌아온다.
func (l *Ledger) Enqueue(ctx context.Context, orderKey string, p domain.VerifiedPurchase) error {
	if orderKey == "" {
		return platformerr.New(platformerr.CodeLedgerStateInvalid,
			"주문 식별자가 비어 있어요")
	}

	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := l.now()

		path, err := l.paths.outbox(orderKey)
		if err != nil {
			return err
		}

		exists, _, err := tx.Exists(path)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}

		return tx.Create(path, outboxDoc{
			Platform:        p.Platform,
			Action:          p.Completion,
			ProductID:       p.ProductID,
			CanonicalID:     p.CanonicalID,
			ProviderOrderID: p.ProviderOrderID,
			Status:          outboxPending,
			AttemptCount:    0,
			// 첫 시도는 바로 한다. 워커가 다음 주기에 집어간다.
			NextAttemptAt: now,
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	})
}

// ClaimNext는 완료 대기 항목 하나를 점유한다.
//
// 한 번에 하나만 집는다. 여러 건을 한꺼번에 점유하면 앞 건이 느릴 때
// 뒤 건의 lease가 처리도 못 해보고 만료된다.
//
// 집을 것이 없으면 (OutboxItem{}, false, nil)이다.
func (l *Ledger) ClaimNext(ctx context.Context, platform domain.Platform) (OutboxItem, bool, error) {
	var (
		item  OutboxItem
		found bool
	)

	leaseID, err := newLeaseID()
	if err != nil {
		return OutboxItem{}, false, err
	}

	err = l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		item, found = OutboxItem{}, false

		now := l.now()

		col, err := l.paths.outboxes()
		if err != nil {
			return err
		}

		// pending이고 시간이 된 것 중 가장 오래 기다린 것부터.
		// (platform, status, nextAttemptAt) 복합 인덱스가 필요하다.
		iter, err := tx.Query(col, func(q firestore.Query) firestore.Query {
			return q.
				Where("platform", "==", string(platform)).
				Where("status", "==", outboxPending).
				Where("nextAttemptAt", "<=", now).
				OrderBy("nextAttemptAt", firestore.Asc).
				Limit(1)
		})
		if err != nil {
			return err
		}
		defer iter.Stop()

		snap, err := iter.Next()
		if store.IsDone(err) {
			return nil
		}
		if err != nil {
			return err
		}

		var doc outboxDoc
		if err := snap.DataTo(&doc); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"완료 대기열을 읽지 못했어요")
		}

		orderKey := snap.Ref.ID

		path, err := l.paths.outbox(orderKey)
		if err != nil {
			return err
		}

		doc.Status = outboxProcessing
		doc.AttemptCount++
		doc.LeaseID = leaseID
		doc.ClaimExpiresAt = now.Add(leaseTTL)
		doc.UpdatedAt = now

		if err := tx.Set(path, doc); err != nil {
			return err
		}

		item = OutboxItem{
			OrderKey:     orderKey,
			LeaseID:      leaseID,
			AttemptCount: doc.AttemptCount,
			CreatedAt:    doc.CreatedAt,
			Purchase: domain.VerifiedPurchase{
				Platform:        doc.Platform,
				ProductID:       doc.ProductID,
				CanonicalID:     doc.CanonicalID,
				ProviderOrderID: doc.ProviderOrderID,
				Completion:      doc.Action,
				State:           domain.StateActive,
			},
		}
		found = true
		return nil
	})
	if err != nil {
		return OutboxItem{}, false, err
	}
	return item, found, nil
}

// CompleteOutbox는 마켓 완료에 성공한 항목을 지운다.
//
// 원장에서 문서를 지우는 유일한 곳이다. 불변식 5의 예외다.
// 이건 대기열이지 원장이 아니다. 완료 사실은 processed_orders에 남는다.
func (l *Ledger) CompleteOutbox(ctx context.Context, orderKey, leaseID string) error {
	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		path, err := l.paths.outbox(orderKey)
		if err != nil {
			return err
		}

		exists, snap, err := tx.Exists(path)
		if err != nil {
			return err
		}
		if !exists {
			// 다른 워커가 이미 끝냈다. 성공으로 본다.
			return nil
		}

		var doc outboxDoc
		if err := snap.DataTo(&doc); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"완료 대기열을 읽지 못했어요")
		}
		if doc.LeaseID != leaseID {
			return platformerr.New(platformerr.CodeCompletionClaimLost,
				"완료 처리 점유를 잃었어요")
		}

		return tx.Delete(path)
	})
}

// FailOutbox는 완료 실패를 기록하고 다음 시도를 예약한다.
//
// maxAttempts를 넘었거나 maxAge보다 오래됐으면 dead_letter로 보낸다.
// dead_letter는 지우지 않는다. 운영자가 봐야 하기 때문이다.
func (l *Ledger) FailOutbox(
	ctx context.Context,
	orderKey, leaseID string,
	errCode platformerr.Code,
	maxAttempts int,
	maxAge time.Duration,
) error {
	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := l.now()

		path, err := l.paths.outbox(orderKey)
		if err != nil {
			return err
		}

		exists, snap, err := tx.Exists(path)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}

		var doc outboxDoc
		if err := snap.DataTo(&doc); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"완료 대기열을 읽지 못했어요")
		}
		if doc.LeaseID != leaseID {
			return platformerr.New(platformerr.CodeCompletionClaimLost,
				"완료 처리 점유를 잃었어요")
		}

		doc.LastErrorCode = string(errCode)
		doc.LeaseID = ""
		doc.ClaimExpiresAt = time.Time{}
		doc.UpdatedAt = now

		tooOld := maxAge > 0 && now.Sub(doc.CreatedAt) > maxAge
		tooMany := maxAttempts > 0 && doc.AttemptCount >= maxAttempts

		if tooOld || tooMany {
			// 더 시도해도 소용없다. 운영자가 처리한다.
			doc.Status = outboxDeadLetter
			doc.NextAttemptAt = time.Time{}
		} else {
			doc.Status = outboxPending
			doc.NextAttemptAt = now.Add(backoffFor(doc.AttemptCount))
		}

		return tx.Set(path, doc)
	})
}

// backoffFor는 시도 횟수에 따른 대기 시간이다.
//
//	min(60s * 2^(n-1), 6h)
//
// 시프트로 계산하면 n이 크실 때 오버플로가 난다. 상한에서 끊는다.
func backoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	d := backoffBase
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= backoffMax {
			return backoffMax
		}
	}
	return d
}

// CountDeadLetters는 dead_letter 항목 수를 센다.
//
// 알림과 운영 화면에 쓴다. 0이 아니면 사람이 봐야 한다.
func (l *Ledger) CountDeadLetters(ctx context.Context) (int, error) {
	col, err := l.paths.outboxes()
	if err != nil {
		return 0, err
	}

	iter, err := l.store.Query(ctx, col, func(q firestore.Query) firestore.Query {
		return q.Where("status", "==", outboxDeadLetter)
	})
	if err != nil {
		return 0, err
	}
	defer iter.Stop()

	n := 0
	for {
		_, err := iter.Next()
		if store.IsDone(err) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		n++
	}
}
