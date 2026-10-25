//go:build integration

package ledger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/operational"
	"github.com/seorilabs/platform/server/internal/store"
)

func starterStore(t *testing.T) (*store.Client, *Ledger) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("isolated Firestore emulator required")
	}
	st, err := store.New(context.Background(), "demo-starter-game", fmt.Sprintf("starter_%d_", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	l := NewForApp(st, domain.EnvSandbox, "starter-game").WithOperationalEvents(operational.NewRepository(st))
	return st, l
}

func TestStarterTicketSourcesStayAtomicAndScoped(t *testing.T) {
	_, l := starterStore(t)
	ctx := context.Background()
	puid := "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	now := time.Now().UTC()
	first := testPurchase("starter-order-one", domain.StateActive, now)
	first.ProductID = "starter_test_ticket_1"
	var wg sync.WaitGroup
	errorsCh := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: first.ProductID, Purchase: first})
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(want int) {
		t.Helper()
		got, err := l.ContentUnitsRemaining(ctx, puid, first.ProductID, 1)
		if err != nil || got != want {
			t.Fatalf("ticket quantity=%d want=%d error=%v", got, want, err)
		}
	}
	check(1)
	second := first
	second.CanonicalID = "starter-order-two"
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: first.ProductID, Purchase: second}); err != nil {
		t.Fatal(err)
	}
	check(2)
	otherOwner, err := l.ContentUnitsRemaining(ctx, "pu_01ARZ3NDEKTSV4RRFFQ69G5FAW", first.ProductID, 1)
	if err != nil || otherOwner != 0 {
		t.Fatalf("another user received tickets: %d %v", otherOwner, err)
	}
	for _, scoped := range []*Ledger{NewForApp(l.store, domain.EnvProduction, "starter-game"), NewForApp(l.store, domain.EnvSandbox, "other-game")} {
		got, err := scoped.ContentUnitsRemaining(ctx, puid, first.ProductID, 1)
		if err != nil || got != 0 {
			t.Fatalf("another scope received tickets: %d %v", got, err)
		}
	}
	revoked := first
	revoked.State = domain.StateRevoked
	revoked.ObservedAt = now.Add(time.Minute)
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: first.ProductID, Purchase: revoked}); err != nil {
		t.Fatal(err)
	}
	check(1)
	if _, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: first.ProductID, Purchase: first}); err != nil {
		t.Fatal(err)
	}
	check(1)
}

func TestStarterMailNotificationsCommitOnceAndRollBackTogether(t *testing.T) {
	st, l := starterStore(t)
	ctx := context.Background()
	puid := "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	in := InboxIssue{RequestID: strings.Repeat("m", 128), PlatformUserID: puid, Title: "Private title", Body: "Private body", Rewards: []InboxReward{{Kind: "entitlement", EntitlementID: "starter_badge", Quantity: 1}}, Reason: AdminReasonInternalValidation, Actor: "test"}
	for range 2 {
		if _, err := l.IssueInbox(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := l.ClaimInbox(ctx, puid, in.RequestID); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ prefix, kind string }{{"inbox_issue", "inbox.issued"}, {"inbox_claim", "inbox.claimed"}} {
		id := operational.StableEventID(item.prefix, l.appID, string(l.env), puid, in.RequestID)
		path, _ := fspath.Parse("operational_event_outbox/" + id)
		snap, err := st.Get(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		var event operational.Event
		if err = snap.DataTo(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type != item.kind || event.Attributes["messageIdHash"] != economyDigest(in.RequestID) || len(event.Attributes) != 3 {
			t.Fatalf("notification contract: %+v", event)
		}
	}
	// A failed outbox write must roll back the mailbox write and issuance audit as well.
	in.RequestID = "outbox-conflict"
	id := operational.StableEventID("inbox_issue", l.appID, string(l.env), puid, in.RequestID)
	path, _ := fspath.Parse("operational_event_outbox/" + id)
	if err := st.Set(ctx, path, map[string]any{"reserved": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.IssueInbox(ctx, in); err == nil {
		t.Fatal("outbox conflict did not abort issue")
	}
	mailPath, _ := l.inboxPath(puid, in.RequestID)
	if _, err := st.Get(ctx, mailPath); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mail survived rolled-back notification: %v", err)
	}
}
