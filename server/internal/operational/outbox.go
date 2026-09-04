// Package operational은 확정 도메인 이벤트를 PII 없이 내구성 outbox에 기록하고
// Backoffice로 전달한다. 앱 요청 트랜잭션과 같은 Firestore transaction에 쓰므로
// 계정·지급은 커밋됐는데 이벤트만 유실되는 틈을 만들지 않는다.
package operational

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/store"
)

const collection = "operational_event_outbox"

const (
	statusPending    = "pending"
	statusProcessing = "processing"
	statusDeadLetter = "dead_letter"
)

type Event struct {
	EventID    string         `json:"eventId" firestore:"eventId"`
	OccurredAt time.Time      `json:"occurredAt" firestore:"occurredAt"`
	Type       string         `json:"type" firestore:"type"`
	AppID      string         `json:"appId" firestore:"appId"`
	Outcome    string         `json:"outcome" firestore:"outcome"`
	Attributes map[string]any `json:"attributes" firestore:"attributes"`
}

type eventContract struct {
	prefix     string
	outcome    string
	attributes map[string]struct{}
}

var (
	eventIDPattern = regexp.MustCompile(`^[a-z_]+_[0-9a-f]{32}$`)
	appIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	eventContracts = map[string]eventContract{
		"identity.created": {
			prefix: "identity_", outcome: "created",
			attributes: setOf("authType", "signInProvider", "anonymous", "referrer"),
		},
		"iap.granted": {
			prefix: "iap_", outcome: "granted",
			attributes: setOf("platform", "entitlementId"),
		},
		"ad.reward.delivered": {
			prefix: "ad_reward_", outcome: "delivered",
			attributes: setOf("provider", "placementId", "rewardKey", "rewardAmount"),
		},
	}
)

type outboxDoc struct {
	Event
	Status         string    `firestore:"status"`
	AttemptCount   int       `firestore:"attemptCount"`
	LeaseID        string    `firestore:"leaseId"`
	ClaimExpiresAt time.Time `firestore:"claimExpiresAt"`
	NextAttemptAt  time.Time `firestore:"nextAttemptAt"`
	LastError      string    `firestore:"lastError"`
	CreatedAt      time.Time `firestore:"createdAt"`
	UpdatedAt      time.Time `firestore:"updatedAt"`
}

type Item struct {
	Event
	LeaseID      string
	AttemptCount int
}

type Repository struct {
	store *store.Client
	now   func() time.Time
}

func NewRepository(st *store.Client) *Repository {
	return &Repository{store: st, now: time.Now}
}

func StableEventID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(h.Sum(nil)[:16])
}

func outboxPath(eventID string) (fspath.Path, error) {
	return fspath.Parse(collection + "/" + eventID)
}

func outboxesPath() (fspath.Path, error) { return fspath.Parse(collection) }

func (r *Repository) EnqueueTx(tx *store.Tx, event Event) error {
	now := r.now().UTC()
	if event.OccurredAt.IsZero() {
		event.OccurredAt = now
	}
	if err := validateEvent(event); err != nil {
		return err
	}
	path, err := outboxPath(event.EventID)
	if err != nil {
		return err
	}
	return tx.Create(path, outboxDoc{
		Event: event, Status: statusPending, NextAttemptAt: now,
		CreatedAt: now, UpdatedAt: now,
	})
}

func validateEvent(event Event) error {
	contract, ok := eventContracts[event.Type]
	if !ok || !eventIDPattern.MatchString(event.EventID) ||
		!strings.HasPrefix(event.EventID, contract.prefix) || !appIDPattern.MatchString(event.AppID) ||
		event.Outcome != contract.outcome || event.OccurredAt.IsZero() {
		return errors.New("operational: 이벤트 계약이 올바르지 않다")
	}
	if len(event.Attributes) > 20 {
		return errors.New("operational: 이벤트 attribute가 너무 많다")
	}
	for key, value := range event.Attributes {
		if _, allowed := contract.attributes[key]; !allowed || !safeScalar(value) {
			return fmt.Errorf("operational: 허용되지 않은 attribute: %s", key)
		}
	}
	return nil
}

func setOf(keys ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		result[key] = struct{}{}
	}
	return result
}

func safeScalar(value any) bool {
	switch value := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case string:
		return len(value) <= 120
	case float32:
		return !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0)
	case float64:
		return !math.IsNaN(value) && !math.IsInf(value, 0)
	default:
		return false
	}
}

