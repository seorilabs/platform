package ads

import (
	"context"
	"errors"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/seorilabs/platform/server/internal/fspath"
)

// DeleteAccountData는 ads가 소유하는 원시 기록만 지운다. 삭제 중 쓰기는
// CheckAccountActive가 차단한다. 파생 키부터 지워 중간 실패 후에도 부모
// 요청 기록을 통해 재시도할 수 있도록 순서를 유지한다.
func (r *StoreRepository) DeleteAccountData(ctx context.Context, appID, puid string) error {
	if appID == "" || puid == "" {
		return errors.New("ads: deletion identity required")
	}
	if err := r.deleteOwned(ctx, claimRequestsCollection, appID, puid, func(snap *firestore.DocumentSnapshot) error {
		var req claimRequestDoc
		if err := snap.DataTo(&req); err != nil {
			return err
		}
		// 과거 ad_usage에는 소유자 필드가 없다. claim의 24시간 유효기간을
		// 기준으로 요청일과 다음 UTC 일자를 복원한다. 신규 행도 같은 키다.
		for _, day := range []time.Time{req.CreatedAt, req.CreatedAt.Add(24 * time.Hour)} {
			p, err := usagePath(ConfirmInput{AppID: appID, PlatformUserID: puid}, req.PlacementID, day.UTC().Format("2006-01-02"))
			if err != nil {
				return err
			}
			if err = r.store.Delete(ctx, p); err != nil {
				return err
			}
		}
		return r.deleteClaimTransactions(ctx, appID, req.ClaimID)
	}); err != nil {
		return err
	}
	if err := r.deleteOwned(ctx, claimsCollection, appID, puid, func(snap *firestore.DocumentSnapshot) error {
		var claim Claim
		if err := snap.DataTo(&claim); err != nil {
			return err
		}
		return r.deleteClaimTransactions(ctx, appID, claim.ClaimID)
	}); err != nil {
		return err
	}
	for _, col := range []string{policyCollection, grantsCollection, revocationsCollection} {
		if err := r.deleteOwned(ctx, col, appID, puid, nil); err != nil {
			return err
		}
	}
	return nil
}
func (r *StoreRepository) deleteClaimTransactions(ctx context.Context, appID, claimID string) error {
	col, err := fspath.Parse(transactionsCollection)
	if err != nil {
		return err
	}
	// transactionDoc는 기존 Firestore 필드가 Go 필드명과 같다.
	iter, err := r.store.Query(ctx, col, func(q firestore.Query) firestore.Query {
		return q.Where("ClaimID", "==", claimID).Where("AppID", "==", appID)
	})
	if err != nil {
		return err
	}
	defer iter.Stop()
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := path(transactionsCollection + "/" + snap.Ref.ID)
		if err != nil {
			return err
		}
		if err = r.store.Delete(ctx, p); err != nil {
			return err
		}
	}
}
func (r *StoreRepository) deleteOwned(ctx context.Context, collection, appID, puid string, before func(*firestore.DocumentSnapshot) error) error {
	col, err := fspath.Parse(collection)
	if err != nil {
		return err
	}
	for {
		iter, err := r.store.Query(ctx, col, func(q firestore.Query) firestore.Query {
			return q.Where("appId", "==", appID).Where("platformUserId", "==", puid).Limit(100)
		})
		if err != nil {
			return err
		}
		count := 0
		for {
			snap, err := iter.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				iter.Stop()
				return err
			}
			if before != nil {
				if err = before(snap); err != nil {
					iter.Stop()
					return err
				}
			}
			p, err := path(collection + "/" + snap.Ref.ID)
			if err == nil {
				err = r.store.Delete(ctx, p)
			}
			if err != nil {
				iter.Stop()
				return err
			}
			count++
		}
		iter.Stop()
		if count == 0 {
			return nil
		}
	}
}
