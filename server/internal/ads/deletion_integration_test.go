//go:build integration

package ads

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/store"
)

func TestAccountDeletionBlocksLateAdWritesAndDeletesLegacyUsage(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local Firestore emulator required")
	}
	ctx := context.Background()
	st, err := store.New(ctx, "platform-deletion-test", "t"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	users := identity.NewStoreRepository(st)
	app := registry.App{AppID: "deletion-qa", FirebaseProjectID: "deletion-qa"}
	puid, err := users.EnsureUser(ctx, app.AppID, identity.NewIdentity{UID: "qa", AuthType: "firebase_bridge"})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewStoreRepository(st).WithAccounts(users)
	now := time.Now().UTC()
	c := integrationClaim(now, "qa-request", puid)
	c.AppID = app.AppID
	if _, err = repo.CreateClaim(ctx, c, 3, 30, 30); err != nil {
		t.Fatal(err)
	}
	if _, err = users.BeginDeletion(ctx, app, "qa", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.CreateClaim(ctx, c, 3, 0, 0); err == nil {
		t.Fatal("replayed claim passed after deletion")
	}
	if _, err = repo.ConfirmClaim(ctx, ConfirmInput{ClaimID: c.ClaimID, AppID: app.AppID, PlatformUserID: puid, Provider: c.Provider, TransactionHash: strings.Repeat("c", 64), Assurance: AssuranceClientConfirmed, Now: now, DailyLimit: 3}); err == nil {
		t.Fatal("late callback confirmed")
	}
	if _, err = repo.AcknowledgeClaim(ctx, c.ClaimID, app.AppID, puid, now); err == nil {
		t.Fatal("late ACK succeeded")
	}
	if err = repo.DeleteAccountData(ctx, app.AppID, puid); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.GetClaim(ctx, c.ClaimID); err == nil {
		t.Fatal("claim remains")
	}
	p, _ := usagePath(ConfirmInput{AppID: app.AppID, PlatformUserID: puid}, c.PlacementID, now.Format("2006-01-02"))
	if _, err = st.Get(ctx, p); err == nil {
		t.Fatal("legacy usage remains")
	}
	// 재시도해도 다른 사용자의 기록은 보존한다.
	other := integrationClaim(now, "other", "other-user")
	other.AppID = app.AppID
	if _, err = repo.CreateClaim(ctx, other, 3, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteAccountData(ctx, app.AppID, puid); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.GetClaim(ctx, other.ClaimID); err != nil {
		t.Fatal("other user's claim removed", err)
	}
}
