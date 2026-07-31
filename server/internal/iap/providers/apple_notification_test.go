//go:build market

package providers_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/providers/apple"
)

// Apple에 테스트 알림 발송을 요청하고 결과를 조회한다.
//
// Apple이 App Store Connect에 등록된 URL로 실제 ASSN v2 알림을 보낸다.
// 응답에 Apple이 보낸 signedPayload가 들어 있으면 그것으로 우리 웹훅
// 파서를 실데이터로 검증할 수 있다.
func TestAppleTestNotification(t *testing.T) {
	if os.Getenv("APPLE_SEND_TEST_NOTIFICATION") == "" {
		t.Skip("APPLE_SEND_TEST_NOTIFICATION이 없어 건너뛴다")
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	token, err := client.RequestTestNotification(ctx)
	if err != nil {
		t.Fatalf("테스트 알림 요청 실패: %v", err)
	}
	t.Logf("테스트 알림 토큰: %s", token)

	// Apple이 보내고 응답을 받기까지 시간이 걸린다.
	var body []byte
	for i := range 10 {
		time.Sleep(3 * time.Second)
		body, err = client.TestNotificationStatus(ctx, token)
		if err == nil {
			break
		}
		t.Logf("  조회 %d회차: %v", i+1, err)
	}
	if err != nil {
		t.Fatalf("테스트 알림 상태 조회 실패: %v", err)
	}

	t.Logf("발송 결과:\n%s", body)
}
