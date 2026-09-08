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

	first, err := repo.CreateClaim(ctx, integrationClaim(now, requestID, puid), 1, 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	replayInput := integrationClaim(now, requestID, puid)
	replay, err := repo.CreateClaim(ctx, replayInput, 1, 30, 0)
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
	_, err = repo.CreateClaim(ctx, integrationClaim(now.Add(time.Minute), "ads-"+uuid.NewString(), puid), 1, 30, 0)
	if platformerr.CodeOf(err) != platformerr.CodeAdDailyLimit {
		t.Fatalf("daily limit code=%q err=%v", platformerr.CodeOf(err), err)
	}

	other := integrationClaim(now, "ads-"+uuid.NewString(), "pu_ads_"+uuid.NewString())
	other, err = repo.CreateClaim(ctx, other, 20, 0, 0)
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

func TestRequestCooldownPreservesReplayAndUnpaidAllowance(t *testing.T) {
	repo, done := newAdsIntegrationRepository(t)
	defer done()
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	puid := "pu_cooldown_" + uuid.NewString()
	makeClaim := func(at time.Time) Claim {
		c := integrationClaim(at, "ads-"+uuid.NewString(), puid)
		c.AppID, c.PlacementID = "lord-ledger", "city_supply"
		c.Provider, c.ClientPlatform = "admob", "android"
		c.Reward = Reward{Key: "food", Amount: 1000}
		return c
	}
	first, err := repo.CreateClaim(ctx, makeClaim(now), 3, 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	replay := first
	replay.ClaimID = "cl_" + uuid.NewString()
	replay.CreatedAt = now.Add(5 * time.Second)
	actual, err := repo.CreateClaim(ctx, replay, 3, 0, 30)
	if err != nil || actual.ClaimID != first.ClaimID {
		t.Fatalf("idempotent retry=%+v err=%v", actual, err)
	}
	if _, err := repo.CreateClaim(ctx, makeClaim(now.Add(29*time.Second)), 3, 0, 30); platformerr.CodeOf(err) != platformerr.CodeAdCooldown {
		t.Fatalf("29 second retry err=%v", err)
	}
	claims := []Claim{first}
	// 미시청 요청 생성은 일일 지급 횟수를 소모하지 않는다.
	for i := 1; i <= 4; i++ {
		c, err := repo.CreateClaim(ctx, makeClaim(now.Add(time.Duration(i)*30*time.Second)), 3, 0, 30)
		if err != nil {
			t.Fatalf("unpaid request %d: %v", i, err)
		}
		claims = append(claims, c)
	}
	for i, c := range claims[:4] {
		_, err := repo.ConfirmClaim(ctx, ConfirmInput{ClaimID: c.ClaimID, AppID: c.AppID, PlatformUserID: puid,
			Provider: "admob", TransactionHash: hash("ssv-" + c.RequestID), Assurance: AssuranceServerVerified,
			Now: now.Add(3 * time.Minute), DailyLimit: 3})
		if i < 3 && err != nil {
			t.Fatalf("grant %d: %v", i, err)
		}
		if i == 3 && platformerr.CodeOf(err) != platformerr.CodeAdDailyLimit {
			t.Fatalf("fourth grant err=%v", err)
		}
	}
	if recovered, err := repo.CreateClaim(ctx, replay, 3, 0, 30); err != nil || recovered.ClaimID != first.ClaimID {
		t.Fatalf("settled replay=%+v err=%v", recovered, err)
	}
}

func TestRequestCooldownCrossesUTCMidnight(t *testing.T) {
	repo, done := newAdsIntegrationRepository(t)
	defer done()
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 23, 59, 50, 0, time.UTC)
	puid := "pu_midnight_" + uuid.NewString()
	for _, tc := range []struct {
		after time.Duration
		want  platformerr.Code
	}{
		{0, ""}, {20 * time.Second, platformerr.CodeAdCooldown}, {30 * time.Second, ""},
	} {
		_, err := repo.CreateClaim(ctx, integrationClaim(now.Add(tc.after), "ads-"+uuid.NewString(), puid), 3, 0, 30)
		if platformerr.CodeOf(err) != tc.want {
			t.Fatalf("after=%s code=%s err=%v", tc.after, platformerr.CodeOf(err), err)
		}
	}
}

func TestRequestCooldownAllowsOnlyOneConcurrentNewRequest(t *testing.T) {
	repo, done := newAdsIntegrationRepository(t)
	defer done()
	ctx := context.Background()
	now := time.Now().UTC()
	puid := "pu_concurrent_" + uuid.NewString()
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := repo.CreateClaim(ctx, integrationClaim(now, "ads-"+uuid.NewString(), puid), 3, 0, 30)
			results <- err
		}()
	}
	accepted := 0
	for i := 0; i < 8; i++ {
		err := <-results
		if err == nil {
			accepted++
		} else if platformerr.CodeOf(err) != platformerr.CodeAdCooldown {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted=%d, want 1", accepted)
	}
}

func TestOmittedRequestCooldownPreservesExistingApps(t *testing.T) {
	repo, done := newAdsIntegrationRepository(t)
	defer done()
	now := time.Now().UTC()
	puid := "pu_existing_" + uuid.NewString()
	for i := 0; i < 4; i++ {
		if _, err := repo.CreateClaim(context.Background(), integrationClaim(now, "ads-"+uuid.NewString(), puid), 3, 30, 0); err != nil {
			t.Fatal(err)
		}
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
