//go:build integration

package ledger

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
)

func TestEconomyConcurrentCreditSpendReplayAndRefund(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	c, e := LizardEconomyCatalog()
	if e != nil {
		t.Fatal(e)
	}
	c.Enabled = true
	c.LaunchAt = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).Unix()
	l.WithAppID("lizard-tycoon").WithEconomyCatalog(c)
	ctx := context.Background()
	puid := uniqueID("pu_economy")
	now := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("economy-order"), domain.StateActive, now)
	purchase.ProductID = "crystal_1000"
	input := GrantInput{PlatformUserID: puid, EntitlementID: "crystal_1000", Purchase: purchase}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := l.Grant(ctx, input); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	snap, e := l.EconomySnapshot(ctx, puid)
	if e != nil || snap.State.Paid != 1000 {
		t.Fatalf("credit %+v %v", snap.State, e)
	}
	req := EconomyRequest{RequestID: "replay_request_12345", Action: "draw", CollectionID: "tidal", Count: 10}
	first, e := l.TransactEconomy(ctx, puid, req)
	if e != nil {
		t.Fatal(e)
	}
	c.Enabled = false
	l.WithEconomyCatalog(c)
	second, e := l.TransactEconomy(ctx, puid, req)
	if e != nil || first.State.Version != second.State.Version || first.Draws[0] != second.Draws[0] {
		t.Fatalf("replay %+v %v", second, e)
	}
	snap, e = l.EconomySnapshot(ctx, puid)
	if e != nil || snap.State.Paid != 0 || snap.State.Tokens != 10 {
		t.Fatalf("spent %+v %v", snap.State, e)
	}
	c.Enabled = true
	l.WithEconomyCatalog(c)
	req.Count = 1
	if _, e := l.TransactEconomy(ctx, puid, req); e == nil {
		t.Fatal("replay mismatch accepted")
	}
	other := input
	other.PlatformUserID = uniqueID("pu_other")
	if _, e := l.Grant(ctx, other); e == nil {
		t.Fatal("wallet receipt changed owner")
	}
	purchase.State = domain.StateRevoked
	purchase.ObservedAt = now.Add(time.Second)
	for range 2 {
		if e := l.RevokeByCanonicalID(ctx, purchase); e != nil {
			t.Fatal(e)
		}
	}
	snap, e = l.EconomySnapshot(ctx, puid)
	if e != nil || snap.State.Paid != -1000 || snap.State.Tokens != 10 {
		t.Fatalf("refund %+v %v", snap.State, e)
	}
	if _, e := l.Grant(ctx, input); e != nil {
		t.Fatal(e)
	}
	snap, e = l.EconomySnapshot(ctx, puid)
	if e != nil || snap.State.Paid != -1000 {
		t.Fatalf("refund replay recredited %+v %v", snap.State, e)
	}
}

