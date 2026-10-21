//go:build integration

package ledger

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

type passClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *passClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *passClock) Set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.now = t }

// 하루 한 번 수령, 동시 요청, 같은 요청 재시도, 재검증과 기기 이전, 재구매 연장,
// 환불 정지, 보류 결제의 시작 시각을 실제 Firestore 트랜잭션으로 검증한다. ADR 0036.
func TestPassClaimIsAtomicIdempotentAndStopsOnRefund(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	clock := &passClock{now: time.Now().UTC().Truncate(time.Second)}
	t0 := clock.Now()
	l.WithClock(clock.Now)
	ctx := context.Background()
	const ent = "amber_pass_30d"
	s := domain.PassSchedule{ImmediateAmount: 300, DailyAmount: 50, Days: 30, MissedDays: domain.MissedDaysForfeit}
	day := func(n int, extra time.Duration) time.Time { return t0.Add(time.Duration(n)*domain.PassDay + extra) }
	grant := func(puid string, p domain.VerifiedPurchase) {
		t.Helper()
		if _, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: ent, Purchase: p}); err != nil {
			t.Fatal(err)
		}
	}
	claim := func(puid, requestID string) (PassReceipt, error) {
		return l.ClaimPass(ctx, puid, ent, s, requestID)
	}
	expectCode := func(err error, want platformerr.Code) {
		t.Helper()
		if platformerr.CodeOf(err) != want {
			t.Fatalf("err=%v, want %s", err, want)
		}
	}

	owner := uniqueID("pu_pass")
	first := testPurchase(uniqueID("pass-a"), domain.StateActive, t0)
	grant(owner, first)
	status, err := l.PassSnapshot(ctx, owner, ent, s)
	if err != nil || !status.Claimable || status.ClaimableAmount != 50 || status.RemainingDays != 30 ||
		status.EndsAt != day(30, 0).Unix() {
		t.Fatalf("구매 직후: %+v %v", status, err)
	}

	// 같은 날 서로 다른 요청 8개 중 하나만 지급한다.
	clock.Set(t0.Add(time.Minute))
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := []PassReceipt{}
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := claim(owner, fmt.Sprintf("claim-day0-%02d", i))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				winners = append(winners, r)
				return
			}
			if platformerr.CodeOf(err) != platformerr.CodePassAlreadyClaimed {
				t.Errorf("동시 요청 오류: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(winners) != 1 || !winners[0].Applied || winners[0].Amount != 50 || winners[0].Days != 1 ||
		winners[0].Pass.Claimable || winners[0].Pass.NextClaimAt != day(1, 0).Unix() {
		t.Fatalf("동시 수령: %+v", winners)
	}
	// 응답이 끊긴 재시도는 같은 결과를 지급 없이 돌려준다.
	replay, err := claim(owner, winners[0].RequestID)
	if err != nil || replay.Applied || replay.Amount != 50 || replay.ClaimedAt != winners[0].ClaimedAt {
		t.Fatalf("재시도: %+v %v", replay, err)
	}
	_, err = l.ClaimPass(ctx, owner, "other_pass", s, winners[0].RequestID)
	expectCode(err, platformerr.CodePassReplayMismatch)

	// 같은 구매의 재검증은 시작 시각과 수령 기록을 바꾸지 않는다.
	clock.Set(t0.Add(2 * time.Hour))
	reverify := first
	reverify.ObservedAt = t0.Add(2 * time.Hour)
	grant(owner, reverify)
	_, err = claim(owner, "claim-day0-again")
	expectCode(err, platformerr.CodePassAlreadyClaimed)

	clock.Set(day(1, time.Minute))
	if r, err := claim(owner, "claim-day1"); err != nil || !r.Applied || r.Pass.ClaimedDays != 2 || r.Pass.RemainingDays != 28 {
		t.Fatalf("다음 날: %+v %v", r, err)
	}

	// 다른 기기의 새 사용자가 같은 구매를 복원하면 남은 날과 오늘 수령 기록이 함께 옮겨 간다.
	clock.Set(day(1, 2*time.Minute))
	device := uniqueID("pu_pass_device")
	grant(device, first)
	_, err = claim(device, "claim-device-day1")
	expectCode(err, platformerr.CodePassAlreadyClaimed)
	_, err = claim(owner, "claim-old-device")
	expectCode(err, platformerr.CodePassInactive)
	if moved, err := l.PassSnapshot(ctx, device, ent, s); err != nil || moved.ClaimedDays != 2 || moved.EndsAt != day(30, 0).Unix() {
		t.Fatalf("이전 뒤: %+v %v", moved, err)
	}

	// 이용 중 재구매는 끝나는 날만 30일 미룬다.
	clock.Set(day(1, 3*time.Minute))
	second := testPurchase(uniqueID("pass-b"), domain.StateActive, day(1, 3*time.Minute))
	grant(device, second)
	extended, err := l.PassSnapshot(ctx, device, ent, s)
	if err != nil || extended.EndsAt != day(60, 0).Unix() || extended.DailyAmount != 50 || extended.TotalDays != 60 {
		t.Fatalf("연장: %+v %v", extended, err)
	}

	// 첫 구매 환불은 그 구매의 남은 날만 멈춘다. 두 번째 구매 기간은 그대로다.
	clock.Set(day(2, 0))
	refunded := first
	refunded.State = domain.StateRevoked
	refunded.ObservedAt = day(2, 0)
	if err := l.RevokeByCanonicalID(ctx, refunded); err != nil {
		t.Fatal(err)
	}
	clock.Set(day(2, time.Minute))
	_, err = claim(device, "claim-after-refund")
	expectCode(err, platformerr.CodePassInactive)
	stopped, err := l.PassSnapshot(ctx, device, ent, s)
	if err != nil || stopped.Active || stopped.NextClaimAt != day(30, 0).Unix() || stopped.EndsAt != day(60, 0).Unix() {
		t.Fatalf("환불 뒤: %+v %v", stopped, err)
	}
	clock.Set(day(30, time.Minute))
	if r, err := claim(device, "claim-second-pass"); err != nil || !r.Applied || r.Pass.RemainingDays != 29 {
		t.Fatalf("두 번째 구매: %+v %v", r, err)
	}

	// 보류 결제는 마켓 구매 시각이 아니라 서버가 활성으로 확정한 시각부터 센다.
	clock.Set(t0)
	pendingOwner := uniqueID("pu_pass_pending")
	pending := testPurchase(uniqueID("pass-pending"), domain.StatePending, t0)
	if err := l.RecordPending(ctx, GrantInput{PlatformUserID: pendingOwner, EntitlementID: ent, Purchase: pending}); err != nil {
		t.Fatal(err)
	}
	clock.Set(day(3, 0))
	paid := pending
	paid.State = domain.StateActive
	paid.ObservedAt = day(3, 0)
	grant(pendingOwner, paid)
	late, err := l.PassSnapshot(ctx, pendingOwner, ent, s)
	if err != nil || late.EndsAt != day(33, 0).Unix() || !late.Claimable {
		t.Fatalf("보류 결제: %+v %v", late, err)
	}
}
