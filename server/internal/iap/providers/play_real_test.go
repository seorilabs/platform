//go:build market

package providers_test

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/providers/play"
)

// 실제 Play 구매를 검증한다.
//
// voidedpurchases.list가 환불된 구매의 purchaseToken을 준다.
// 원장에는 토큰을 저장하지 않지만(PII 최소화) Play API는 알고 있다.
//
// 환불된 구매라 acknowledge는 할 수 없다. 하지만 검증 경로 —
// 조회, 응답 파싱, 상태 매핑, canonicalId 선택 — 는 전부 실제로 돈다.
func TestPlayRealPurchase(t *testing.T) {
	token := os.Getenv("PLAY_REAL_PURCHASE_TOKEN")
	packageName := os.Getenv("IAP_PLAY_PACKAGE_NAME")
	if token == "" || packageName == "" {
		t.Skip("PLAY_REAL_PURCHASE_TOKEN 또는 IAP_PLAY_PACKAGE_NAME이 없어 건너뛴다")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	httpClient, err := google.DefaultClient(ctx, playScope)
	if err != nil {
		t.Fatalf("Play 자격증명을 얻지 못했다: %v", err)
	}

	verifier, err := play.New(packageName, httpClient)
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}

	// 상품을 지정하지 않는다. 응답이 알려주게 둔다 —
	// 환불 알림 재검증 경로와 같은 형태다.
	got, err := verifier.Verify(ctx, domain.Proof{
		Platform: domain.PlatformGooglePlay,
		Token:    token,
	})
	if err != nil {
		t.Fatalf("실제 구매 검증 실패: %v", err)
	}

	t.Logf("검증 결과:")
	t.Logf("  productId       = %s", got.ProductID)
	t.Logf("  canonicalId     = %s (purchaseToken)", truncate(got.CanonicalID))
	t.Logf("  providerOrderId = %s", got.ProviderOrderID)
	t.Logf("  state           = %s", got.State)
	t.Logf("  completion      = %s", got.Completion)
	t.Logf("  purchasedAt     = %s", got.PurchasedAt.Format(time.RFC3339))
	t.Logf("  accountRef 있음 = %v", got.PlatformAccountID != "")
	t.Logf("  orderKey        = %s", got.Key())

	// 불변식 1. Play의 canonicalId는 purchaseToken이다.
	if got.CanonicalID != token {
		t.Errorf("canonicalId가 purchaseToken이 아니다")
	}
	if got.ProductID == "" {
		t.Error("productId가 비었다")
	}
	// 넘겨준 토큰이 활성인지 환불된 것인지에 따라 기대가 갈린다.
	// 둘 다 유효한 검증 대상이라 상태별로 나눠 본다.
	switch got.State {
	case domain.StateActive:
		// 활성 구매다. acknowledge하지 않았다면 완료 처리를 요구해야 한다.
		// 3일 안에 하지 않으면 Play가 자동 환불한다.
		if got.Completion != domain.CompletionGoogleAcknowledge &&
			got.Completion != domain.CompletionNone {
			t.Errorf("completion = %s — 활성 구매에 맞지 않는다", got.Completion)
		}
	case domain.StateRevoked:
		// 환불된 구매에 완료 처리를 시키면 안 된다.
		if got.Completion != domain.CompletionNone {
			t.Errorf("completion = %s — 환불된 구매에 완료 처리를 요구한다", got.Completion)
		}
	default:
		t.Errorf("state = %s — active나 revoked를 기대했다", got.State)
	}
	if got.PurchasedAt.IsZero() {
		t.Error("purchasedAt이 비었다")
	}
}

func truncate(s string) string {
	if len(s) <= 24 {
		return s
	}
	return s[:24] + "..."
}