func (r *Repository) ClaimNext(ctx context.Context) (Item, bool, error) {
	leaseID, err := randomID()
	if err != nil {
		return Item{}, false, err
	}
	var item Item
	var found bool
	err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		item, found = Item{}, false
		now := r.now().UTC()
		col, err := outboxesPath()
		if err != nil {
			return err
		}
		iter, err := tx.Query(col, func(q firestore.Query) firestore.Query {
			return q.Where("status", "==", statusPending).
				Where("nextAttemptAt", "<=", now).
				OrderBy("nextAttemptAt", firestore.Asc).Limit(1)
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
			return err
		}
		path, err := outboxPath(doc.EventID)
		if err != nil {
			return err
		}
		doc.Status = statusProcessing
		doc.AttemptCount++
		doc.LeaseID = leaseID
		doc.ClaimExpiresAt = now.Add(2 * time.Minute)
		doc.UpdatedAt = now
		if err := tx.Set(path, doc); err != nil {
			return err
		}
		item = Item{Event: doc.Event, LeaseID: leaseID, AttemptCount: doc.AttemptCount}
		found = true
		return nil
	})
	return item, found, err
}

func (r *Repository) Complete(ctx context.Context, eventID, leaseID string) error {
	return r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		path, err := outboxPath(eventID)
		if err != nil {
			return err
		}
		exists, snap, err := tx.Exists(path)
		if err != nil || !exists {
			return err
		}
		var doc outboxDoc
		if err := snap.DataTo(&doc); err != nil {
			return err
		}
		if doc.LeaseID != leaseID {
			return errors.New("operational: delivery lease를 잃었다")
		}
		return tx.Delete(path)
	})
}

func (r *Repository) Fail(ctx context.Context, eventID, leaseID, reason string) error {
	return r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		path, err := outboxPath(eventID)
		if err != nil {
			return err
		}
		exists, snap, err := tx.Exists(path)
		if err != nil || !exists {
			return err
		}
		var doc outboxDoc
		if err := snap.DataTo(&doc); err != nil {
			return err
		}
		if doc.LeaseID != leaseID {
			return errors.New("operational: delivery lease를 잃었다")
		}
		now := r.now().UTC()
		doc.LeaseID = ""
		doc.ClaimExpiresAt = time.Time{}
		doc.LastError = reason
		doc.UpdatedAt = now
		if doc.AttemptCount >= 10 {
			doc.Status = statusDeadLetter
			doc.NextAttemptAt = time.Time{}
		} else {
			doc.Status = statusPending
			doc.NextAttemptAt = now.Add(backoff(doc.AttemptCount))
		}
		return tx.Set(path, doc)
	})
}

func (r *Repository) RecoverExpired(ctx context.Context) error {
	// claim query와 동일 컬렉션을 작은 범위로 훑는다. 중단된 프로세스의
	// processing lease만 되돌리며 정상 pending 항목은 건드리지 않는다.
	col, err := outboxesPath()
	if err != nil {
		return err
	}
	now := r.now().UTC()
	iter, err := r.store.Query(ctx, col, func(q firestore.Query) firestore.Query {
		return q.Where("status", "==", statusProcessing).
			Where("claimExpiresAt", "<=", now).Limit(20)
	})
	if err != nil {
		return err
	}
	defer iter.Stop()
	for {
		snap, err := iter.Next()
		if store.IsDone(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var candidate outboxDoc
		if err := snap.DataTo(&candidate); err != nil {
			return err
		}
		path, err := outboxPath(candidate.EventID)
		if err != nil {
			return err
		}
		// Query 뒤 바로 쓰면 다른 인스턴스가 lease를 연장한 값을 덮을 수 있다.
		// transaction에서 status와 만료 시각을 다시 확인한 뒤 CAS한다.
		if err := r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
			exists, currentSnap, err := tx.Exists(path)
			if err != nil || !exists {
				return err
			}
			var current outboxDoc
			if err := currentSnap.DataTo(&current); err != nil {
				return err
			}
			if current.Status != statusProcessing || current.ClaimExpiresAt.After(now) {
				return nil
			}
			current.Status = statusPending
			current.LeaseID = ""
			current.ClaimExpiresAt = time.Time{}
			current.NextAttemptAt = now
			current.UpdatedAt = now
			return tx.Set(path, current)
		}); err != nil {
			return err
		}
	}
}

func backoff(attempt int) time.Duration {
	exponent := max(attempt-1, 0)
	d := 30 * time.Second * time.Duration(1<<min(exponent, 6))
	return min(d, 30*time.Minute)
}

func randomID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("operational: lease ID 생성 실패: %w", err)
	}
	return hex.EncodeToString(b), nil
}
