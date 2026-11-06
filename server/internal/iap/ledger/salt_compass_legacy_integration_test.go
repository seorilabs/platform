//go:build integration

package ledger

import (
	"context"
	"testing"
)

func TestSaltLegacyPositiveAndKnownPositiveZeroAreHeld(t *testing.T) {
	l, _ := saltTestLedger(t)
	ctx, uid := context.Background(), uniqueOperatorPUID()
	for _, amount := range []int64{40, 1000000} {
		if _, err := l.TransactSalt(ctx, uid, SaltRequest{RequestID: "positive", Action: "bootstrap", LegacySeals: amount}); err == nil {
			t.Fatal("positive legacy was automatically minted")
		}
	}
	state, _ := l.SaltSnapshot(ctx, uid)
	if state.Initialized || state.Available != 0 {
		t.Fatal("held import changed wallet")
	}
	p, _ := l.saltPath("salt_compass_legacy", uid)
	if err := l.store.Set(ctx, p, saltLegacyHold{PlatformUserID: uid, SealCap: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.TransactSalt(ctx, uid, SaltRequest{RequestID: "missing-local-zero", Action: "bootstrap"}); err == nil {
		t.Fatal("zero overwrote known positive hold")
	}
	state, _ = l.SaltSnapshot(ctx, uid)
	if state.Initialized || state.Available != 0 {
		t.Fatal("known positive hold was lost")
	}
	other := uniqueOperatorPUID()
	if receipt, err := l.TransactSalt(ctx, other, SaltRequest{RequestID: "new-zero", Action: "bootstrap"}); err != nil || !receipt.Snapshot.Initialized || receipt.Snapshot.Available != 0 {
		t.Fatalf("fresh zero bootstrap failed: %v", err)
	}
}
