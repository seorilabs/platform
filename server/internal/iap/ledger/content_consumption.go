package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

type contentSourceUsageDoc struct {
	EntitlementID string    `firestore:"entitlementId"`
	SourceKey     string    `firestore:"sourceKey"`
	Consumed      int       `firestore:"consumed"`
	UpdatedAt     time.Time `firestore:"updatedAt"`
}

type contentConsumptionDoc struct {
	PlatformUserID string    `firestore:"platformUserId"`
	EntitlementID  string    `firestore:"entitlementId"`
	RequestKey     string    `firestore:"requestKey"`
	SourceKey      string    `firestore:"sourceKey"`
	Consumed       int       `firestore:"consumed"`
	Remaining      int       `firestore:"remaining"`
	CreatedAt      time.Time `firestore:"createdAt"`
}

// ConsumptionResult는 원자 차감 결과다. Applied=false는 같은 requestKey의
// 재시도였음을 뜻하며 차감 수는 늘지 않는다.
type ConsumptionResult struct {
	Applied   bool
	Remaining int
	// SourceKey는 차감한 구매 source의 비식별 SHA-256 key다. 콘텐츠 권한이
	// 환불·소유권 이전 뒤에도 이 source의 활성 상태를 다시 확인하는 데 쓴다.
	SourceKey string
}

// ConsumeUnits는 현재 활성 구매 source마다 unitsPerSource를 배정하고 한 건을
// 원자적으로 차감한다. 불변식 4·6에 따라 source 해시에 사용량을 묶으므로 소유권
// 이전에도 사용량을 되살리지 않고, 환불된 source의 사용량이 새 구매를 깎지 않는다.
// 불변식 5에 따라 request별 consumption 증거는 create-only로 남겨 재시작을 멱등 처리한다.
func (l *Ledger) ConsumeUnits(
	ctx context.Context,
	puid, entitlementID string,
	unitsPerSource int,
	requestKey string,
) (ConsumptionResult, error) {
	if puid == "" || entitlementID == "" || requestKey == "" || unitsPerSource <= 0 || unitsPerSource > 10000 {
		return ConsumptionResult{}, platformerr.New(platformerr.CodeInternal,
			"콘텐츠 열람권 차감 정보가 올바르지 않아요")
	}

	consumptionPath, err := l.paths.contentConsumption(contentRequestDigest(
		l.appID + "\x00" + string(l.env) + "\x00" + puid + "\x00" + entitlementID + "\x00" + requestKey,
	))
	if err != nil {
		return ConsumptionResult{}, err
	}
	entitlementPath, err := l.paths.internalEntitlement(puid, entitlementID)
	if err != nil {
		return ConsumptionResult{}, err
	}
	result := ConsumptionResult{}
	err = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		consumptionExists, consumptionSnap, err := tx.Exists(consumptionPath)
		if err != nil {
			return err
		}
		if consumptionExists {
			var existing contentConsumptionDoc
			if err := consumptionSnap.DataTo(&existing); err != nil {
				return err
			}
			if existing.PlatformUserID != puid || existing.EntitlementID != entitlementID ||
				existing.RequestKey != requestKey || len(existing.SourceKey) != 64 ||
				existing.Consumed != 1 || existing.Remaining < 0 {
				return platformerr.New(platformerr.CodeContentReplayMismatch,
					"열람권 재시도 내용이 기존 차감과 달라요")
			}
			result = ConsumptionResult{
				Applied: false, Remaining: existing.Remaining, SourceKey: existing.SourceKey,
			}
			return nil
		}

		entitlementExists, entitlementSnap, err := tx.Exists(entitlementPath)
		if err != nil {
			return err
		}

		if !entitlementExists {
			return platformerr.New(platformerr.CodeContentTicketEmpty,
				"사용할 수 있는 열람권이 없어요")
		}
		var entitlement entitlementDoc
		if err := entitlementSnap.DataTo(&entitlement); err != nil {
			return err
		}
		if entitlement.EntitlementID != "" && entitlement.EntitlementID != entitlementID {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"열람권 entitlement 원장이 올바르지 않아요")
		}
		activeSources := make([]string, 0, len(entitlement.Sources))
		for sourceKey, source := range entitlement.Sources {
			if source.State == domain.StateActive {
				activeSources = append(activeSources, sourceKey)
			}
		}
		sort.Strings(activeSources)
		if len(activeSources) == 0 {
			return platformerr.New(platformerr.CodeContentTicketEmpty,
				"사용할 수 있는 열람권이 없어요")
		}

		type sourceUsage struct {
			path fspath.Path
			doc  contentSourceUsageDoc
		}
		usages := make([]sourceUsage, 0, len(activeSources))
		for _, sourceKey := range activeSources {
			usagePath, err := l.paths.contentSourceUsage(sourceKey)
			if err != nil {
				return err
			}
			exists, snap, err := tx.Exists(usagePath)
			if err != nil {
				return err
			}
			usage := contentSourceUsageDoc{EntitlementID: entitlementID, SourceKey: sourceKey}
			if exists {
				if err := snap.DataTo(&usage); err != nil {
					return err
				}
				if usage.EntitlementID != entitlementID || usage.SourceKey != sourceKey ||
					usage.Consumed < 0 || usage.Consumed > unitsPerSource {
					return platformerr.New(platformerr.CodeLedgerStateInvalid,
						"열람권 source 사용 원장이 올바르지 않아요")
				}
			}
			usages = append(usages, sourceUsage{path: usagePath, doc: usage})
		}

		chosen := -1
		remaining := 0
		for index := range usages {
			capacity := unitsPerSource - usages[index].doc.Consumed
			remaining += capacity
			if chosen < 0 && capacity > 0 {
				chosen = index
			}
		}
		if chosen < 0 {
			return platformerr.New(platformerr.CodeContentTicketEmpty,
				"사용할 수 있는 열람권이 없어요")
		}

		now := l.now().UTC()
		usages[chosen].doc.Consumed++
		usages[chosen].doc.UpdatedAt = now
		remaining--
		if err := tx.Set(usages[chosen].path, usages[chosen].doc); err != nil {
			return err
		}
		if err := tx.Create(consumptionPath, contentConsumptionDoc{
			PlatformUserID: puid, EntitlementID: entitlementID, RequestKey: requestKey,
			SourceKey: usages[chosen].doc.SourceKey, Consumed: 1,
			Remaining: remaining, CreatedAt: now,
		}); err != nil {
			return err
		}
		result = ConsumptionResult{
			Applied: true, Remaining: remaining, SourceKey: usages[chosen].doc.SourceKey,
		}
		return nil
	})
	if err != nil {
		if code := platformerr.CodeOf(err); code != platformerr.CodeInternal {
			return ConsumptionResult{}, err
		}
		return ConsumptionResult{}, platformerr.Wrap(err, platformerr.CodeContentUnavailable,
			"열람권을 차감하지 못했어요")
	}
	return result, nil
}

