//go:build market

// 실제 마켓 API에 붙는 통합 테스트다.
//
// fake HTTP 테스트는 "우리가 아는 형식"만 검증한다. 실제 마켓이 그
// 형식으로 답하는지, 자격증명이 통하는지는 붙어봐야 안다.
// 이벤트 SDK에서 정확히 그 간극 때문에 이벤트가 전량 유실됐다.
//
// 실결제 없이도 확인할 수 있는 것을 확인한다.
//   - 자격증명으로 인증이 되는가
//   - 존재하지 않는 거래를 조회했을 때 우리가 매핑한 에러가 오는가
//   - 응답이 우리가 아는 스키마인가
//
// 실제 구매 검증은 사람이 기기에서 샌드박스 결제를 해야 한다.
// 그건 이 테스트의 범위 밖이다.
//
//	go test -tags=market ./internal/iap/providers/ -v
//
// 필요한 환경변수:
//
//	APPLE_IAP_KEY_ID, APPLE_IAP_ISSUER_ID, APPLE_IAP_PRIVATE_KEY_PATH
//	APPLE_IAP_BUNDLE_ID
//	GOOGLE_APPLICATION_CREDENTIALS (Play publisher SA)
//	IAP_PLAY_PACKAGE_NAME
package providers_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/providers/apple"
	"github.com/seorilabs/platform/server/internal/iap/providers/play"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

const playScope = "https://www.googleapis.com/auth/androidpublisher"

// 실제 마켓 호출이라 넉넉히 준다.
const marketTimeout = 30 * time.Second

func envOrSkip(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s가 없어 건너뛴다", key)
	}
	return v
}

// App Store Server API에 실제로 붙는다.
//
// 존재하지 않는 transactionId를 조회한다. 인증이 통하면 Apple이
// 4040010(TransactionIdNotFoundError)을 주고, 우리는 그것을
// purchase_not_found로 매핑한다.
//
// 자격증명이 틀리면 401 계열이 오고 provider_auth_failed가 된다.
// 둘을 구분할 수 있어야 운영 중에 원인을 안다.
func TestAppleRealAPI(t *testing.T) {
	keyPath := envOrSkip(t, "APPLE_IAP_PRIVATE_KEY_PATH")
	keyID := envOrSkip(t, "APPLE_IAP_KEY_ID")
	issuer := envOrSkip(t, "APPLE_IAP_ISSUER_ID")
	bundleID := envOrSkip(t, "APPLE_IAP_BUNDLE_ID")

	keyContent, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf(".p8 키를 읽지 못했다: %v", err)
	}

	// 샌드박스로 붙는다. production 원장을 건드리지 않는다.
	client, err := apple.NewClient(apple.Config{
		KeyContent: keyContent,
		KeyID:      keyID,
		Issuer:     issuer,
		BundleID:   bundleID,
		Sandbox:    true,
		// 샌드박스는 폐기 확인을 강제하지 않는다.
		// production 조합은 NewClient가 부팅 시점에 막는다.
		RequireOCSP: false,
	})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}

	verifier, err := apple.New(client, bundleID, true)
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), marketTimeout)
	defer cancel()

	// 존재할 수 없는 거래 식별자
	_, err = verifier.Verify(ctx, domain.Proof{
		Platform:  domain.PlatformAppStore,
		ProductID: "com.seorilabs.lizardtycoon.premium.galaxy_gecko",
		Token:     "000000000000000",
	})

	if err == nil {
		t.Fatal("없는 거래인데 성공했다")
	}

	code := platformerr.CodeOf(err)
	t.Logf("Apple 응답 코드: %s (%v)", code, err)

	switch code {
	case platformerr.CodePurchaseNotFound, platformerr.CodePurchaseInvalid:
		// 인증이 통했다는 뜻이다. Apple이 우리 요청을 이해했고
		// "그런 거래는 없다"고 답했다.
		t.Log("인증 성공 — Apple이 요청을 처리했다")

	case platformerr.CodeProviderAuthFailed:
		t.Fatal("자격증명이 거부됐다. keyId·issuer·.p8을 확인해라")

	default:
		t.Fatalf("예상하지 못한 코드다: %s — 에러 매핑을 확인해라", code)
	}
}

