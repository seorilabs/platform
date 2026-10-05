//go:build integration

package ledger

import (
	"context"
	"fmt"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/store"
	"os"
	"sync"
	"testing"
	"time"
)

func TestInboxTransactions(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local Firestore emulator required")
	}
	ctx := context.Background()
	st, e := store.New(ctx, "demo-outgame", "qa_")
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	now := time.Unix(1800000000, 0)
	app := fmt.Sprintf("mail-%d", time.Now().UnixNano())
	l := NewForApp(st, domain.EnvSandbox, app).WithClock(func() time.Time { return now })
	puid := "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	other := "pu_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	in := InboxIssue{RequestID: "welcome", PlatformUserID: puid, Title: "Welcome", Body: "First reward", Rewards: []InboxReward{{Kind: "entitlement", EntitlementID: "starter", Quantity: 1}}, Reason: AdminReasonInternalValidation, Actor: "test"}
	first, e := l.IssueInbox(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := l.IssueInbox(ctx, in)
	if e != nil || replay.ID != first.ID {
		t.Fatalf("issue replay: %+v %v", replay, e)
	}
	mismatch := in
	mismatch.PlatformUserID = other
	if _, e = l.IssueInbox(ctx, mismatch); e == nil {
		t.Fatal("issue replay redirected to another user")
	}
	read, e := l.ReadInbox(ctx, puid, in.RequestID)
	if e != nil || read.ReadAt == 0 || read.ClaimedAt != 0 {
		t.Fatalf("read conflated with claim: %+v %v", read, e)
	}
	if _, e = l.ClaimInbox(ctx, other, in.RequestID); e == nil {
		t.Fatal("other user claim accepted")
	}
	if _, e = NewForApp(st, domain.EnvSandbox, app+"-other").ClaimInbox(ctx, puid, in.RequestID); e == nil {
		t.Fatal("other game claim accepted")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := l.ClaimInbox(ctx, puid, in.RequestID); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	ep, _ := l.paths.internalEntitlement(puid, "starter")
	snap, e := st.Get(ctx, ep)
	if e != nil {
		t.Fatal(e)
	}
	var ent entitlementDoc
	if e = snap.DataTo(&ent); e != nil {
		t.Fatal(e)
	}
	if len(ent.Sources) != 1 || !ent.Active {
		t.Fatalf("duplicate grant: %+v", ent)
	}
	// Response loss: repeat the same claim after moving past the original expiry.
	in.RequestID = "timed"
	in.ExpiresAt = now.Unix() + 1
	if _, e = l.IssueInbox(ctx, in); e != nil {
		t.Fatal(e)
	}
	if _, e = l.ClaimInbox(ctx, puid, "timed"); e != nil {
		t.Fatal(e)
	}
	now = now.Add(time.Second)
	if _, e = l.ClaimInbox(ctx, puid, "timed"); e != nil {
		t.Fatal("committed claim lost on expiration", e)
	}
	in.RequestID = "expired"
	in.ExpiresAt = now.Unix() + 1
	if _, e = l.IssueInbox(ctx, in); e != nil {
		t.Fatal(e)
	}
	now = now.Add(time.Second)
	if _, e = l.ClaimInbox(ctx, puid, "expired"); e == nil {
		t.Fatal("expiry boundary accepted")
	}
	// No automatic deletion: paginate past 50 messages, including old unclaimed items.
	in.ExpiresAt = 0
	in.Rewards = []InboxReward{}
	for i := 0; i < 51; i++ {
		in.RequestID = fmt.Sprintf("page-%02d", i)
		if _, e = l.IssueInbox(ctx, in); e != nil {
			t.Fatal(e)
		}
	}
	page, e := l.ListInbox(ctx, puid, "")
	if e != nil || len(page.Messages) != 50 || page.NextCursor == "" {
		t.Fatalf("page: %+v %v", page, e)
	}
	next, e := l.ListInbox(ctx, puid, page.NextCursor)
	if e != nil || len(next.Messages) != 4 || next.NextCursor != "" {
		t.Fatalf("next page: %+v %v", next, e)
	}
	empty, e := l.ListInbox(ctx, other, "")
	if e != nil || len(empty.Messages) != 0 {
		t.Fatal("empty inbox", e)
	}
}
