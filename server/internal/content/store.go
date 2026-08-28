package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/store"
)

const (
	usageCollection        = "content_usage"
	unlockCollection       = "content_unlocks"
	claimBindingCollection = "content_claim_bindings"

	// maxUnlockListLimit은 한 번에 읽는 해제 문서 수 상한이다.
	// 화면은 최신 몇 건만 쓰지만, 정렬을 메모리에서 하므로 상한 안에서
	// 전부 읽어야 "최신순"이 실제로 최신이 된다.
	maxUnlockListLimit = 200
)

type StoreRepository struct {
	store *store.Client
	now   func() time.Time
}

func NewStoreRepository(st *store.Client) *StoreRepository {
	return &StoreRepository{store: st, now: time.Now}
}

func (r *StoreRepository) WithClock(now func() time.Time) *StoreRepository {
	r.now = now
	return r
}

type usageDoc struct {
	AppID          string          `firestore:"appId"`
	PlatformUserID string          `firestore:"platformUserId"`
	Date           string          `firestore:"date"`
	ReadingKeys    map[string]bool `firestore:"readingKeys"`
	TermCount      int             `firestore:"termCount"`
	UpdatedAt      time.Time       `firestore:"updatedAt"`
}

type unlockDoc struct {
	AppID          string    `firestore:"appId"`
	PlatformUserID string    `firestore:"platformUserId"`
	ReadingKey     string    `firestore:"readingKey"`
	DeepKey        string    `firestore:"deepKey"`
	Source         string    `firestore:"source"`
	Reference      string    `firestore:"reference,omitempty"`
	CreatedAt      time.Time `firestore:"createdAt"`
}

type claimBindingDoc struct {
	ClaimID        string    `firestore:"claimId"`
	AppID          string    `firestore:"appId"`
	PlatformUserID string    `firestore:"platformUserId"`
	ReadingKey     string    `firestore:"readingKey"`
	DeepKey        string    `firestore:"deepKey"`
	CreatedAt      time.Time `firestore:"createdAt"`
}

func (r *StoreRepository) AllowReading(
	ctx context.Context,
	app registry.App,
	puid, readingKey string,
) error {
	return r.updateUsage(ctx, app, puid, func(doc *usageDoc) error {
		return allowReadingKey(doc, app.Content.ReadingDailyLimit, readingKey)
	})
}

func (r *StoreRepository) AllowTerm(ctx context.Context, app registry.App, puid string) error {
	return r.updateUsage(ctx, app, puid, func(doc *usageDoc) error {
		return allowTermLookup(doc, app.Content.TermDailyLimit)
	})
}

func allowReadingKey(doc *usageDoc, limit int, readingKey string) error {
	if doc.ReadingKeys[readingKey] {
		return nil
	}
	if len(doc.ReadingKeys) >= limit {
		return platformerr.New(platformerr.CodeRateLimited,
			"오늘 새로 볼 수 있는 명식 수를 모두 사용했어요")
	}
	doc.ReadingKeys[readingKey] = true
	return nil
}

func allowTermLookup(doc *usageDoc, limit int) error {
	if doc.TermCount >= limit {
		return platformerr.New(platformerr.CodeRateLimited,
			"오늘 사전에서 볼 수 있는 항목 수를 모두 사용했어요")
	}
	doc.TermCount++
	return nil
}

func (r *StoreRepository) updateUsage(
	ctx context.Context,
	app registry.App,
	puid string,
	update func(*usageDoc) error,
) error {
	now := r.now().UTC()
	date := now.Format("2006-01-02")
	p, err := usagePath(app.AppID, puid, date)
	if err != nil {
		return err
	}
	err = r.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		exists, snap, err := tx.Exists(p)
		if err != nil {
			return err
		}
		doc := usageDoc{
			AppID: app.AppID, PlatformUserID: puid, Date: date,
			ReadingKeys: map[string]bool{},
		}
		if exists {
			if err := snap.DataTo(&doc); err != nil {
				return err
			}
			if doc.AppID != app.AppID || doc.PlatformUserID != puid || doc.Date != date {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"콘텐츠 사용량 원장이 올바르지 않아요")
			}
			if doc.ReadingKeys == nil {
				doc.ReadingKeys = map[string]bool{}
			}
		}
		if err := update(&doc); err != nil {
			return err
		}
		doc.UpdatedAt = now
		return tx.Set(p, doc)
	})
	if err != nil {
		if platformerr.CodeOf(err) == platformerr.CodeRateLimited ||
			platformerr.CodeOf(err) == platformerr.CodeLedgerStateInvalid {
			return err
		}
		return platformerr.Wrap(err, platformerr.CodeContentUnavailable,
			"콘텐츠 사용량을 기록하지 못했어요")
	}
	return nil
}

