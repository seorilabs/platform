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
