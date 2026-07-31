package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

// 마켓 알림 처리의 멱등성을 lease로 보장한다.
//
// 같은 알림이 두 번 와도 한 번만 처리한다. Apple도 Play도 응답이
// 늦거나 실패하면 재전송하므로 중복은 예외가 아니라 일상이다.
//
// 흐름은 claim → 처리 → complete다. 처리 도중 죽으면 lease가 만료되고
// 다음 재전송이 뺏어간다. release는 재시도 가치가 있는 실패에 쓴다.

// leaseTTL은 이벤트 점유 시간이다.
//
// 이보다 오래 걸리는 처리는 죽은 것으로 본다.
// 마켓 호출이 붙어도 5분이면 충분하다.
const leaseTTL = 5 * time.Minute

// 이벤트 처리 상태다.
const (
	eventProcessing = "processing"
	eventCompleted  = "completed"
)

// eventDoc은 처리된 알림 원장이다.
//
// 삭제하지 않는다. 불변식 5다. 지우면 멱등성이 사라진다.
type eventDoc struct {
	Provider     string    `firestore:"provider"`
	Status       string    `firestore:"status"`
	AttemptCount int       `firestore:"attemptCount"`
	LeaseID      string    `firestore:"leaseId"`
	ClaimedAt    time.Time `firestore:"claimedAt"`
	// ClaimExpiresAt이 지나면 다른 처리자가 뺏을 수 있다.
	ClaimExpiresAt time.Time `firestore:"claimExpiresAt"`
	CompletedAt    time.Time `firestore:"completedAt"`
	CreatedAt      time.Time `firestore:"createdAt"`
	UpdatedAt      time.Time `firestore:"updatedAt"`
}

// EventClaim은 점유한 알림이다.
type EventClaim struct {
	EventKey string
	LeaseID  string
	// AttemptCount는 이번이 몇 번째 시도인지다. 1부터 센다.
	AttemptCount int
	// AlreadyProcessed면 이미 끝난 알림이다.
	//
	// 에러가 아니다. 마켓에 200을 주어 재전송을 멈춰야 한다.
	// 재시도 가능한 에러를 주면 같은 알림이 영원히 돌아온다.
	AlreadyProcessed bool
}

// ClaimEvent는 알림 처리를 점유한다.
//
// 이미 처리된 알림이면 AlreadyProcessed를 세워 돌려준다.
// 다른 처리자가 점유 중이면 event_busy다 — 503이라 마켓이 재전송한다.
func (l *Ledger) ClaimEvent(ctx context.Context, eventKey, provider string) (EventClaim, error) {
	if eventKey == "" || provider == "" {
		return EventClaim{}, platformerr.New(platformerr.CodeEventReplayMismatch,
			"알림 식별자가 비어 있어요")
	}

	leaseID, err := newLeaseID()
	if err != nil {
		return EventClaim{}, err
	}

	var claim EventClaim

	err = l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		// 트랜잭션은 재실행될 수 있다. 매번 초기화한다.
		claim = EventClaim{EventKey: eventKey, LeaseID: leaseID, AttemptCount: 1}

		now := l.now()

		path, err := l.paths.event(eventKey)
		if err != nil {
			return err
		}

		exists, snap, err := tx.Exists(path)
		if err != nil {
			return err
		}

		if !exists {
			claim.AttemptCount = 1
			return tx.Create(path, eventDoc{
				Provider:       provider,
				Status:         eventProcessing,
				AttemptCount:   1,
				LeaseID:        leaseID,
				ClaimedAt:      now,
				ClaimExpiresAt: now.Add(leaseTTL),
				CreatedAt:      now,
				UpdatedAt:      now,
			})
		}

		var doc eventDoc
		if err := snap.DataTo(&doc); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"알림 원장을 읽지 못했어요")
		}

		// 이미 끝났다. 마켓에 200을 준다.
		if doc.Status == eventCompleted {
			claim.AlreadyProcessed = true
			return nil
		}

		// 다른 처리자가 살아 있는 lease로 점유 중이다.
		if doc.ClaimExpiresAt.After(now) {
			return platformerr.New(platformerr.CodeEventBusy,
				"같은 알림을 처리 중이에요")
		}

		// lease가 만료됐다. 뺏는다.
		claim.AttemptCount = doc.AttemptCount + 1
		doc.Status = eventProcessing
		doc.AttemptCount = claim.AttemptCount
		doc.LeaseID = leaseID
		doc.ClaimedAt = now
		doc.ClaimExpiresAt = now.Add(leaseTTL)
		doc.UpdatedAt = now

		return tx.Set(path, doc)
	})
	if err != nil {
		return EventClaim{}, err
	}
	return claim, nil
}

