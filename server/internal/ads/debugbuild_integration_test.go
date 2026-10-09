//go:build integration

// Firestore emulator에서 디버그 빌드 보상이 운영 이벤트를 남기지 않는지 검증한다.
//
//	FIRESTORE_EMULATOR_HOST=127.0.0.1:8080 GOOGLE_CLOUD_PROJECT=platform-test \
//	  go test -tags=integration -run TestDebugBuild ./internal/ads/
package ads

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/operational"
	"github.com/seorilabs/platform/server/internal/store"
)

func TestDebugBuildRewardDeliverySkipsOperationalEvent(t *testing.T) {
	repo, done := newAdsIntegrationRepository(t)
	defer done()
	repo.WithOperationalEvents(operational.NewRepository(repo.store))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	tests := []struct {
		name      string
		debug     bool
		wantEvent bool
	}{
		{"운영 빌드 보상은 알림 이벤트를 남긴다", false, true},
		// QA 기기의 테스트 광고 보상이 #action-events와 운영 보고서의 광고 보상으로
		// 세어지면 안 된다. 보상 지급 자체는 그대로다.
		{"디버그 빌드 보상은 정산만 하고 이벤트를 남기지 않는다", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			puid := "pu_ads_" + uuid.NewString()
			requestID := "ads-" + uuid.NewString()
			input := integrationClaim(now, requestID, puid)
			input.DebugBuild = tt.debug
			claim, err := repo.CreateClaim(ctx, input, 20, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.ConfirmClaim(ctx, ConfirmInput{
				ClaimID: claim.ClaimID, AppID: claim.AppID, PlatformUserID: puid,
				Provider: claim.Provider, TransactionHash: hash("tx-" + requestID),
				Assurance: AssuranceClientConfirmed, Now: now, DailyLimit: 20,
			}); err != nil {
				t.Fatal(err)
			}
			delivered, err := repo.AcknowledgeClaim(ctx, claim.ClaimID, claim.AppID, puid, now)
			if err != nil || delivered.State != StateDelivered {
				t.Fatalf("정산 결과=%+v err=%v", delivered, err)
			}

			eventPath, err := fspath.Parse(
				"operational_event_outbox/" + operational.StableEventID("ad_reward", claim.AppID, claim.ClaimID),
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.store.Get(ctx, eventPath)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Fatal(err)
			}
			if gotEvent := err == nil; gotEvent != tt.wantEvent {
				t.Fatalf("광고 보상 이벤트 = %v, want %v", gotEvent, tt.wantEvent)
			}
		})
	}
}
