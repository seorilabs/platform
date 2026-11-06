//go:build integration

package ledger

import (
	"context"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"sync"
	"testing"
	"time"
)

func recoveryEligibleStarter(t *testing.T) (*Ledger, *time.Time, string) {
	t.Helper()
	l, now := saltTestLedger(t)
	uid := "independent_starter"
	saltTx(t, l, uid, SaltRequest{RequestID: "boot", Action: "bootstrap"})
	*now = now.Add(24 * time.Hour)
	saltTx(t, l, uid, SaltRequest{RequestID: "dispatch", Action: "expedition_dispatch", TargetID: "R1_X1", DurationSeconds: 14400})
	*now = now.Add(4 * time.Hour)
	saltTx(t, l, uid, SaltRequest{RequestID: "collect", Action: "expedition_collect", TargetID: "R1_X1"})
	saltTx(t, l, uid, SaltRequest{RequestID: "prepare", Action: "prepare_starter"})
	return l, now, uid
}

func TestSaltRegressionMigrationHoldRecoversAfterBootstrap(t *testing.T) {
	l, now := saltTestLedger(t)
	ctx := context.Background()
	uid := "independent_migration"
	p := saltPurchase("independent_migration_order", "saltcompass_seal_m", *now)
	in := GrantInput{PlatformUserID: uid, EntitlementID: p.ProductID, Purchase: p}
	if _, err := l.Grant(ctx, in); err == nil {
		t.Fatal("pre-migration purchase was not held")
	}
	saltTx(t, l, uid, SaltRequest{RequestID: "boot", Action: "bootstrap", LegacySeals: 5})
	res, err := l.Grant(ctx, in)
	if err != nil {
		t.Fatalf("same charged order must recover after bootstrap; got held error=%v", err)
	}
	got, err := l.SaltSnapshot(ctx, uid)
	if err != nil || !res.Granted || got.Available != 65 {
		t.Fatalf("recovery must preserve5 and grant60 once; granted=%v available=%d err=%v", res.Granted, got.Available, err)
	}
}

func TestSaltRegressionCancelledPendingReleasesUnusedStarter(t *testing.T) {
	l, now, uid := recoveryEligibleStarter(t)
	ctx := context.Background()
	p := saltPurchase("independent_pending_cancel", saltStarterID, *now)
	p.State = domain.StatePending
	in := GrantInput{PlatformUserID: uid, EntitlementID: p.ProductID, Purchase: p}
	if err := l.RecordPending(ctx, in); err != nil {
		t.Fatal(err)
	}
	p.State = domain.StateRevoked
	p.ObservedAt = now.Add(time.Minute)
	in.Purchase = p
	if _, err := l.Grant(ctx, in); err != nil {
		t.Fatal(err)
	}
	snap, err := l.SaltSnapshot(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Starter.Purchased || !snap.Starter.Eligible || snap.Starter.Reserved {
		t.Fatalf("verified cancelled unpaid pending must reopen remaining offer: purchased=%v eligible=%v reserved=%v", snap.Starter.Purchased, snap.Starter.Eligible, snap.Starter.Reserved)
	}
}

func TestSaltRegressionRefundRemovesCompletionOutbox(t *testing.T) {
	l, now := saltTestLedger(t)
	ctx := context.Background()
	uid := "independent_outbox"
	saltTx(t, l, uid, SaltRequest{RequestID: "boot", Action: "bootstrap"})
	p := saltPurchase("independent_outbox_order", "saltcompass_seal_s", *now)
	p.Platform = domain.PlatformGooglePlay
	p.Completion = domain.CompletionGoogleConsume
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: uid, EntitlementID: p.ProductID, Purchase: p}); err != nil {
		t.Fatal(err)
	}
	if err := l.Enqueue(ctx, p.Key(), p); err != nil {
		t.Fatal(err)
	}
	p.State = domain.StateRevoked
	p.ObservedAt = now.Add(time.Minute)
	if err := l.RevokeByCanonicalID(ctx, p); err != nil {
		t.Fatal(err)
	}
	_, found, err := l.ClaimNext(ctx, p.Platform)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("refunded Salt order still claimable for provider consume/finish")
	}
	// 완료 실패 응답이 환불 뒤 늦게 도착해도 큐가 다시 만들어지지 않는다.
	if err := l.Enqueue(ctx, p.Key(), p); err != nil {
		t.Fatal(err)
	}
	if _, found, err := l.ClaimNext(ctx, p.Platform); err != nil || found {
		t.Fatalf("late enqueue revived refund: found=%v err=%v", found, err)
	}
}

