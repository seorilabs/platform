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
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// acknowledge 호출 경로를 실제 API로 확인한다.
//
// 환불된 구매에 호출하므로 Play는 거절한다. 그래도 인증이 통하고
// 우리 요청이 Play에 닿는다는 것은 확인된다 — 자격증명 오류와
// 구분되는 응답이 오면 경로는 살아 있다.
//
// 완전한 검증은 완료하지 않은 활성 구매가 필요하다. 그 토큰을 얻는
// API 경로가 없어서(voidedpurchases.list는 환불된 것만 준다)
// 기기 결제로만 만들 수 있다.
func TestPlayAcknowledgePath(t *testing.T) {
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

	err = verifier.CompleteGrant(ctx, domain.VerifiedPurchase{
		Platform:    domain.PlatformGooglePlay,
		ProductID:   "sp_galaxy_gecko",
		CanonicalID: token,
		Completion:  domain.CompletionGoogleAcknowledge,
	})

	if err == nil {
		t.Log("acknowledge 성공 — 완료 처리 경로가 동작한다")
		return
	}

	code := platformerr.CodeOf(err)
	t.Logf("acknowledge 응답: %s (%v)", code, err)

	switch code {
	case platformerr.CodeProviderAuthFailed:
		t.Fatal("자격증명이 거부됐다. Play Console 권한을 확인해라")
	case platformerr.CodePurchaseNotFound, platformerr.CodePurchaseInvalid:
		// 환불된 구매라 Play가 거절했다. 인증은 통했고 요청이 닿았다.
		t.Log("Play가 거래를 거절했다 — 인증은 통했고 경로는 살아 있다")
	default:
		t.Fatalf("예상하지 못한 코드다: %s — 에러 매핑을 확인해라", code)
	}
}