func TestEconomyAcceptedPendingOrderCompletesAfterSalesClose(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	c, err := LizardEconomyCatalog()
	if err != nil {
		t.Fatal(err)
	}
	c.Enabled = true
	c.LaunchAt = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).Unix()
	l.WithAppID("lizard-tycoon").WithEconomyCatalog(c)
	ctx := context.Background()
	puid := uniqueID("pu_pending_economy")
	purchase := testPurchase(uniqueID("pending-crystal"), domain.StatePending, time.Now())
	purchase.ProductID = "crystal_300"
	in := GrantInput{PlatformUserID: puid, EntitlementID: "crystal_300", Purchase: purchase}
	if err := l.RecordPending(ctx, in); err != nil {
		t.Fatal(err)
	}
	c.Enabled = false
	l.WithEconomyCatalog(c)
	fresh := in
	fresh.Purchase = testPurchase(uniqueID("new-crystal"), domain.StatePending, time.Now())
	fresh.Purchase.ProductID = "crystal_300"
	if err := l.RecordPending(ctx, fresh); err == nil {
		t.Fatal("new pending order accepted after sales close")
	}
	fresh.Purchase.State = domain.StateActive
	if _, err := l.Grant(ctx, fresh); err == nil {
		t.Fatal("new completed order accepted after sales close")
	}
	in.Purchase.State = domain.StateActive
	in.Purchase.ObservedAt = in.Purchase.ObservedAt.Add(time.Minute)
	for range 2 {
		if _, err := l.Grant(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := l.EconomySnapshot(ctx, puid)
	if err != nil || snap.State.Paid != 300 {
		t.Fatalf("accepted order not credited exactly once: %+v, %v", snap.State, err)
	}
	in.Purchase.State = domain.StateRevoked
	in.Purchase.ObservedAt = in.Purchase.ObservedAt.Add(time.Minute)
	if err := l.RevokeByCanonicalID(ctx, in.Purchase); err != nil {
		t.Fatal(err)
	}
	snap, err = l.EconomySnapshot(ctx, puid)
	if err != nil || snap.State.Paid != 0 {
		t.Fatalf("closed-sale refund not processed: %+v, %v", snap.State, err)
	}
}

func TestEconomyPlayTestPurchaseUsesOnlyEnrolledTestWallet(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	c, err := LizardEconomyCatalog()
	if err != nil {
		t.Fatal(err)
	}
	c.Enabled = false
	c.LaunchAt = 0
	c.TestEnabled = true
	c.TestLaunchAt = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).Unix()
	l.WithAppID("lizard-tycoon").WithEconomyCatalog(c)
	ctx := context.Background()
	puid := uniqueID("pu_test_wallet")
	testFlag := true
	purchase := testPurchase(uniqueID("test-crystal"), domain.StateActive, time.Now())
	purchase.ProductID = "crystal_300"
	purchase.IsTestPurchase = &testFlag
	in := GrantInput{PlatformUserID: puid, EntitlementID: "crystal_300", Purchase: purchase}
	if _, err := l.Grant(ctx, in); err == nil {
		t.Fatal("unregistered test account received currency")
	}
	if _, err := l.SetEconomyTester(ctx, puid, "operator", true); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := l.Grant(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	lookup, err := l.LookupEconomy(ctx, puid)
	if err != nil || lookup.Found || !lookup.TestFound || lookup.TestState.Paid != 300 {
		t.Fatalf("test credit leaked into production: %+v, %v", lookup, err)
	}
	req := EconomyRequest{RequestID: uniqueID("test-draw"), Action: "draw", CollectionID: "tidal", Count: 1}
	if _, err := l.TransactEconomy(ctx, puid, req); err != nil {
		t.Fatal(err)
	}
	c.TestEnabled = false
	l.WithEconomyCatalog(c)
	if _, err := l.TransactEconomy(ctx, puid, req); err != nil {
		t.Fatalf("시험 기능 중지 뒤 접수된 소비 재조회: %v", err)
	}
	if _, err := l.TransactEconomy(ctx, puid, EconomyRequest{RequestID: uniqueID("closed-draw"), Action: "draw", CollectionID: "tidal", Count: 1}); err == nil {
		t.Fatal("시험 기능 중지 뒤 새 소비가 처리됐다")
	}
	lookup, err = l.LookupEconomy(ctx, puid)
	if err != nil || lookup.TestState.Paid != 200 || lookup.Found {
		t.Fatalf("test spend: %+v, %v", lookup, err)
	}
	if _, err := l.SetEconomyTester(ctx, puid, "operator", false); err != nil {
		t.Fatal(err)
	}
	// Public launch can open after the tester is removed. A saved request must
	// still replay from the test ledger and cannot be reused with new contents.
	c.Enabled = true
	c.LaunchAt = c.TestLaunchAt
	l.WithEconomyCatalog(c)
	if _, err := l.TransactEconomy(ctx, puid, req); err != nil {
		t.Fatalf("test spend replay after unenrollment: %v", err)
	}
	changed := req
	changed.Count = 10
	if _, err := l.TransactEconomy(ctx, puid, changed); err == nil {
		t.Fatal("test request ID reused in production")
	}
	in.Purchase.State = domain.StateRevoked
	in.Purchase.ObservedAt = in.Purchase.ObservedAt.Add(time.Minute)
	if err := l.RevokeByCanonicalID(ctx, in.Purchase); err != nil {
		t.Fatal(err)
	}
	lookup, err = l.LookupEconomy(ctx, puid)
	if err != nil || lookup.TestState.Paid != -100 || lookup.Found {
		t.Fatalf("test refund: %+v, %v", lookup, err)
	}
}

func TestEconomyTestRechargeAndStarterOnce(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	c, err := LizardEconomyCatalog()
	if err != nil {
		t.Fatal(err)
	}
	c.Enabled = false
	c.TestEnabled = true
	c.TestLaunchAt = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).Unix()
	l.WithAppID("lizard-tycoon").WithEconomyCatalog(c)
	ctx, puid := context.Background(), uniqueID("pu_recharge")
	if _, err := l.SetEconomyTester(ctx, puid, "operator", true); err != nil {
		t.Fatal(err)
	}
	flag := true
	grant := func(id, product string) error {
		purchase := testPurchase(uniqueID(id), domain.StateActive, time.Now())
		purchase.ProductID, purchase.IsTestPurchase = product, &flag
		_, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: product, Purchase: purchase})
		return err
	}
	if err := grant("recharge-1", "crystal_300"); err != nil {
		t.Fatal(err)
	}
	if err := grant("recharge-2", "crystal_300"); err != nil {
		t.Fatal(err)
	}
	if err := grant("starter-1", "crystal_starter"); err != nil {
		t.Fatal(err)
	}
	if err := grant("starter-2", "crystal_starter"); err == nil {
		t.Fatal("second starter credited")
	}
	lookup, err := l.LookupEconomy(ctx, puid)
	if err != nil || lookup.Found || !lookup.TestFound || lookup.TestState.Paid != 900 || !lookup.TestState.StarterPurchased || lookup.TestState.StarterChoices != 1 {
		t.Fatalf("test recharge/starter: %+v, %v", lookup, err)
	}
}
func TestEconomyConcurrentDifferentRequestsCannotOverspend(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	c, _ := LizardEconomyCatalog()
	c.Enabled = true
	c.LaunchAt = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).Unix()
	l.WithAppID("lizard-tycoon").WithEconomyCatalog(c)
	ctx := context.Background()
	puid := uniqueID("pu_spend")
	purchase := testPurchase(uniqueID("crystal-order"), domain.StateActive, time.Now())
	purchase.ProductID = "crystal_300"
	if _, e := l.Grant(ctx, GrantInput{puid, "crystal_300", purchase}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	success := make(chan bool, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := l.TransactEconomy(ctx, puid, EconomyRequest{RequestID: uniqueID("draw_request"), Action: "draw", CollectionID: "tidal", Count: 1})
			success <- e == nil
		}()
	}
	wg.Wait()
	close(success)
	count := 0
	for v := range success {
		if v {
			count++
		}
	}
	snap, e := l.EconomySnapshot(ctx, puid)
	if e != nil || count != 3 || snap.State.Paid != 0 || snap.State.Tokens != 3 {
		t.Fatalf("count=%d state=%+v err=%v", count, snap.State, e)
	}
}
