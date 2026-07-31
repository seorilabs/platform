//go:build market

// 완료 재시도 워커를 실제 Firestore와 실제 마켓으로 검증한다.
//
// 워커는 "지급은 했는데 마켓에 알리지 못한" 주문을 다시 처리한다.
// Play는 3일 안에 acknowledge하지 않으면 자동 환불하므로 이 경로가
// 멈추면 매출이 사라진다. fake로만 검증하고 넘어갈 수 없다.
//
//	go test -tags=market ./internal/iap/worker/ -v
//
// 필요한 환경변수는 market_integration_test.go와 같고
// APPLE_REAL_TRANSACTION_ID가 추가로 필요하다.
package worker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/providers/apple"
	"github.com/seorilabs/platform/server/internal/store"
)

func TestWorkerCompletesAgainstRealMarket(t *testing.T) {
	txID := os.Getenv("APPLE_REAL_TRANSACTION_ID")
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if txID == "" || project == "" {
		t.Skip("APPLE_REAL_TRANSACTION_ID 또는 GOOGLE_CLOUD_PROJECT가 없어 건너뛴다")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// staging prefix + sandbox 환경으로 이중 격리한다.
	st, err := store.New(ctx, project, "stg_")
	if err != nil {
		t.Fatalf("store 생성 실패: %v", err)
	}
	defer st.Close()

	led := ledger.New(st, domain.EnvSandbox)

	keyContent, err := os.ReadFile(os.Getenv("APPLE_IAP_PRIVATE_KEY_PATH"))
	if err != nil {
		t.Fatalf(".p8을 읽지 못했다: %v", err)
	}
	bundleID := os.Getenv("APPLE_IAP_BUNDLE_ID")

	client, err := apple.NewClient(apple.Config{
		KeyContent:  keyContent,
		KeyID:       os.Getenv("APPLE_IAP_KEY_ID"),
		Issuer:      os.Getenv("APPLE_IAP_ISSUER_ID"),
		BundleID:    bundleID,
		Sandbox:     true,
		RequireOCSP: false,
	})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}

	verifier, err := apple.New(client, bundleID, true)
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}

	// 완료하지 못한 주문이 있는 상황을 만든다.
	purchase := domain.VerifiedPurchase{
		Platform:        domain.PlatformAppStore,
		ProductID:       "com.seorilabs.lizardtycoon.premium.galaxy_gecko",
		CanonicalID:     txID,
		ProviderOrderID: txID,
		State:           domain.StateActive,
		Completion:      domain.CompletionAppleFinish,
		PurchasedAt:     time.Now().Add(-time.Hour),
		ObservedAt:      time.Now(),
	}
	orderKey := purchase.Key()

	if err := led.Enqueue(ctx, orderKey, purchase); err != nil {
		t.Fatalf("대기열 적재 실패: %v", err)
	}
	t.Logf("대기열에 적재: %s", orderKey)

	w, err := New(Config{
		Outbox:      led,
		Completers:  map[domain.Platform]Completer{domain.PlatformAppStore: verifier},
		MaxAttempts: 12,
		MaxAge:      48 * time.Hour,
	})
	if err != nil {
		t.Fatalf("워커 생성 실패: %v", err)
	}

	stats, err := w.RunOnce(ctx)
	if err != nil {
		t.Fatalf("워커 실행 실패: %v", err)
	}

	t.Logf("워커 결과: claimed=%d completed=%d failed=%d",
		stats.Claimed, stats.Completed, stats.Failed)

	if stats.Claimed == 0 {
		t.Fatal("대기열에서 아무것도 집지 않았다 — 인덱스나 쿼리를 확인해라")
	}
	if stats.Completed == 0 {
		t.Fatal("완료 처리에 실패했다 — 마켓 호출 경로를 확인해라")
	}

	// 완료했으면 대기열에서 사라져야 한다.
	// 원장에서 문서를 지우는 유일한 경우다.
	remaining, found, err := led.ClaimNext(ctx, domain.PlatformAppStore)
	if err != nil {
		t.Fatalf("대기열 재조회 실패: %v", err)
	}
	if found && remaining.OrderKey == orderKey {
		t.Error("완료했는데 대기열에 남아 있다")
	}
	t.Log("대기열에서 제거됨 — 완료 재시도 경로가 전 구간 동작한다")
}