// Play Developer API에 실제로 붙는다.
//
// 존재하지 않는 purchaseToken을 조회한다. 권한이 있으면 Play가
// 404를 주고 purchase_not_found가 된다.
//
// Play Console에서 SA에 권한을 주지 않았으면 401/403이 오고
// provider_auth_failed가 된다. 이 구분이 중요하다 — SA는 만들었는데
// Console 연결을 잊는 것이 흔한 실수다.
func TestPlayRealAPI(t *testing.T) {
	envOrSkip(t, "GOOGLE_APPLICATION_CREDENTIALS")
	packageName := envOrSkip(t, "IAP_PLAY_PACKAGE_NAME")

	ctx, cancel := context.WithTimeout(context.Background(), marketTimeout)
	defer cancel()

	httpClient, err := google.DefaultClient(ctx, playScope)
	if err != nil {
		t.Fatalf("Play 자격증명을 얻지 못했다: %v", err)
	}

	verifier, err := play.New(packageName, httpClient)
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}

	_, err = verifier.Verify(ctx, domain.Proof{
		Platform:  domain.PlatformGooglePlay,
		ProductID: "sp_galaxy_gecko",
		// Play purchaseToken은 긴 영숫자 문자열이다.
		// 형식이 아예 다르면 404가 아니라 400이 온다.
		Token: "abcdefghijklmnop.AO-J1Ox" + time.Now().Format("20060102150405") +
			"NotARealTokenButLooksLikeOne0123456789abcdefghijklmnop",
	})

	if err == nil {
		t.Fatal("없는 구매인데 성공했다")
	}

	code := platformerr.CodeOf(err)
	t.Logf("Play 응답 코드: %s (%v)", code, err)

	switch code {
	case platformerr.CodePurchaseNotFound, platformerr.CodePurchaseInvalid:
		t.Log("인증 성공 — Play가 요청을 처리했다")

	case platformerr.CodeProviderAuthFailed:
		t.Fatal("Play가 요청을 거부했다. " +
			"SA에 Play Console 권한을 부여했는지 확인해라 " +
			"(Play Console > 사용자 및 권한 > 초대)")

	default:
		t.Fatalf("예상하지 못한 코드다: %s — 에러 매핑을 확인해라", code)
	}
}

// SA가 어느 범위까지 접근되는지 기록한다.
//
// 실측 결과 inappproducts는 403이지만 이유가 권한이 아니었다.
//
//	"Please migrate to the new publishing API."
//
// 이 엔드포인트가 deprecated된 것이고 SA 권한은 정상이다.
// 우리가 쓰는 purchases.productsv2는 잘 동작한다.
//
// 403을 보고 "권한이 없다"고 단정하면 엉뚱한 곳을 고치게 된다.
// 그래서 본문까지 로그로 남긴다.
func TestPlayCredentialScope(t *testing.T) {
	envOrSkip(t, "GOOGLE_APPLICATION_CREDENTIALS")
	packageName := envOrSkip(t, "IAP_PLAY_PACKAGE_NAME")

	ctx, cancel := context.WithTimeout(context.Background(), marketTimeout)
	defer cancel()

	httpClient, err := google.DefaultClient(ctx, playScope)
	if err != nil {
		t.Fatalf("Play 자격증명을 얻지 못했다: %v", err)
	}

	url := "https://androidpublisher.googleapis.com/androidpublisher/v3/applications/" +
		packageName + "/inappproducts"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("요청 생성 실패: %v", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("Play 호출 실패: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	t.Logf("inappproducts 응답 %d: %s", resp.StatusCode, body)

	// 이 엔드포인트는 우리가 쓰지 않는다. 403이어도 구매 검증에는
	// 지장이 없다 — 실제로 deprecation 때문에 403이 온다.
	if resp.StatusCode == http.StatusForbidden {
		t.Log("inappproducts는 deprecated다. 구매 검증 경로와 무관하다")
	}
}
