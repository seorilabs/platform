package ledger

import (
	"context"
	"errors"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

var errReconcileOwnerChanged = errors.New("reconcile order owner changed")

const reconcileOwnerRetryLimit = 3

// ReconcileResult는 알림 반영 결과다.
type ReconcileResult struct {
	// Known이면 우리가 아는 주문이었다.
	Known bool
	// PlatformUserID는 반영된 소유자다. Known일 때만 채워진다.
	PlatformUserID string
	EntitlementID  string
}

// ReconcileByCanonicalID는 마켓 알림을 기존 주문에 반영한다.
//
// 불변식 10이다. 알림은 이미 아는 주문의 상태만 재조정한다.
// 알림만으로 신규 지급을 하지 않는다.
//
// 이유가 있다. 알림에는 platform_user_id가 없다. 누구에게 줄지 모르는
// 지급은 할 수 없고, 추측해서 주면 남의 계정에 물건이 생긴다.
// 정상 구매는 클라이언트가 검증 요청을 보내는 경로로 들어온다.
//
// 모르는 주문의 환불은 tombstone으로 남긴다. 마켓이 환불을 먼저
// 알리고 우리가 그 구매를 아직 모르는 경우가 실제로 있다.
// 나중에 그 구매가 검증되면 stale 억제가 재지급을 막는다.
func (l *Ledger) ReconcileByCanonicalID(
	ctx context.Context,
	p domain.VerifiedPurchase,
) (ReconcileResult, error) {
	return l.reconcileByCanonicalID(ctx, p, nil)
}

// reconcileByCanonicalID의 afterOwnerRead는 transaction 밖 owner 조회와
// 상태 반영 사이의 경합을 실제 Firestore에서 재현하는 테스트 seam이다.
// production 경로는 nil을 넘기며 소유권 결정에는 관여하지 않는다.
func (l *Ledger) reconcileByCanonicalID(
	ctx context.Context,
	p domain.VerifiedPurchase,
	afterOwnerRead func(attempt int, owner string) error,
) (ReconcileResult, error) {
	if p.CanonicalID == "" {
		return ReconcileResult{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"주문 식별자가 비어 있어요")
	}

	orderKey := domain.OrderKey(p.Platform, p.CanonicalID)
	for attempt := 0; attempt < reconcileOwnerRetryLimit; attempt++ {
		owner, entID, known, err := l.orderOwner(ctx, orderKey)
		if err != nil {
			return ReconcileResult{}, err
		}

		if !known {
			// 모르는 주문이다. 환불이면 tombstone을 남기고,
			// 그 외에는 아무것도 하지 않는다.
			if p.State == domain.StateRevoked {
				if err := l.RevokeByCanonicalID(ctx, p.Platform, p.CanonicalID, p.ObservedAt); err != nil {
					return ReconcileResult{}, err
				}
			}
			return ReconcileResult{}, nil
		}
		if afterOwnerRead != nil {
			if err := afterOwnerRead(attempt, owner); err != nil {
				return ReconcileResult{}, err
			}
		}

		// 아는 주문이다. 원래 소유자에게 그대로 반영한다.
		//
		// 소유자는 transaction 밖에서 읽었으므로 Grant transaction에서
		// expected owner로 다시 묶는다. 그 사이 앱 복원이 A→B로 이전되면
		// 오래된 A 스냅샷으로 B→A를 되돌리지 않고 현재 소유자를 재조회한다.
		// 웹훅에는 소유권을 옮길 구매 증명이 없다. 불변식 4, 10.
		if _, err := l.grantExpectedOwner(ctx, GrantInput{
			PlatformUserID: owner,
			EntitlementID:  entID,
			Purchase:       p,
		}, owner); err != nil {
			if errors.Is(err, errReconcileOwnerChanged) {
				continue
			}
			return ReconcileResult{}, err
		}

		return ReconcileResult{
			Known:          true,
			PlatformUserID: owner,
			EntitlementID:  entID,
		}, nil
	}

	// 소유권 변경이 계속되는 동안 임의의 한 사용자를 선택하지 않는다.
	// event_busy는 웹훅 lease를 풀어 마켓 재전송으로 다시 시도하게 한다.
	return ReconcileResult{}, platformerr.New(platformerr.CodeEventBusy,
		"주문 소유권이 변경 중이에요")
}

// orderOwner는 주문의 소유자를 읽는다.
//
// tombstone은 소유자를 모르는 환불 기록이라 아는 주문으로 치지 않는다.
func (l *Ledger) orderOwner(ctx context.Context, orderKey string) (puid, entID string, known bool, err error) {
	path, err := l.paths.order(orderKey)
	if err != nil {
		return "", "", false, err
	}

	snap, err := l.store.Get(ctx, path)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", "", false, nil
		}
		return "", "", false, err
	}

	var doc orderDoc
	if err := snap.DataTo(&doc); err != nil {
		return "", "", false, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
			"주문 원장을 읽지 못했어요")
	}

	if doc.Tombstone || doc.PlatformUserID == "" || doc.EntitlementID == "" {
		return "", "", false, nil
	}
	return doc.PlatformUserID, doc.EntitlementID, true, nil
}