func (r *StoreRepository) GetUnlock(
	ctx context.Context,
	appID, puid, readingKey, deepKey string,
) (UnlockGrant, error) {
	p, err := unlockPath(appID, puid, readingKey, deepKey)
	if err != nil {
		return UnlockGrant{}, err
	}
	snap, err := r.store.Get(ctx, p)
	if errors.Is(err, store.ErrNotFound) {
		return UnlockGrant{}, nil
	}
	if err != nil {
		return UnlockGrant{}, err
	}
	var doc unlockDoc
	if err := snap.DataTo(&doc); err != nil {
		return UnlockGrant{}, err
	}
	if doc.AppID != appID || doc.PlatformUserID != puid || doc.ReadingKey != readingKey || doc.DeepKey != deepKey {
		return UnlockGrant{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"콘텐츠 잠금 해제 원장이 올바르지 않아요")
	}
	if (doc.Source == "reward_claim" && doc.Reference == "") ||
		(doc.Source == "ticket" && len(doc.Reference) != 64) ||
		(doc.Source != "reward_claim" && doc.Source != "ticket") {
		return UnlockGrant{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"콘텐츠 잠금 해제 근거가 올바르지 않아요")
	}
	return UnlockGrant{Exists: true, Source: doc.Source, Reference: doc.Reference}, nil
}

func (r *StoreRepository) BindReward(
	ctx context.Context,
	appID, puid, readingKey, deepKey, claimID string,
) error {
	up, err := unlockPath(appID, puid, readingKey, deepKey)
	if err != nil {
		return err
	}
	bp, err := claimBindingPath(claimID)
	if err != nil {
		return err
	}
	now := r.now().UTC()
	err = r.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		unlockExists, unlockSnap, err := tx.Exists(up)
		if err != nil {
			return err
		}
		bindingExists, bindingSnap, err := tx.Exists(bp)
		if err != nil {
			return err
		}
		if unlockExists {
			var existing unlockDoc
			if err := unlockSnap.DataTo(&existing); err != nil {
				return err
			}
			if err := validateUnlock(existing, appID, puid, readingKey, deepKey); err != nil {
				return err
			}
			if existing.Source != "reward_claim" || existing.Reference != claimID {
				return platformerr.New(platformerr.CodeContentReplayMismatch,
					"심화 항목은 이미 다른 권한으로 열렸어요")
			}
			return nil
		}
		if bindingExists {
			var existing claimBindingDoc
			if err := bindingSnap.DataTo(&existing); err != nil {
				return err
			}
			if existing.ClaimID != claimID || existing.AppID != appID ||
				existing.PlatformUserID != puid || existing.ReadingKey != readingKey || existing.DeepKey != deepKey {
				return platformerr.New(platformerr.CodeContentReplayMismatch,
					"광고 claim이 이미 다른 심화 항목에 사용됐어요")
			}
		} else if err := tx.Create(bp, claimBindingDoc{
			ClaimID: claimID, AppID: appID, PlatformUserID: puid,
			ReadingKey: readingKey, DeepKey: deepKey, CreatedAt: now,
		}); err != nil {
			return err
		}
		return tx.Create(up, unlockDoc{
			AppID: appID, PlatformUserID: puid, ReadingKey: readingKey, DeepKey: deepKey,
			Source: "reward_claim", Reference: claimID, CreatedAt: now,
		})
	})
	return wrapContentStore(err, "광고 보상을 심화 항목에 연결하지 못했어요")
}

func (r *StoreRepository) RecordTicket(
	ctx context.Context,
	appID, puid, readingKey, deepKey, sourceKey string,
) error {
	if len(sourceKey) != 64 {
		return platformerr.New(platformerr.CodeLedgerStateInvalid,
			"열람권 구매 source가 올바르지 않아요")
	}
	p, err := unlockPath(appID, puid, readingKey, deepKey)
	if err != nil {
		return err
	}
	now := r.now().UTC()
	err = r.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		exists, snap, err := tx.Exists(p)
		if err != nil {
			return err
		}
		if exists {
			var existing unlockDoc
			if err := snap.DataTo(&existing); err != nil {
				return err
			}
			if err := validateUnlock(existing, appID, puid, readingKey, deepKey); err != nil {
				return err
			}
			if existing.Source != "ticket" || existing.Reference != sourceKey {
				return platformerr.New(platformerr.CodeContentReplayMismatch,
					"심화 항목은 이미 다른 권한으로 열렸어요")
			}
			return nil
		}
		return tx.Create(p, unlockDoc{
			AppID: appID, PlatformUserID: puid, ReadingKey: readingKey, DeepKey: deepKey,
			Source: "ticket", Reference: sourceKey, CreatedAt: now,
		})
	})
	return wrapContentStore(err, "열람권 사용 결과를 기록하지 못했어요")
}

