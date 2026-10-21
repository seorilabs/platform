package ledger

import (
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
)

// 지금 받을 수 있으면 nextClaimAt은 serverTime과 같은 초다. 다음 날 경계는 올림해
// 경계 직전 초에 받기를 눌러 409가 나지 않게 한다.
func TestPassStatusTimesAreUsableByClients(t *testing.T) {
	s := domain.PassSchedule{DailyAmount: 50, Days: 30, MissedDays: domain.MissedDaysForfeit}
	now := time.Date(2026, 10, 21, 0, 0, 5, 700_000_000, time.UTC)
	if got := passStatus("p", s, domain.PassPlan{ClaimableDays: 1, NextClaimAt: now}); got.NextClaimAt != now.Unix() || got.ClaimableAmount != 50 {
		t.Fatalf("claimable: %+v", got)
	}
	if got := passStatus("p", s, domain.PassPlan{NextClaimAt: now, EndsAt: now}); got.NextClaimAt != now.Unix()+1 || got.EndsAt != now.Unix()+1 {
		t.Fatalf("waiting: %+v", got)
	}
	if got := passStatus("p", s, domain.PassPlan{}); got.NextClaimAt != 0 || got.EndsAt != 0 {
		t.Fatalf("none: %+v", got)
	}
}
