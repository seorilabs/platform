//go:build integration

package ledger

import (
	"context"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"strings"
	"testing"
)

func TestSaltLegacyCannotMintFromClientAndKeepsOfflineSpending(t *testing.T) {
	l, now := saltTestLedger(t)
	ctx := context.Background()
	uid := uniqueOperatorPUID()
	if _, err := l.TransactSalt(ctx, uid, SaltRequest{RequestID: "mint", Action: "bootstrap", LegacySeals: 1000000}); err == nil {
		t.Fatal("client minted unverified legacy money")
	}
	state, _ := l.SaltSnapshot(ctx, uid)
	if state.Initialized || state.Available != 0 {
		t.Fatal("failed import changed balance")
	}
	baseline := SaltLegacyBalance{PlatformUserID: uid, SealCap: 60, SourceKind: "pre-iap-cloud-snapshot", SourceDigest: strings.Repeat("a", 64), CapturedAt: *now}
	if err := l.ImportSaltLegacy(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	if err := l.ImportSaltLegacy(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	inflated := baseline
	inflated.SealCap = 280
	if err := l.ImportSaltLegacy(ctx, inflated); err == nil {
		t.Fatal("overwrote frozen cap")
	}
	if _, err := l.TransactSalt(ctx, uid, SaltRequest{RequestID: "inflate", Action: "bootstrap", LegacySeals: 61}); err == nil {
		t.Fatal("client exceeded trusted cap")
	}
	// 동결60 이후 오프라인에서20을 썼다면 현재40만 보존한다.
	got, err := l.TransactSalt(ctx, uid, SaltRequest{RequestID: "migrate", Action: "bootstrap", LegacySeals: 40})
	if err != nil || got.Snapshot.Available != 40 {
		t.Fatalf("offline spending revived: %+v %v", got, err)
	}
	if _, err := l.TransactSalt(ctx, uid, SaltRequest{RequestID: "migrate-again", Action: "bootstrap", LegacySeals: 60}); err == nil {
		t.Fatal("old snapshot reimported money")
	}
	other := uniqueOperatorPUID()
	if _, err := l.TransactSalt(ctx, other, SaltRequest{RequestID: "other", Action: "bootstrap", LegacySeals: 40}); err == nil {
		t.Fatal("another PUID inherited cap")
	}
	foreign := NewForApp(l.store, domain.EnvSandbox, "other-app")
	if err := foreign.ImportSaltLegacy(ctx, baseline); err == nil {
		t.Fatal("foreign app imported Salt baseline")
	}
}
