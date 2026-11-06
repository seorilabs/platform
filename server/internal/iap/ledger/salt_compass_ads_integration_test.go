//go:build integration

package ledger

import (
	"context"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
)

func TestSaltSSVClaimCannotCreditTwoWalletEnvironments(t *testing.T) {
	l, now := saltTestLedger(t)
	uid := "ad_env_owner"
	production := NewForApp(l.store, domain.EnvProduction, saltAppID).WithClock(func() time.Time { return *now })
	saltTx(t, l, uid, SaltRequest{RequestID: "sandbox-boot", Action: "bootstrap"})
	saltTx(t, production, uid, SaltRequest{RequestID: "production-boot", Action: "bootstrap"})
	r := SaltRequest{RequestID: "one-ad", Action: "ad_reward", ClaimID: "same-signed-ad"}.WithVerifiedReward()
	saltTx(t, l, uid, r)
	if _, err := production.TransactSalt(context.Background(), uid, r); err == nil {
		t.Fatal("same signed claim credited production after sandbox")
	}
	a, _ := l.SaltSnapshot(context.Background(), uid)
	b, _ := production.SaltSnapshot(context.Background(), uid)
	if a.Available+b.Available != 3 {
		t.Fatalf("shared claim minted %d", a.Available+b.Available)
	}
}

func TestSaltVerifiedTimerReturnsAbsoluteDeadlineAndReplayDoesNotHalveAgain(t *testing.T) {
	l, now := saltTestLedger(t)
	uid := "ad_timer_owner"
	saltTx(t, l, uid, SaltRequest{RequestID: "boot", Action: "bootstrap"})
	*now = now.Add(24 * time.Hour)
	saltTx(t, l, uid, SaltRequest{RequestID: "dispatch", Action: "expedition_dispatch", TargetID: "scope_X1", DurationSeconds: 14400})
	r := SaltRequest{RequestID: "timer-ad", Action: "ad_timer", TargetID: "scope_X1", TimerKind: "expedition", Deadline: now.Add(4 * time.Hour).Unix(), ClaimID: "timer-signed-ad"}.WithVerifiedReward()
	receipt := saltTx(t, l, uid, r)
	if receipt.EffectDeadline != now.Add(2*time.Hour).Unix() || receipt.Snapshot.Available != 0 {
		t.Fatalf("timer receipt=%+v", receipt)
	}
	*now = now.Add(time.Hour)
	if again, err := l.TransactSalt(context.Background(), uid, r); err != nil || again != receipt {
		t.Fatalf("timer replay changed: %v", err)
	}
	*now = time.Unix(receipt.EffectDeadline, 0)
	collected := saltTx(t, l, uid, SaltRequest{RequestID: "collect", Action: "expedition_collect", TargetID: "scope_X1"})
	if !collected.Snapshot.Starter.Eligible {
		t.Fatal("ad-shortened actual settlement did not open offer")
	}
	r.RequestID, r.TargetID = "other-request", "scope_X2"
	if _, err := l.TransactSalt(context.Background(), uid, r); err == nil {
		t.Fatal("same timer claim retargeted")
	}
}