// CompleteEvent는 알림 처리를 끝낸다.
//
// 성공했을 때도, 다시 시도해도 소용없는 실패일 때도 부른다.
// 후자를 완료로 남기지 않으면 마켓이 같은 알림을 영원히 재전송한다.
func (l *Ledger) CompleteEvent(ctx context.Context, eventKey, leaseID string) error {
	return l.updateClaimedEvent(ctx, eventKey, leaseID, func(doc *eventDoc, now time.Time) {
		doc.Status = eventCompleted
		doc.CompletedAt = now
		// lease를 비워 다른 처리자가 다시 잡지 않게 한다.
		doc.LeaseID = ""
		doc.ClaimExpiresAt = time.Time{}
	})
}

// ReleaseEvent는 점유를 놓아준다.
//
// 재시도 가치가 있는 실패에 쓴다. 마켓이 다시 보내면 그때 처리한다.
// lease 만료를 기다리지 않고 즉시 풀어주는 것이 목적이다.
func (l *Ledger) ReleaseEvent(ctx context.Context, eventKey, leaseID string) error {
	return l.updateClaimedEvent(ctx, eventKey, leaseID, func(doc *eventDoc, _ time.Time) {
		doc.LeaseID = ""
		doc.ClaimExpiresAt = time.Time{}
	})
}

// updateClaimedEvent는 lease를 확인하고 문서를 갱신한다.
//
// lease가 다르면 우리가 처리하는 사이에 다른 처리자가 뺏어간 것이다.
// 그대로 쓰면 두 처리자가 같은 알림을 이중 반영한다.
func (l *Ledger) updateClaimedEvent(
	ctx context.Context,
	eventKey, leaseID string,
	mutate func(*eventDoc, time.Time),
) error {
	if eventKey == "" || leaseID == "" {
		return platformerr.New(platformerr.CodeEventClaimLost,
			"알림 점유 정보가 비어 있어요")
	}

	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := l.now()

		path, err := l.paths.event(eventKey)
		if err != nil {
			return err
		}

		exists, snap, err := tx.Exists(path)
		if err != nil {
			return err
		}
		if !exists {
			return platformerr.New(platformerr.CodeEventClaimLost,
				"알림 기록을 찾을 수 없어요")
		}

		var doc eventDoc
		if err := snap.DataTo(&doc); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"알림 원장을 읽지 못했어요")
		}

		// 이미 완료된 알림에 대한 늦은 호출이다. 되돌리지 않는다.
		if doc.Status == eventCompleted {
			return nil
		}
		if doc.LeaseID != leaseID {
			return platformerr.New(platformerr.CodeEventClaimLost,
				"알림 점유를 잃었어요")
		}

		mutate(&doc, now)
		doc.UpdatedAt = now

		return tx.Set(path, doc)
	})
}

// newLeaseID는 점유 식별자를 만든다.
//
// 충돌하면 남의 점유를 자기 것으로 착각한다. 무작위 16바이트면 충분하다.
func newLeaseID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", platformerr.Wrap(err, platformerr.CodeInternal,
			"점유 식별자를 만들지 못했어요")
	}
	return hex.EncodeToString(b), nil
}
