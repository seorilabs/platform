//go:build integration

package identity

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/store"
)

func TestDeletionTransactionRecoveryAndCleanup(t *testing.T) {
	// 운영 DB에 이 테스트를 실행할 수 없도록 에뮬레이터를 필수로 한다.
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local Firestore emulator required")
	}
	ctx := context.Background()
	st, err := store.New(ctx, "platform-deletion-test", "t"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	repo := NewStoreRepository(st)
	repo.now = func() time.Time { return now }
	app := deletionTestApp()
	uid := "qa-user"
	puid, err := repo.EnsureUser(ctx, app.AppID, NewIdentity{UID: uid, AuthType: "firebase_bridge"})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveRefresh(ctx, "qa-refresh", Session{AppID: app.AppID, AppUserID: uid, PlatformUserID: puid}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	receipt := strings.Repeat("a", 64)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = repo.EnsureUser(ctx, app.AppID, NewIdentity{UID: uid, AuthType: "firebase_bridge"})
		}()
	}
	status, err := repo.BeginDeletion(ctx, app, uid, receipt)
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if status.State != "processing" {
		t.Fatal(status)
	}
	if _, err = repo.EnsureUser(ctx, app.AppID, NewIdentity{UID: uid}); err == nil {
		t.Fatal("deleted identity recreated")
	}
	if err = repo.SaveRefresh(ctx, "late-refresh", Session{AppID: app.AppID, AppUserID: uid, PlatformUserID: puid}, now.Add(time.Hour)); err == nil {
		t.Fatal("late refresh persisted")
	}
	if _, err = repo.BeginDeletion(ctx, app, uid, receipt); err != nil {
		t.Fatal("lost response replay", err)
	}
	if _, err = repo.DeletionStatus(ctx, "other-app", receipt); err == nil {
		t.Fatal("cross-app status disclosure")
	}
	jobs, err := repo.ClaimDeletions(ctx, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim jobs %v %v", len(jobs), err)
	}
	first := jobs[0]
	now = now.Add(3 * time.Minute)
	jobs, err = repo.ClaimDeletions(ctx, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatal("lease recovery", err)
	}
	if err = repo.AdvanceDeletion(ctx, first, true); err == nil {
		t.Fatal("stale worker advanced state")
	}
	for step := 0; step < 4; step++ {
		if step > 0 {
			jobs, err = repo.ClaimDeletions(ctx, 1)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("step %d jobs=%d err=%v", step, len(jobs), err)
			}
		}
		if jobs[0].Step != step {
			t.Fatalf("step=%d", jobs[0].Step)
		}
		if step == 3 {
			jobs[0].AnalyticsJobRef = "asia-northeast3/long-job"
			accepted := now.UTC()
			jobs[0].AnalyticsPhaseAcceptedAt = &accepted
			jobs[0].GoogleDeletionRequestedAt = &accepted
			jobs[0].GoogleAnalyticsDeletion = "accepted"
			if err = repo.AdvanceDeletion(ctx, jobs[0], false); err != nil {
				t.Fatal(err)
			}
			now = now.Add(6 * time.Minute)
			jobs, err = repo.ClaimDeletions(ctx, 1)
			if err != nil || len(jobs) != 1 || jobs[0].AnalyticsJobRef != "asia-northeast3/long-job" {
				t.Fatal("long query reference lost", err)
			}
			if jobs[0].AnalyticsPhaseAcceptedAt == nil || !jobs[0].AnalyticsPhaseAcceptedAt.Equal(accepted) {
				t.Fatal("Google acceptance was lost across copy cleanup retry")
			}
		}
		if step == 3 {
			if err = repo.DeleteIdentityData(ctx, app.AppID, uid, puid); err != nil {
				t.Fatal(err)
			}
		}
		if err = repo.AdvanceDeletion(ctx, jobs[0], true); err != nil {
			t.Fatal(err)
		}
		if step == 2 {
			pending, err := repo.ClaimDeletions(ctx, 1)
			if err != nil || len(pending) != 0 {
				t.Fatal("export recheck ran before delay")
			}
			now = now.Add(4 * 24 * time.Hour)
		}
	}
	snap, err := st.Get(ctx, deletionPath(app.AppID, uid))
	if err != nil {
		t.Fatal(err)
	}
	var final DeletionJob
	if err = snap.DataTo(&final); err != nil {
		t.Fatal(err)
	}
	if final.UID != "" || final.PlatformUserID != "" || final.State != "completed" {
		t.Fatal("completed receipt retains raw identity")
	}
	if _, err = repo.LoadRefresh(ctx, "qa-refresh"); err == nil {
		t.Fatal("refresh remains")
	}
	path, _ := identityPath(app.AppID, uid)
	if _, err = st.Get(ctx, path); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("identity remains", err)
	}
	if _, err = repo.DeletionStatus(ctx, app.AppID, receipt); err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * 24 * time.Hour)
	if _, err = repo.ClaimDeletions(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Get(ctx, deletionPath(app.AppID, uid)); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired deletion metadata remains", err)
	}
}
