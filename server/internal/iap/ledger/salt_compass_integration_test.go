//go:build integration

package ledger

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/store"
)

func saltTestLedger(t *testing.T) (*Ledger, *time.Time) {
	t.Helper()
	_, base := starterStore(t)
	now := time.Unix(1793941200, 0).UTC()
	l := NewForApp(base.store, domain.EnvSandbox, saltAppID).WithClock(func() time.Time { return now })
	return l, &now
}
func saltTx(t *testing.T, l *Ledger, puid string, r SaltRequest) SaltReceipt {
	t.Helper()
	x, err := l.TransactSalt(context.Background(), puid, r)
	if err != nil {
		t.Fatal(err)
	}
	return x
}
func saltPurchase(id, sku string, at time.Time) domain.VerifiedPurchase {
	p := testPurchase(id, domain.StateActive, at)
	p.ProductID = sku
	return p
}
func TestSaltAtomicCreditReplayRefundDebtAndRecovery(t *testing.T) {
	l, now := saltTestLedger(t)
	ctx := context.Background()
	puid := "pu_salt_financial"
	saltTx(t, l, puid, SaltRequest{RequestID: "boot", Action: "bootstrap", LegacySeals: 5})
	p := saltPurchase("salt-m", "saltcompass_seal_m", *now)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: p.ProductID, Purchase: p})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, store.ErrAborted) {
			t.Fatal(err)
		}
	}
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: p.ProductID, Purchase: p}); err != nil {
		t.Fatal(err)
	}
	got, err := l.SaltSnapshot(ctx, puid)
	if err != nil || got.Available != 65 {
		t.Fatalf("grant=%+v error=%v", got, err)
	}
	first := saltTx(t, l, puid, SaltRequest{RequestID: "spend", Action: "spend_build", TargetID: "run_B1", ShipClass: "S17"})
	replay := saltTx(t, l, puid, SaltRequest{RequestID: "spend", Action: "spend_build", TargetID: "run_B1", ShipClass: "S17"})
	if first != replay || replay.Snapshot.Available != 5 {
		t.Fatalf("debit replay=%+v", replay)
	}
	if _, err := l.TransactSalt(ctx, puid, SaltRequest{RequestID: "other", Action: "spend_build", TargetID: "run_B1", ShipClass: "S17"}); err == nil {
		t.Fatal("duplicate effect debited twice")
	}
	p.State = domain.StateRevoked
	p.ObservedAt = now.Add(time.Hour)
	for range 2 {
		if err := l.RevokeByCanonicalID(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	got, err = l.SaltSnapshot(ctx, puid)
	if err != nil || got.Debt != 55 || got.Available != 0 {
		t.Fatalf("refund=%+v error=%v", got, err)
	}
	// 늦은 active 응답도 환불을 되돌리지 못한다.
	p.State = domain.StateActive
	p.ObservedAt = now.Add(2 * time.Hour)
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: p.ProductID, Purchase: p}); err == nil {
		t.Fatal("refunded active returned completion-eligible success")
	}
	saltTx(t, l, puid, SaltRequest{RequestID: "ad1", Action: "ad_reward", ClaimID: "claim1"}.WithVerifiedReward())
	p2 := saltPurchase("salt-m2", "saltcompass_seal_m", *now)
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: p2.ProductID, Purchase: p2}); err != nil {
		t.Fatal(err)
	}
	got, err = l.SaltSnapshot(ctx, puid)
	if err != nil || got.Available != 8 || got.Debt != 0 {
		t.Fatalf("income=%+v error=%v", got, err)
	}
	if _, err := l.TransactSalt(ctx, puid, SaltRequest{RequestID: "ad2", Action: "ad_reward", ClaimID: "claim1"}.WithVerifiedReward()); err == nil {
		t.Fatal("same claim credited twice")
	}
	// 재실행은 같은 app/environment/PUID의 잔액을 읽으며 추가 지급하지 않는다.
	fresh := NewForApp(l.store, domain.EnvSandbox, saltAppID)
	if got, err := fresh.SaltSnapshot(ctx, puid); err != nil || got.Available != 8 {
		t.Fatalf("recovery=%+v %v", got, err)
	}
}