func validateUnlock(doc unlockDoc, appID, puid, readingKey, deepKey string) error {
	if doc.AppID != appID || doc.PlatformUserID != puid || doc.ReadingKey != readingKey || doc.DeepKey != deepKey {
		return platformerr.New(platformerr.CodeContentReplayMismatch,
			"심화 잠금 해제 요청 내용이 달라요")
	}
	return nil
}

func usagePath(appID, puid, date string) (fspath.Path, error) {
	return fspath.Parse(usageCollection + "/" + digest(appID+"\x00"+puid+"\x00"+date))
}

func unlockPath(appID, puid, readingKey, deepKey string) (fspath.Path, error) {
	return fspath.Parse(unlockCollection + "/" + digest(appID+"\x00"+puid+"\x00"+readingKey+"\x00"+deepKey))
}

func claimBindingPath(claimID string) (fspath.Path, error) {
	return fspath.Parse(claimBindingCollection + "/" + digest(claimID))
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func wrapContentStore(err error, message string) error {
	if err == nil {
		return nil
	}
	if platformerr.CodeOf(err) != platformerr.CodeInternal {
		return err
	}
	return platformerr.Wrap(err, platformerr.CodeContentUnavailable, message)
}

// ListUnlocks는 한 사용자가 이 앱에서 이미 연 심화 항목을 최신순으로 준다.
//
// 정렬을 Firestore에 맡기지 않고 메모리에서 한다. 동등 필터 둘에 OrderBy를
// 더하면 복합 인덱스가 필요해지고, 인덱스를 배포하지 않은 환경에서 조회가
// 통째로 실패한다. 같은 이유로 ads의 SuppressionHistory도 이 방식이다.
// 한 사용자의 해제 수는 구매 수에 비례해 작으므로 이 정도로 충분하다.
func (r *StoreRepository) ListUnlocks(
	ctx context.Context,
	appID, puid string,
	limit int,
) ([]UnlockRecord, error) {
	if appID == "" || puid == "" {
		return nil, platformerr.New(platformerr.CodeInternal,
			"심화 열람 현황 조회 정보가 올바르지 않아요")
	}
	if limit <= 0 || limit > maxUnlockListLimit {
		limit = maxUnlockListLimit
	}
	p, err := fspath.Parse(unlockCollection)
	if err != nil {
		return nil, err
	}
	iter, err := r.store.Query(ctx, p, func(q firestore.Query) firestore.Query {
		return q.Where("appId", "==", appID).
			Where("platformUserId", "==", puid).
			Limit(maxUnlockListLimit)
	})
	if err != nil {
		return nil, wrapContentStore(err, "심화 열람 현황을 읽지 못했어요")
	}
	defer iter.Stop()

	records := make([]UnlockRecord, 0, limit)
	for {
		snap, err := iter.Next()
		if store.IsDone(err) {
			break
		}
		if err != nil {
			return nil, wrapContentStore(err, "심화 열람 현황을 읽지 못했어요")
		}
		var doc unlockDoc
		if err := snap.DataTo(&doc); err != nil {
			return nil, err
		}
		if !listableUnlock(doc, appID, puid) {
			continue
		}
		records = append(records, UnlockRecord{
			ReadingKey: doc.ReadingKey, DeepKey: doc.DeepKey,
			Source: doc.Source, CreatedAt: doc.CreatedAt,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

// listableUnlock은 목록에 실을 수 있는 문서인지 본다.
//
// 원장이 깨진 문서 하나 때문에 화면 전체를 못 그리게 하지 않는다. 조회 경로는
// 권한 판정이 아니라 표시용이고 판정은 GetUnlock이 한다.
//
// 다만 **버리는 기준은 응답 계약과 같아야 한다.** source는 스펙에서 enum이고
// unlockedAt은 시각이다. 걸러내지 않으면 깨진 문서 하나가 알 수 없는 source나
// 0001-01-01을 그대로 내보내 서버가 자기 스펙을 어긴다.
func listableUnlock(doc unlockDoc, appID, puid string) bool {
	return doc.AppID == appID && doc.PlatformUserID == puid &&
		doc.ReadingKey != "" && doc.DeepKey != "" &&
		(doc.Source == "ticket" || doc.Source == "reward_claim") &&
		!doc.CreatedAt.IsZero()
}
