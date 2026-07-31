//go:build market

package providers_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/providers/apple"
)

// 실제 샌드박스 구매를 검증한다. shadow 대조다.
//
// lizard-tycoon이 Firebase Functions(TypeScript)로 운영하며 남긴
// 실제 거래다. 사람이 기기에서 결제한 것이고, 여기서는 그
// transactionId로 우리 Go provider가 같은 결론에 도달하는지 본다.
//
// 핵심은 orderKey 일치다. 기존 구현이 Firestore 문서 ID로 쓴 값과
// 우리가 계산한 값이 같아야 한다. 다르면 전환 시점에 같은 구매가
// 두 주문으로 갈라지고, 멱등이 깨져 이중 지급이 난다.
//
// 실결제를 다시 하지 않고도 "실제 마켓 응답을 우리가 제대로
// 해석하는가"를 검증할 수 있는 경로다.
//
// Play는 이렇게 할 수 없다. purchaseToken을 원장에 저장하지 않기
// 때문이다(PII 최소화). orderKey는 sha256이라 역산도 안 된다.
// Play 재검증은 기기에서 새 구매를 만들어야 한다.
func TestAppleRealSandboxPurchase(t *testing.T) {
	txID := os.Getenv("APPLE_REAL_TRANSACTION_ID")
	if txID == "" {
		t.Skip("APPLE_REAL_TRANSACTION_ID가 없어 건너뛴다")
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got, err := verifier.Verify(ctx, domain.Proof{
		Platform: domain.PlatformAppStore,
		// 상품을 지정하지 않는다. 응답이 알려주게 둔다 —
		// 웹훅 재검증 경로와 같은 형태다.
		Token: txID,
	})
	if err != nil {
		t.Fatalf("실제 구매 검증 실패: %v", err)
	}

	t.Logf("검증 결과:")
	t.Logf("  productId       = %s", got.ProductID)
	t.Logf("  canonicalId     = %s (originalTransactionId)", got.CanonicalID)
	t.Logf("  providerOrderId = %s (transactionId)", got.ProviderOrderID)
	t.Logf("  state           = %s", got.State)
	t.Logf("  completion      = %s", got.Completion)
	t.Logf("  purchasedAt     = %s", got.PurchasedAt.Format(time.RFC3339))
	t.Logf("  accountRef 있음 = %v", got.PlatformAccountID != "")

	// 불변식 1. canonicalId는 originalTransactionId여야 한다.
	if got.CanonicalID == "" {
		t.Error("canonicalId가 비었다 — 멱등키를 만들 수 없다")
	}
	if got.ProductID == "" {
		t.Error("productId가 비었다")
	}
	// 불변식 9. NON_CONSUMABLE만 통과한다. 통과했다는 것 자체가 검증이다.
	if got.State != domain.StateActive && got.State != domain.StateRevoked {
		t.Errorf("state = %s — active나 revoked를 기대했다", got.State)
	}
	if got.PurchasedAt.IsZero() {
		t.Error("purchasedAt이 비었다 — stale 억제가 성립하지 않는다")
	}
	// 기존 구현이 Firestore 문서 ID로 쓴 orderKey와 같아야 한다.
	//
	// 다르면 전환 시점에 같은 구매가 두 주문으로 갈라진다.
	// 멱등이 깨져 이미 지급한 것을 다시 지급한다.
	orderKey := got.Key()
	t.Logf("  orderKey        = %s", orderKey)

	if want := os.Getenv("APPLE_REAL_ORDER_KEY"); want != "" {
		if orderKey != want {
			t.Errorf("orderKey가 기존 구현과 다르다\n  got  = %s\n  want = %s\n"+
				"전환 시점에 같은 구매가 두 주문으로 갈라진다", orderKey, want)
		} else {
			t.Log("  → 기존 TypeScript 구현과 orderKey 일치 (shadow 대조 통과)")
		}
	}
}