func TestSaltRegressionSandboxResetMoneyAndRightsAtomic(t *testing.T) {
	l, now := saltTestLedger(t)
	ctx := context.Background()
	uid := uniqueOperatorPUID()
	saltTx(t, l, uid, SaltRequest{RequestID: "boot", Action: "bootstrap", LegacySeals: 5})
	p := saltPurchase("independent_reset_order", "saltcompass_seal_m", *now)
	p.Platform = domain.PlatformAppStore
	p.Completion = domain.CompletionAppleFinish
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: uid, EntitlementID: p.ProductID, Purchase: p}); err != nil {
		t.Fatal(err)
	}
	saltTx(t, l, uid, SaltRequest{RequestID: "spend", Action: "spend_build", TargetID: "R1_B1", ShipClass: "S17"})
	*now = now.Add(time.Minute)
	input := SandboxResetInput{RequestID: "independent_reset_request", PlatformUserID: uid, AppID: saltAppID, ActorLogin: "independent-qa", Reason: AdminReasonInternalValidation}
	if _, err := l.MarkSandboxReset(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := l.MarkSandboxReset(ctx, input); err != nil {
		t.Fatal(err)
	}
	snap, err := l.SaltSnapshot(ctx, uid)
	if err != nil || snap.Available != 0 || snap.Debt != 55 {
		t.Fatalf("reset financial result available=%d debt=%d err=%v", snap.Available, snap.Debt, err)
	}
	active, err := l.ListActive(ctx, uid)
	if err != nil || len(active) != 0 {
		t.Fatalf("reset rights count=%d err=%v", len(active), err)
	}
	p.ObservedAt = now.Add(time.Hour)
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: uid, EntitlementID: p.ProductID, Purchase: p}); err == nil {
		t.Fatal("reset order returned completion-eligible success")
	}
}

func TestSaltRegressionConcurrentRefundSpendConservesBalance(t *testing.T) {
	l, now := saltTestLedger(t)
	ctx := context.Background()
	uid := "independent_race"
	saltTx(t, l, uid, SaltRequest{RequestID: "boot", Action: "bootstrap"})
	p := saltPurchase("independent_race_order", "saltcompass_seal_m", *now)
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: uid, EntitlementID: p.ProductID, Purchase: p}); err != nil {
		t.Fatal(err)
	}
	rev := p
	rev.State = domain.StateRevoked
	rev.ObservedAt = now.Add(time.Minute)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var spendErr, refundErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, spendErr = l.TransactSalt(ctx, uid, SaltRequest{RequestID: "spend", Action: "spend_build", TargetID: "R1_B1", ShipClass: "S17"})
	}()
	go func() { defer wg.Done(); <-start; refundErr = l.RevokeByCanonicalID(ctx, rev) }()
	close(start)
	wg.Wait()
	if refundErr != nil {
		t.Fatal(refundErr)
	}
	snap, err := l.SaltSnapshot(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if spendErr == nil {
		if snap.Available != 0 || snap.Debt != 60 {
			t.Fatalf("successful spend+refund must yield debt60; available=%d debt=%d", snap.Available, snap.Debt)
		}
	} else if platformerr.CodeOf(spendErr) == platformerr.CodePurchaseInvalid {
		if snap.Available != 0 || snap.Debt != 0 {
			t.Fatalf("rejected spend+refund must yield0; available=%d debt=%d", snap.Available, snap.Debt)
		}
	} else {
		t.Fatal(spendErr)
	}
}

func TestSaltRegressionOwnerHoldPreservesBothLedgers(t *testing.T) {
	l, now := saltTestLedger(t)
	ctx := context.Background()
	for _, uid := range []string{"independent_owner", "independent_other"} {
		saltTx(t, l, uid, SaltRequest{RequestID: "boot", Action: "bootstrap", LegacySeals: 5})
	}
	p := saltPurchase("independent_owner_order", "saltcompass_seal_l", *now)
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: "independent_owner", EntitlementID: p.ProductID, Purchase: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: "independent_other", EntitlementID: p.ProductID, Purchase: p}); err == nil {
		t.Fatal("owner mismatch not held")
	}
	owner, _ := l.SaltSnapshot(ctx, "independent_owner")
	other, _ := l.SaltSnapshot(ctx, "independent_other")
	if owner.Available != 285 || other.Available != 5 {
		t.Fatalf("hold changed ledgers: owner=%d other=%d", owner.Available, other.Available)
	}
	path, _ := l.saltPath("salt_compass_reconciliation", p.Key()+"\x00independent_other")
	if _, err := l.store.Get(ctx, path); err != nil {
		t.Fatal("owner hold not durable")
	}
}
