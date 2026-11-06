//go:build integration

package ledger

import (
	"context"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

func TestSaltCollectClockSkewRetriesSameRequestAfterReturn(t *testing.T) {
	l, now := saltTestLedger(t)
	uid := "timing_collect"
	saltTx(t, l, uid, SaltRequest{RequestID: "boot", Action: "bootstrap"})
	*now = now.Add(24 * time.Hour)
	saltTx(t, l, uid, SaltRequest{RequestID: "dispatch", Action: "expedition_dispatch", TargetID: "X1", DurationSeconds: 10800})
	*now = now.Add(10800*time.Second - time.Second)
	r := SaltRequest{RequestID: "collect", Action: "expedition_collect", TargetID: "X1"}
	if _, err := l.TransactSalt(context.Background(), uid, r); platformerr.CodeOf(err) != platformerr.CodeSaltExpeditionPending {
		t.Fatalf("early collect lost retryable state: %v", err)
	}
	*now = now.Add(time.Second)
	receipt, err := l.TransactSalt(context.Background(), uid, r)
	if err != nil || !receipt.Snapshot.Starter.Eligible {
		t.Fatalf("same request failed to open eligible settlement: %v", err)
	}
	if again, err := l.TransactSalt(context.Background(), uid, r); err != nil || again != receipt {
		t.Fatalf("settlement replay changed: %v", err)
	}
}

func TestSaltLegacyExpeditionSpendAndExpiredTimerReplay(t *testing.T) {
	l, now := saltTestLedger(t)
	uid := "timing_legacy"
	saltTx(t, l, uid, SaltRequest{RequestID: "boot", Action: "bootstrap", LegacySeals: 40})
	r := SaltRequest{RequestID: "spend-old", Action: "spend_expedition", TargetID: "old_X1", Deadline: now.Add(time.Hour).Unix()}
	receipt, err := l.TransactSalt(context.Background(), uid, r)
	if err != nil || receipt.Snapshot.Available != 30 {
		t.Fatalf("old expedition blocked fixed-cost spend: %v", err)
	}
	*now = now.Add(2 * time.Hour)
	if replay, err := l.TransactSalt(context.Background(), uid, r); err != nil || replay != receipt {
		t.Fatalf("committed spend became invalid after timer expiry: %v", err)
	}
	if _, err := l.TransactSalt(context.Background(), uid, SaltRequest{RequestID: "another", Action: "spend_build", TargetID: "B1", ShipClass: "S08", Deadline: now.Add(-time.Second).Unix()}); err == nil {
		t.Fatal("expired build charged")
	}
	if _, err := l.TransactSalt(context.Background(), uid, SaltRequest{RequestID: "collect-old", Action: "expedition_collect", TargetID: "old_X1"}); err == nil {
		t.Fatal("legacy spend forged starter settlement proof")
	}
	got, _ := l.SaltSnapshot(context.Background(), uid)
	if got.Available != 30 || got.Starter.StartsAt != 0 {
		t.Fatal("expiry or legacy target changed money/offer")
	}
}

func TestSaltPrelaunchCancelFencesDelayedPrepare(t *testing.T) {
	l, _, uid := recoveryEligibleStarter(t)
	ctx := context.Background()
	// 이전 예약 A가 아직 취소되지 않았어도 버린 준비 B의 tombstone은 남긴다.
	saltTx(t, l, uid, SaltRequest{RequestID: "cancel-delayed", Action: "cancel_starter", TargetID: "delayed-prepare"})
	if still, _ := l.SaltSnapshot(ctx, uid); !still.Starter.Reserved {
		t.Fatal("cancelling B removed active A")
	}
	saltTx(t, l, uid, SaltRequest{RequestID: "cancel-original", Action: "cancel_starter", TargetID: "prepare"})
	// A second prelaunch was cancelled before its delayed network request arrived.
	if _, err := l.TransactSalt(ctx, uid, SaltRequest{RequestID: "delayed-prepare", Action: "prepare_starter"}); err == nil {
		t.Fatal("late prepare recreated abandoned reservation")
	}
	receipt := saltTx(t, l, uid, SaltRequest{RequestID: "fresh-prepare", Action: "prepare_starter"})
	if !receipt.Snapshot.Starter.Reserved {
		t.Fatal("fresh checkout could not reserve")
	}
}
