package ledger

import (
	"cloud.google.com/go/firestore"
	"errors"
	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
	"google.golang.org/api/iterator"
)

// CheckGuestAccountSwitch는 계정 연결 트랜잭션 안에서 구매 원장 유실을 막는다.
// 불변식 5: 환불되거나 모두 개봉한 구매도 이력이며, 원장을 합치거나 삭제하지 않는다.
// Apple sandbox와 production 모두 확인한다. 읽기 실패는 전환 허용이 아니다.
func CheckGuestAccountSwitch(tx *store.Tx, appID, puid string) error {
	for _, env := range []domain.Environment{domain.EnvProduction, domain.EnvSandbox} {
		paths := newAppPathBuilder(env, appID)
		state, err := paths.boxState(puid)
		if err != nil {
			return err
		}
		exists, _, err := tx.Exists(state)
		if err != nil {
			return err
		}
		if exists {
			return guestPurchaseConflict()
		}
		entitlements, err := paths.internalEntitlements(puid)
		if err != nil {
			return err
		}
		orders, err := paths.orders()
		if err != nil {
			return err
		}
		for _, query := range []struct {
			path  fspath.Path
			order bool
		}{{entitlements, false}, {orders, true}} {
			// 한 건이면 충분하므로 전체 원장을 읽지 않는다.
			it, err := tx.Query(query.path, func(q firestore.Query) firestore.Query {
				if query.order {
					q = q.Where("platformUserId", "==", puid)
				}
				return q.Limit(1)
			})
			if err != nil {
				return err
			}
			_, err = it.Next()
			it.Stop()
			if err == nil {
				return guestPurchaseConflict()
			}
			if !errors.Is(err, iterator.Done) {
				return err
			}
		}
	}
	return nil
}

func guestPurchaseConflict() error {
	return platformerr.New(platformerr.CodeAccountLinkConflict,
		"현재 계정에 구매 이력이 있어 다른 연결 계정으로 바꿀 수 없어요")
}
