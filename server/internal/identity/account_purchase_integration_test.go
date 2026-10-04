//go:build integration

package identity

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

func TestBloomhandGuestAccountPurchaseProtection(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local Firestore emulator required")
	}
	for _, tc := range []struct {
		name, app, history string
		existing, conflict bool
	}{
		{"empty restore", "bloomhand", "", true, false},
		{"production order", "bloomhand", "iap_apps/bloomhand/processed_orders/order", true, true},
		{"sandbox order", "bloomhand", "iap_environments/sandbox/iap_apps/bloomhand/processed_orders/order", true, true},
		{"production entitlement", "bloomhand", "iap_apps/bloomhand/iap_users/PUID/entitlements/friend_box_1", true, true},
		{"sandbox entitlement", "bloomhand", "iap_environments/sandbox/iap_apps/bloomhand/iap_users/PUID/entitlements/friend_box_1", true, true},
		{"opened production", "bloomhand", "iap_apps/bloomhand/box_states/PUID", true, true},
		{"opened sandbox", "bloomhand", "iap_environments/sandbox/iap_apps/bloomhand/box_states/PUID", true, true},
		{"new provider preserves purchase", "bloomhand", "iap_apps/bloomhand/box_states/PUID", false, false},
		{"other app unchanged", "other-app", "iap_apps/other-app/box_states/PUID", true, false},
		{"other app ledger ignored", "bloomhand", "iap_apps/other-app/processed_orders/order", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.New(ctx, "platform-account-qa", "t"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			repo := NewStoreRepository(st)
			guest, err := repo.EnsureUser(ctx, tc.app, NewIdentity{UID: "guest", Anonymous: true, AuthType: "firebase_bridge"})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			target := guest
			if tc.existing {
				target, err = repo.EnsureUser(ctx, tc.app, NewIdentity{UID: "existing", Anonymous: true, AuthType: "firebase_bridge"})
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.CreateAccountLinkChallenge(ctx, tc.app, target, "apple", "first-nonce", now.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.ConnectAccount(ctx, tc.app, target, "apple", "subject", "first-nonce", now); err != nil {
					t.Fatal(err)
				}
			}
			var history fspath.Path
			if tc.history != "" {
				history, err = fspath.Parse(strings.ReplaceAll(tc.history, "PUID", guest))
				if err != nil {
					t.Fatal(err)
				}
				if err := st.Set(ctx, history, map[string]any{"platformUserId": guest, "opened": 1}); err != nil {
					t.Fatal(err)
				}
			}
			if err := repo.CreateAccountLinkChallenge(ctx, tc.app, guest, "apple", "guest-nonce", now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				result, err := repo.ConnectAccount(ctx, tc.app, guest, "apple", "subject", "guest-nonce", now)
				if tc.conflict {
					if platformerr.CodeOf(err) != platformerr.CodeAccountLinkConflict {
						t.Fatalf("expected conflict, got %+v %v", result, err)
					}
					linked, err := repo.IsAccountLinked(ctx, tc.app, guest)
					if err != nil || linked {
						t.Fatalf("guest identity changed: %v %v", linked, err)
					}
				} else if err != nil || result.PlatformUserID != target || result.Restored != tc.existing {
					t.Fatalf("link result %+v %v", result, err)
				}
			}
			if tc.history != "" {
				if _, err := st.Get(ctx, history); err != nil {
					t.Fatal("history lost", err)
				}
			}
			// 소비된 challenge도 전환 이후 옛 신원에 생긴 구매를 버리지 않는다.
			if tc.app == "bloomhand" && tc.history == "" {
				history, _ := fspath.Parse("iap_apps/bloomhand/box_states/" + guest)
				if err := st.Set(ctx, history, map[string]any{"opened": 1}); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.ConnectAccount(ctx, tc.app, guest, "apple", "subject", "guest-nonce", now); platformerr.CodeOf(err) != platformerr.CodeAccountLinkConflict {
					t.Fatal("consumed challenge bypassed history", err)
				}
			}
		})
	}
}
