//go:build integration

package identity

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/store"
)

func TestReviewAccessTransactions(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local Firestore emulator required")
	}
	st, err := store.New(t.Context(), "demo-review-access", "t"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := NewStoreRepository(st)
	app := "my-little-parasite"
	var group sync.WaitGroup
	uids := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			uid, e := repo.ResolveReviewUID(t.Context(), app, reviewPrefix(app)+fmt.Sprintf("%064x", i+1))
			uids <- uid
			errs <- e
		}(i)
	}
	group.Wait()
	close(uids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	first := ""
	for uid := range uids {
		if first == "" {
			first = uid
		}
		if uid != first {
			t.Fatal("concurrent demo identities split")
		}
	}
	// Ordinary deletion markers are retained; entering the demo never clears them.
	if err = st.Set(t.Context(), deletionPath(app, first), map[string]any{"state": "processing"}); err != nil {
		t.Fatal(err)
	}
	replacement := reviewPrefix(app) + strings.Repeat("f", 64)
	got, err := repo.ResolveReviewUID(t.Context(), app, replacement)
	if err != nil || got != replacement {
		t.Fatalf("deleted demo not replaced: %v", err)
	}
	if _, err = st.Get(t.Context(), deletionPath(app, first)); err != nil {
		t.Fatal("deletion marker removed")
	}
	other, err := repo.ResolveReviewUID(t.Context(), "other-app", reviewPrefix("other-app")+strings.Repeat("b", 64))
	if err != nil || other == got {
		t.Fatal("apps share demo identity")
	}
	now := time.Unix(1800000000, 0)
	for i := 0; i < reviewAttemptsPerMinute; i++ {
		ok, e := repo.AllowReviewAttempt(t.Context(), app, now)
		if e != nil || !ok {
			t.Fatalf("request%d: %v", i, e)
		}
	}
	if ok, e := repo.AllowReviewAttempt(t.Context(), app, now); e != nil || ok {
		t.Fatalf("rate cap ignored: %v", e)
	}
	if ok, e := repo.AllowReviewAttempt(t.Context(), app, now.Add(time.Minute)); e != nil || !ok {
		t.Fatalf("code blocked beyond burst window: %v", e)
	}
	if ok, e := repo.AllowReviewAttempt(t.Context(), "other-app", now); e != nil || !ok {
		t.Fatal("rate caps shared between apps")
	}
}

// Simulate the completed deletion marker's TTL expiry without another demo visit.
// Retiring the alias in the deletion transaction must survive that cleanup.
func TestDeletedReviewAliasDoesNotSurviveMarkerExpiry(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local Firestore emulator required")
	}
	st, err := store.New(t.Context(), "demo-review-access", "t"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := NewStoreRepository(st)
	app := testApp()
	first := reviewPrefix(app.AppID) + strings.Repeat("a", 64)
	if _, err = repo.ResolveReviewUID(t.Context(), app.AppID, first); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.BeginDeletion(t.Context(), app, first, "review-expiry-receipt"); err != nil {
		t.Fatal(err)
	}
	if err = st.Delete(t.Context(), deletionPath(app.AppID, first)); err != nil {
		t.Fatal(err)
	}
	replacement := reviewPrefix(app.AppID) + strings.Repeat("b", 64)
	got, err := repo.ResolveReviewUID(t.Context(), app.AppID, replacement)
	if err != nil || got != replacement {
		t.Fatalf("expired deletion resurrected old review UID: got=%s err=%v", got, err)
	}
	// A late deletion request for the old UID must leave the replacement alias alone.
	if _, err = repo.BeginDeletion(t.Context(), app, first, "review-old-receipt"); err != nil {
		t.Fatal(err)
	}
	got, err = repo.ResolveReviewUID(t.Context(), app.AppID, reviewPrefix(app.AppID)+strings.Repeat("c", 64))
	if err != nil || got != replacement {
		t.Fatalf("old deletion retired replacement UID: got=%s err=%v", got, err)
	}
}
