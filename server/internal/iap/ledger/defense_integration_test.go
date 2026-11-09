//go:build integration

package ledger

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// This suite refuses a real Firestore endpoint even when the developer has active credentials.
func newDefenseTestLedger(t *testing.T) (*Ledger, func()) {
	t.Helper()
	if !strings.HasPrefix(os.Getenv("FIRESTORE_EMULATOR_HOST"), "127.0.0.1:") || !strings.HasPrefix(os.Getenv("GOOGLE_CLOUD_PROJECT"), "demo-") {
		t.Skip("defense integration requires loopback Firestore emulator and demo project")
	}
	base, done := newTestLedger(t)
	return NewForApp(base.store, domain.EnvSandbox, "mechanical-frontier"), done
}
func defenseGrant(t *testing.T, l *Ledger, puid string, units int, now time.Time) domain.VerifiedPurchase {
	t.Helper()
	p := testPurchase(uniqueID("defense-pack"), domain.StateActive, now)
	p.ProductID = fmt.Sprintf("crystals_%d", units)
	if _, e := l.Grant(context.Background(), GrantInput{PlatformUserID: puid, EntitlementID: fmt.Sprintf("defense_crystals_%d", units), Purchase: p}); e != nil {
		t.Fatal(e)
	}
	return p
}
func defenseSnapshot(t *testing.T, l *Ledger, puid string) DefenseSnapshot {
	t.Helper()
	out, e := l.DefenseSnapshot(context.Background(), puid)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func defenseRequest(action string) DefenseRequest {
	return DefenseRequest{RequestID: uniqueID("action"), ActionID: action, CatalogVersion: "defense-1"}
}

func TestDefenseConcurrentReplayRefundAndRecovery(t *testing.T) {
	l, done := newDefenseTestLedger(t)
	defer done()
	ctx := context.Background()
	puid := uniqueID("pu-defense")
	now := time.Now().UTC().Truncate(time.Second)
	before := defenseSnapshot(t, l, puid)
	if before.State.Balance != 0 || !before.State.Enabled {
		t.Fatalf("empty %+v", before.State)
	}
	purchase := defenseGrant(t, l, puid, 100, now)
	funded := defenseSnapshot(t, l, puid)
	if funded.State.Balance != 100 || funded.State.Version <= before.State.Version {
		t.Fatalf("funding did not advance state %+v -> %+v", before.State, funded.State)
	}
	req := defenseRequest("damage")
	results := make(chan DefenseReceipt, 8)
	errors := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := l.ActDefense(ctx, puid, req)
			if e != nil {
				errors <- e
				return
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for e := range errors {
		t.Error(e)
	}
	var first DefenseReceipt
	received := 0
	for r := range results {
		if received == 0 {
			first = r
		} else if !reflect.DeepEqual(r, first) {
			t.Fatalf("concurrent replay changed receipt %#v vs %#v", first, r)
		}
		received++
	}
	if received != 8 || first.Cost != 25 || first.State.Balance != 75 || first.State.Upgrades["damage"] != 1 {
		t.Fatalf("replayed %d times result %+v", received, first)
	}
	after := defenseSnapshot(t, l, puid)
	if after.State.Balance != 75 || after.State.Upgrades["damage"] != 1 {
		t.Fatalf("not exactly one spend %+v", after.State)
	}
	changed := req
	changed.ActionID = "core"
	if _, e := l.ActDefense(ctx, puid, changed); e == nil {
		t.Fatal("request ID was reused with another action")
	}
	second, e := l.ActDefense(ctx, puid, defenseRequest("damage"))
	if e != nil || second.Cost != 35 || second.State.Balance != 40 || second.State.Upgrades["damage"] != 2 {
		t.Fatalf("second %+v err=%v", second, e)
	}
	replay, e := l.ActDefense(ctx, puid, req)
	if e != nil || !reflect.DeepEqual(replay, first) {
		t.Fatalf("later state replaced first receipt %+v err=%v", replay, e)
	}
	if _, e = l.ActDefense(ctx, puid, defenseRequest("storm")); platformerr.CodeOf(e) != platformerr.CodeContentTicketEmpty {
		t.Fatalf("overspend err=%v", e)
	}
	rejected := defenseSnapshot(t, l, puid)
	if rejected.State.Balance != 40 || rejected.State.Version != second.State.Version {
		t.Fatalf("rejected request mutated state %+v", rejected.State)
	}
	// Reverify the same source: consumed units never become a fresh pack.
	if _, e = l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: "defense_crystals_100", Purchase: purchase}); e != nil {
		t.Fatal(e)
	}
	if got := defenseSnapshot(t, l, puid); got.State.Balance != 40 {
		t.Fatalf("duplicate proof minted funds %+v", got.State)
	}
	refunded := purchase
	refunded.State = domain.StateRevoked
	refunded.ObservedAt = now.Add(time.Second)
	if e = l.RevokeByCanonicalID(ctx, refunded); e != nil {
		t.Fatal(e)
	}
	debt := defenseSnapshot(t, l, puid)
	if debt.State.Balance != -60 || debt.State.Debt != 60 || debt.State.Enabled || debt.State.Version <= second.State.Version {
		t.Fatalf("refund not reflected %+v", debt.State)
	}
	// Transport lost the response before refund. Replay remains immutable; the newer snapshot wins.
	replay, e = l.ActDefense(ctx, puid, req)
	if e != nil || !reflect.DeepEqual(replay, first) || debt.State.Version <= replay.State.Version {
		t.Fatalf("refund/replay version ordering snapshot=%+v receipt=%+v err=%v", debt.State, replay, e)
	}
	if _, e = l.ActDefense(ctx, puid, defenseRequest("core")); e == nil {
		t.Fatal("refunded debt spent")
	}
	// Older active observation cannot undo the refund.
	if _, e = l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: "defense_crystals_100", Purchase: purchase}); e != nil {
		t.Fatal(e)
	}
	if stale := defenseSnapshot(t, l, puid); stale.State.Balance != debt.State.Balance || stale.State.Enabled {
		t.Fatalf("stale grant revived refund %+v", stale.State)
	}
	defenseGrant(t, l, puid, 550, now.Add(2*time.Second))
	recovered := defenseSnapshot(t, l, puid)
	if recovered.State.Balance != 490 || !recovered.State.Enabled || recovered.State.Version <= debt.State.Version {
		t.Fatalf("recharge did not repay debt %+v", recovered.State)
	}
	bought, e := l.ActDefense(ctx, puid, defenseRequest("siege"))
	if e != nil || bought.State.Balance != 190 || len(bought.State.Weapons) != 1 || bought.State.Weapons[0] != "siege" {
		t.Fatalf("weapon %+v err=%v", bought, e)
	}
}

