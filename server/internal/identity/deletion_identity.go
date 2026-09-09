package identity

import (
	"context"
	"errors"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

// DeleteIdentityData는 구 API의 매핑 삭제 의미를 넓히지 않고 별도 삭제
// 처리에서 세션·연결 challenge까지 지운다. IAP 원장에는 접근하지 않는다.
func (r *StoreRepository) DeleteIdentityData(ctx context.Context, appID, uid, puid string) error {
	if appID == "" || uid == "" {
		return errors.New("identity: deletion target required")
	}
	if puid == "" {
		return nil
	}
	for _, collection := range []string{refreshCollection, accountChallengeCollection} {
		for {
			iter, err := r.store.Query(ctx, deletionStorePath(collection), func(q firestore.Query) firestore.Query {
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
				if err = r.store.Delete(ctx, deletionStorePath(collection+"/"+snap.Ref.ID)); err != nil {
					iter.Stop()
					return err
				}
				count++
			}
			iter.Stop()
			if count == 0 {
				break
			}
		}
	}
	return r.DeleteUser(ctx, appID, uid, puid)
}
