//go:build integration

package ledger

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/boxes"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// 동시 개봉·같은 requestId 재시도·환불 부채를 실제 Firestore 트랜잭션으로 검증한다.
// ADR 0029 2·3·6항.
func TestBoxOpenIsAtomicIdempotentAndTracksRefundDebt(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	cat, _, err := boxes.Load("bloomhand")
	if err != nil {
		t.Fatal(err)
	}
	version, err := cat.Active(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	puid := uniqueID("pu_box")
	now := time.Now().UTC().Truncate(time.Second)
	five := testPurchase(uniqueID("box5"), domain.StateActive, now)
	five.ProductID = "bloomhand_friend_box_5"
	one := testPurchase(uniqueID("box1"), domain.StateActive, now)
	one.ProductID = "bloomhand_friend_box_1"
	for _, in := range []GrantInput{
		{PlatformUserID: puid, EntitlementID: "friend_box_5", Purchase: five},
		{PlatformUserID: puid, EntitlementID: "friend_box_1", Purchase: one},
	} {
		if _, err := l.Grant(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	state, err := l.BoxSnapshot(ctx, puid, cat)
	if err != nil || state.Available != 6 || state.Debt != 0 {
		t.Fatalf("snapshot %+v %v", state, err)
	}

	// 같은 requestId 8번 동시 → 한 번만 차감
	var wg sync.WaitGroup
	results := make(chan BoxReceipt, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := l.OpenBox(ctx, puid, cat, version, "open-req-0001", boxes.CryptoRandom{})
			if err != nil {
				t.Error(err)
				return
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	applied := 0
	friend := ""
	for r := range results {
		if r.Applied {
			applied++
		}
		if friend != "" && r.FriendID != friend {
			t.Fatalf("replay changed result %s vs %s", r.FriendID, friend)
		}
		friend = r.FriendID
	}
	if applied != 1 {
		t.Fatalf("applied %d times", applied)
	}

	// 서로 다른 requestId 5번 동시 → 정확히 남은 수만큼만 열린다
	for i := range 5 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := l.OpenBox(ctx, puid, cat, version, uniqueID("open-many"), boxes.CryptoRandom{}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	state, err = l.BoxSnapshot(ctx, puid, cat)
	if err != nil || state.Available != 0 || state.Opened != 6 {
		t.Fatalf("after 6 opens %+v %v", state, err)
	}
	if _, err := l.OpenBox(ctx, puid, cat, version, "open-req-empty", boxes.CryptoRandom{}); platformerr.CodeOf(err) != platformerr.CodeBoxEmpty {
		t.Fatalf("empty: %v", err)
	}

	// 5개짜리 환불 → 이미 연 상자는 부채가 되고 개봉이 막힌다
	five.State = domain.StateRevoked
	five.ObservedAt = now.Add(time.Second)
	if err := l.RevokeByCanonicalID(ctx, five); err != nil {
		t.Fatal(err)
	}
	state, err = l.BoxSnapshot(ctx, puid, cat)
	if err != nil || state.Debt == 0 || state.Available >= 0 {
		t.Fatalf("debt %+v %v", state, err)
	}
	// 새 구매가 부채를 먼저 메운다
	again := testPurchase(uniqueID("box5b"), domain.StateActive, now.Add(2*time.Second))
	again.ProductID = "bloomhand_friend_box_5"
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: "friend_box_5", Purchase: again}); err != nil {
		t.Fatal(err)
	}
	after, err := l.BoxSnapshot(ctx, puid, cat)
	if err != nil || after.Available != state.Available+5 {
		t.Fatalf("repay %+v %v", after, err)
	}
	if after.Available <= 0 {
		if _, err := l.OpenBox(ctx, puid, cat, version, "open-req-debt", boxes.CryptoRandom{}); platformerr.CodeOf(err) != platformerr.CodeBoxDebt {
			t.Fatalf("debt open: %v", err)
		}
	}
}