func TestDefenseConcurrentUniqueRequestsNeverOverdraw(t *testing.T) {
	l, done := newDefenseTestLedger(t)
	defer done()
	puid := uniqueID("pu-defense-race")
	defenseGrant(t, l, puid, 100, time.Now().UTC())
	var successes atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := l.ActDefense(context.Background(), puid, defenseRequest("core"))
			if e == nil {
				successes.Add(1)
			} else if platformerr.CodeOf(e) != platformerr.CodeContentTicketEmpty {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	s := defenseSnapshot(t, l, puid)
	if successes.Load() != 2 || s.State.Balance != 40 || s.State.Upgrades["core"] != 2 {
		t.Fatalf("success=%d state=%+v", successes.Load(), s.State)
	}
}
func TestDefenseScopeIsolationAndUnlimitedRevives(t *testing.T) {
	l, done := newDefenseTestLedger(t)
	defer done()
	puid := uniqueID("pu-defense-scope")
	now := time.Now().UTC()
	defenseGrant(t, l, puid, 6200, now)
	prod := NewForApp(l.store, domain.EnvProduction, "mechanical-frontier")
	if s := defenseSnapshot(t, prod, puid); s.State.Balance != 0 {
		t.Fatalf("sandbox funds crossed environment %+v", s.State)
	}
	defenseGrant(t, prod, puid, 550, now)
	if s := defenseSnapshot(t, l, puid); s.State.Balance != 6200 {
		t.Fatalf("production funds crossed environment %+v", s.State)
	}
	if s := defenseSnapshot(t, l, uniqueID("pu-other")); s.State.Balance != 0 {
		t.Fatalf("funds crossed account %+v", s.State)
	}
	other := NewForApp(l.store, domain.EnvSandbox, "other-app")
	if _, e := other.DefenseSnapshot(context.Background(), puid); e == nil {
		t.Fatal("foreign app acquired defense state")
	}
	if _, e := other.ActDefense(context.Background(), puid, defenseRequest("damage")); e == nil {
		t.Fatal("foreign app spent defense funds")
	}
	for range 12 {
		r := defenseRequest("revive")
		r.RunID = "same_run_0001"
		if got, e := l.ActDefense(context.Background(), puid, r); e != nil || got.Cost != 75 || got.RunID != r.RunID {
			t.Fatalf("repeat revive %+v err=%v", got, e)
		}
	}
	if s := defenseSnapshot(t, l, puid); s.State.Balance != 5300 {
		t.Fatalf("repeat revival balance %+v", s.State)
	}
}
func TestDefensePendingPurchaseDoesNotFundActions(t *testing.T) {
	l, done := newDefenseTestLedger(t)
	defer done()
	puid := uniqueID("pu-defense-pending")
	p := testPurchase(uniqueID("pending"), domain.StatePending, time.Now().UTC())
	p.ProductID = "crystals_100"
	_, _ = l.Grant(context.Background(), GrantInput{PlatformUserID: puid, EntitlementID: "defense_crystals_100", Purchase: p})
	if s := defenseSnapshot(t, l, puid); s.State.Balance != 0 {
		t.Fatalf("pending payment credited %+v", s.State)
	}
	if _, e := l.ActDefense(context.Background(), puid, defenseRequest("damage")); e == nil {
		t.Fatal("pending payment funded action")
	}
}
