//go:build integration

package identity

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/store"
)

func TestAccountRecoveryTransaction(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local Firestore emulator required")
	}
	for _, name := range []string{"recover and replay", "same target", "wrong target", "wrong subject", "other app", "other linked source", "normal purpose", "normal rejects recovery", "expired", "replay expired", "replay changed subject", "unlink before", "unlink after", "delete source", "delete target", "block source", "block target", "deleted user marker", "inconsistent target", "inconsistent mapping", "purchase protected"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.New(ctx, "demo-tc-sdk14", "t"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			now := time.Now().UTC()
			repo := NewStoreRepository(st).WithClock(func() time.Time { return now })
			appID := "lizard-tycoon"
			if name == "purchase protected" {
				appID = "bloomhand"
			}
			source, err := repo.EnsureUser(ctx, appID, NewIdentity{UID: "source", Anonymous: true, AuthType: "firebase_bridge"})
			if err != nil {
				t.Fatal(err)
			}
			target, err := repo.EnsureUser(ctx, appID, NewIdentity{UID: "target", Anonymous: true, AuthType: "firebase_bridge"})
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.CreateAccountLinkChallenge(ctx, appID, target, "kakao", "normal-target", now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.ConnectAccount(ctx, appID, target, "kakao", "subject", "normal-target", now); err != nil {
				t.Fatal(err)
			}
			if name == "same target" {
				source = target
			}
			nonce := "recovery"
			expected := "target"
			subject := "subject"
			callApp := appID
			if err := repo.CreateAccountLinkRecoveryChallenge(ctx, appID, source, "kakao", nonce, expected, now.Add(5*time.Minute)); err != nil {
				t.Fatal(err)
			}
			sourcePath, _ := userPath(source)
			targetPath, _ := userPath(target)
			mappingPath, _ := providerIdentityPath(appID, "kakao", hashHex("subject"))
			read := func(path fspath.Path) map[string]any {
				snap, e := st.Get(ctx, path)
				if e != nil {
					t.Fatal(e)
				}
				var data map[string]any
				if e = snap.DataTo(&data); e != nil {
					t.Fatal(e)
				}
				return data
			}
			set := func(path fspath.Path, data any) {
				if err := st.Set(ctx, path, data); err != nil {
					t.Fatal(err)
				}
			}
			pass := name == "recover and replay" || name == "same target"
			switch name {
			case "wrong target":
				expected = "other"
			case "wrong subject":
				subject = "other"
			case "other app":
				callApp = "other-app"
			case "other linked source":
				d := read(sourcePath)
				d["linkedProviders"] = map[string]string{"apple": "different"}
				set(sourcePath, d)
			case "normal purpose":
				nonce = "normal-target"
				source = target
			case "expired":
				now = now.Add(5 * time.Minute)
			case "unlink before":
				if err := repo.DisconnectAccount(ctx, appID, "kakao", "subject"); err != nil {
					t.Fatal(err)
				}
			case "delete source":
				set(deletionPath(appID, "source"), DeletionJob{})
			case "delete target":
				set(deletionPath(appID, "target"), DeletionJob{})
			case "block source", "block target":
				uid := "source"
				if name == "block target" {
					uid = "target"
				}
				p, _ := fspath.Parse("blocks/" + appID + "/uids/" + uid)
				set(p, map[string]any{"blocked_at": now})
			case "deleted user marker":
				set(deletionUserPath(appID, target), deletionReceiptDoc{JobID: "deletion"})
			case "inconsistent target":
				d := read(targetPath)
				d["appId"] = "other-app"
				set(targetPath, d)
			case "inconsistent mapping":
				d := read(mappingPath)
				d["subjectHash"] = "bad"
				set(mappingPath, d)
			case "purchase protected":
				p, _ := fspath.Parse("iap_apps/bloomhand/box_states/" + source)
				set(p, map[string]any{"opened": 1})
			}
			if name == "normal rejects recovery" {
				if _, err := repo.ConnectAccount(ctx, appID, source, "kakao", subject, nonce, now); err == nil {
					t.Fatal("AR-7: purpose bypass")
				}
				return
			}
			if name == "replay expired" || name == "replay changed subject" || name == "unlink after" {
				if _, err := repo.RecoverAccount(ctx, appID, source, "kakao", subject, nonce, expected, now); err != nil {
					t.Fatal(err)
				}
				if name == "replay expired" {
					now = now.Add(5 * time.Minute)
				}
				if name == "replay changed subject" {
					subject = "other"
				}
				if name == "unlink after" {
					if err := repo.DisconnectAccount(ctx, appID, "kakao", "subject"); err != nil {
						t.Fatal(err)
					}
				}
			}
			beforeSource := read(sourcePath)
			beforeTarget := read(targetPath)
			result, err := repo.RecoverAccount(ctx, callApp, source, "kakao", subject, nonce, expected, now)
			if pass {
				if err != nil || result.PlatformUserID != target || result.AppUserID != "target" || result.Restored != (source != target) {
					t.Fatalf("AR-1/2: canonical recovery failed: %v", err)
				}
				if _, err = repo.RecoverAccount(ctx, callApp, source, "kakao", subject, nonce, expected, now); err != nil {
					t.Fatal("AR-7: same-proof replay failed", err)
				}
			} else if err == nil {
				t.Fatal("AR-3/7: unsafe recovery accepted")
			}
			if !reflect.DeepEqual(beforeSource, read(sourcePath)) || !reflect.DeepEqual(beforeTarget, read(targetPath)) {
				t.Fatal("AR-3: recovery modified user mappings")
			}
		})
	}
}
