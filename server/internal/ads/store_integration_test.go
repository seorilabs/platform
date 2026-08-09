//go:build integration

// Firestore emulator 또는 staging Firestore에서 트랜잭션 불변식을 검증한다.
//
//	FIRESTORE_EMULATOR_HOST=127.0.0.1:8080 GOOGLE_CLOUD_PROJECT=platform-test \
//	  go test -tags=integration ./internal/ads/
package ads

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

func newAdsIntegrationRepository(t *testing.T) (*StoreRepository, func()) {
	t.Helper()
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		t.Skip("GOOGLE_CLOUD_PROJECT가 없어 건너뛴다")
	}
	st, err := store.New(context.Background(), project, "stg_")
	if err != nil {
		t.Fatalf("store 생성 실패: %v", err)
	}
	return NewStoreRepository(st), func() { _ = st.Close() }
}

func integrationClaim(now time.Time, requestID, puid string) Claim {
	return Claim{
		ClaimID: "cl_" + uuid.NewString(), RequestID: requestID,
		AppID: "happy-farm", PlatformUserID: puid, SupportCode: "HF-TEST",
		PlacementID: "harvest_boost", Provider: "apps_in_toss", ClientPlatform: "apps_in_toss",
		Reward: Reward{Key: "harvest_boost", Amount: 1}, State: StateAccepted, Assurance: AssurancePending,
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), TTLAt: now.Add(90 * 24 * time.Hour),
	}
}

func TestAdsClaimReplayLimitAndTransactionAreAtomic(t *testing.T) {
	repo, done := newAdsIntegrationRepository(t)
	defer done()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	puid := "pu_ads_" + uuid.NewString()
	requestID := "ads-" + uuid.NewString()

	first, err := repo.CreateClaim(ctx, integrationClaim(now, requestID, puid), 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	replayInput := integrationClaim(now, requestID, puid)
	replay, err := repo.CreateClaim(ctx, replayInput, 1, 30)
	if err != nil || replay.ClaimID != first.ClaimID {
		t.Fatalf("claim replay=%+v err=%v", replay, err)
	}

	confirmed, err := repo.ConfirmClaim(ctx, ConfirmInput{
		ClaimID: first.ClaimID, AppID: first.AppID, PlatformUserID: puid,
		Provider: first.Provider, TransactionHash: hash("tx-" + requestID),
		Assurance: AssuranceClientConfirmed, Now: now, DailyLimit: 1, CooldownSeconds: 30,
	})
	if err != nil || confirmed.State != StateConfirmed {
		t.Fatalf("confirm=%+v err=%v", confirmed, err)
	}
	_, err = repo.CreateClaim(ctx, integrationClaim(now.Add(time.Minute), "ads-"+uuid.NewString(), puid), 1, 30)
	if platformerr.CodeOf(err) != platformerr.CodeAdDailyLimit {
		t.Fatalf("daily limit code=%q err=%v", platformerr.CodeOf(err), err)
	}

	other := integrationClaim(now, "ads-"+uuid.NewString(), "pu_ads_"+uuid.NewString())
	other, err = repo.CreateClaim(ctx, other, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ConfirmClaim(ctx, ConfirmInput{
		ClaimID: other.ClaimID, AppID: other.AppID, PlatformUserID: other.PlatformUserID,
		Provider: other.Provider, TransactionHash: hash("tx-" + requestID),
		Assurance: AssuranceClientConfirmed, Now: now, DailyLimit: 20,
	})
	if platformerr.CodeOf(err) != platformerr.CodeClaimTransactionReplayed {
		t.Fatalf("transaction replay code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestAdsSuppressionProjectionIsIdempotentAndAppendOnly(t *testing.T) {
	repo, done := newAdsIntegrationRepository(t)
	defer done()
	ctx := context.Background()
	now := time.Now().UTC()
	puid := "pu_ads_" + uuid.NewString()
	grantID := "ads-" + uuid.NewString()
	record := SuppressionRecord{RequestID: grantID, AppID: "happy-farm", PlatformUserID: puid, ActorLogin: "integration-test", Reason: "internal_validation", CreatedAt: now, Operation: "grant"}

	first, err := repo.GrantSuppression(ctx, record)
	if err != nil || !first.Applied || first.ActiveGrantRequestID != grantID {
		t.Fatalf("grant=%+v err=%v", first, err)
	}
	replay, err := repo.GrantSuppression(ctx, record)
	if err != nil || !replay.Applied || replay.ActiveGrantRequestID != grantID {
		t.Fatalf("grant replay=%+v err=%v", replay, err)
	}
	second := record
	second.RequestID = "ads-" + uuid.NewString()
	secondResult, err := repo.GrantSuppression(ctx, second)
	if err != nil || secondResult.Applied || secondResult.ActiveGrantRequestID != grantID {
		t.Fatalf("second grant=%+v err=%v", secondResult, err)
	}
	revoke := SuppressionRecord{RequestID: "ads-" + uuid.NewString(), GrantRequestID: grantID, AppID: record.AppID, PlatformUserID: puid, ActorLogin: record.ActorLogin, Reason: "internal_validation", CreatedAt: now.Add(time.Second), Operation: "revoke"}
	if _, err := repo.RevokeSuppression(ctx, revoke); err != nil {
		t.Fatal(err)
	}
	active, err := repo.OperatorSuppressed(ctx, record.AppID, puid)
	if err != nil || active {
		t.Fatalf("active=%v err=%v", active, err)
	}
	history, err := repo.SuppressionHistory(ctx, record.AppID, puid, 10)
	if err != nil || len(history) != 3 {
		t.Fatalf("history=%d err=%v (%s)", len(history), err, fmt.Sprint(history))
	}
}