// IsActive는 특정 entitlement를 원문 source를 노출하지 않고 확인한다.
func (l *Ledger) IsActive(ctx context.Context, puid, entitlementID string) (bool, error) {
	p, err := l.paths.internalEntitlement(puid, entitlementID)
	if err != nil {
		return false, err
	}
	snap, err := l.store.Get(ctx, p)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
			"보유 권한을 읽지 못했어요")
	}
	var doc entitlementDoc
	if err := snap.DataTo(&doc); err != nil {
		return false, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
			"보유 권한을 읽지 못했어요")
	}
	if doc.EntitlementID != "" && doc.EntitlementID != entitlementID {
		return false, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"보유 권한 원장이 올바르지 않아요")
	}
	return domain.IsActiveFrom(doc.Sources), nil
}

// IsSourceActive는 콘텐츠 열람권을 실제 차감한 구매 source가 아직 이 사용자에게
// 활성인지 확인한다. 환불되거나 다른 사용자에게 이전된 source는 false다.
func (l *Ledger) IsSourceActive(
	ctx context.Context,
	puid, entitlementID, sourceKey string,
) (bool, error) {
	if len(sourceKey) != 64 {
		return false, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"열람권 구매 source가 올바르지 않아요")
	}
	p, err := l.paths.internalEntitlement(puid, entitlementID)
	if err != nil {
		return false, err
	}
	snap, err := l.store.Get(ctx, p)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
			"열람권 구매 source를 읽지 못했어요")
	}
	var doc entitlementDoc
	if err := snap.DataTo(&doc); err != nil {
		return false, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
			"열람권 구매 source를 읽지 못했어요")
	}
	if doc.EntitlementID != "" && doc.EntitlementID != entitlementID {
		return false, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"열람권 entitlement 원장이 올바르지 않아요")
	}
	source, ok := doc.Sources[sourceKey]
	return ok && source.State == domain.StateActive, nil
}

func contentRequestDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
