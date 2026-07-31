//go:build market

package providers_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/providers/apple"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// finishTransaction 호출 경로를 실제 API로 확인한다.
//
// 이미 완료된 거래에 다시 호출한다. Apple이 멱등하게 받아주면
// 완료 처리 경로 전체가 동작한다는 뜻이다.
//
// 이게 중요한 이유. 완료 호출이 실패하면 Play는 3일 뒤 자동 환불하고
// Apple도 거래를 미완료로 본다. 지급은 했는데 마켓은 모르는 상태가
// 되고, 그 상태를 outbox 워커가 계속 재시도한다. 경로가 살아 있는지
// 실결제 전에 알아야 한다.
func TestAppleFinishTransactionPath(t *testing.T) {
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

	// 검증기가 만드는 것과 같은 형태로 완료를 요청한다.
	err = verifier.CompleteGrant(ctx, domain.VerifiedPurchase{
		Platform:        domain.PlatformAppStore,
		ProductID:       "com.seorilabs.lizardtycoon.premium.galaxy_gecko",
		CanonicalID:     txID,
		ProviderOrderID: txID,
		Completion:      domain.CompletionAppleFinish,
	})

	if err == nil {
		t.Log("finishTransaction 성공 — 완료 처리 경로가 동작한다")
		return
	}

	code := platformerr.CodeOf(err)
	t.Logf("finishTransaction 응답: %s (%v)", code, err)

	switch code {
	case platformerr.CodeProviderAuthFailed:
		t.Fatal("자격증명이 거부됐다")
	case platformerr.CodePurchaseNotFound, platformerr.CodePurchaseInvalid:
		// 이미 완료된 거래라 Apple이 거절할 수 있다.
		// 인증은 통했다는 뜻이므로 경로는 살아 있다.
		t.Log("Apple이 거래를 거절했다 — 인증은 통했고 경로는 살아 있다")
	default:
		t.Fatalf("예상하지 못한 코드다: %s", code)
	}
}