func TestSaltStarterReservationPendingExpiryAndSecondChargeHold(t *testing.T) {
	l, now := saltTestLedger(t)
	ctx := context.Background()
	puid := "pu_salt_starter"
	saltTx(t, l, puid, SaltRequest{RequestID: "boot", Action: "bootstrap"})
	*now = now.Add(24 * time.Hour)
	saltTx(t, l, puid, SaltRequest{RequestID: "dispatch", Action: "expedition_dispatch", TargetID: "run_X1", DurationSeconds: 10800})
	*now = now.Add(3 * time.Hour)
	saltTx(t, l, puid, SaltRequest{RequestID: "collect", Action: "expedition_collect", TargetID: "run_X1"})
	saltTx(t, l, puid, SaltRequest{RequestID: "prepare", Action: "prepare_starter"})
	p := saltPurchase("salt-starter", saltStarterID, *now)
	p.State = domain.StatePending
	if err := l.RecordPending(ctx, GrantInput{PlatformUserID: puid, EntitlementID: p.ProductID, Purchase: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.TransactSalt(ctx, puid, SaltRequest{RequestID: "cancel", Action: "cancel_starter", TargetID: "prepare"}); err == nil {
		t.Fatal("cancelled accepted pending")
	}
	*now = now.Add(73 * time.Hour)
	p.State = domain.StateActive
	p.ObservedAt = *now
	if got, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: p.ProductID, Purchase: p}); err != nil || !got.Granted {
		t.Fatalf("delayed approval=%+v %v", got, err)
	}
	p2 := p
	p2.CanonicalID = "salt-starter-second"
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: p2.ProductID, Purchase: p2}); err == nil {
		t.Fatal("second distinct starter was granted/completed")
	}
	op, _ := l.paths.order(p2.Key())
	snap, err := l.store.Get(ctx, op)
	var order orderDoc
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.DataTo(&order); err != nil {
		t.Fatal(err)
	}
	if !order.SaltReconciliation || order.SaltGranted {
		t.Fatalf("second charge hold=%+v", order)
	}
	p.State = domain.StateRevoked
	p.ObservedAt = now.Add(time.Hour)
	if err := l.RevokeByCanonicalID(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := l.SaltSnapshot(ctx, puid)
	if err != nil || !got.Starter.Purchased || got.Starter.Active || got.Starter.Eligible || got.Available != 0 {
		t.Fatalf("starter refund=%+v %v", got, err)
	}
}

func TestSaltUnknownRefundAndOtherOwnerCannotDuplicate(t *testing.T) {
	l, now := saltTestLedger(t)
	ctx := context.Background()
	for _, uid := range []string{"owner", "other"} {
		saltTx(t, l, uid, SaltRequest{RequestID: "boot", Action: "bootstrap"})
	}
	p := saltPurchase("salt-owner", "saltcompass_seal_s", *now)
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: "owner", EntitlementID: p.ProductID, Purchase: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: "other", EntitlementID: p.ProductID, Purchase: p}); err == nil {
		t.Fatal("transferred spent currency")
	}
	got, _ := l.SaltSnapshot(ctx, "other")
	if got.Available != 0 {
		t.Fatal("other received currency")
	}
	p2 := saltPurchase("unknown-refund", "saltcompass_seal_l", *now)
	p2.State = domain.StateRevoked
	if err := l.RevokeByCanonicalID(ctx, p2); err != nil {
		t.Fatal(err)
	}
	p2.State = domain.StateActive
	p2.ObservedAt = now.Add(time.Hour)
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: "owner", EntitlementID: p2.ProductID, Purchase: p2}); err == nil {
		t.Fatal("tombstone allowed completion")
	}
	got, _ = l.SaltSnapshot(ctx, "owner")
	if got.Available != 10 || got.Debt != 0 {
		t.Fatalf("ungranted refund changed balance=%+v", got)
	}
}
