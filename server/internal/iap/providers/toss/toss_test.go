package toss

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// fake HTTP 서버로 검증한다.
//
// mTLS 인증서와 상품 ID는 아직 미확보라 실제 AIT 호출은 못 하지만,
// 응답 해석·상태 매핑·타임스탬프 처리는 여기서 전부 확인한다.

var tossNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

// testCert는 mTLS 설정 검사를 통과할 자체 서명 인증서다.
func testCert(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("키 생성 실패: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "테스트 클라이언트"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("인증서 생성 실패: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// newFakeVerifier는 fake 서버에 붙은 검증기를 만든다.
//
// mTLS는 New의 설정 검사만 통과시키고, 실제 통신은 평문 fake로 한다.
// TLS 핸드셰이크 자체는 우리 코드가 아니라 표준 라이브러리의 몫이다.
func newFakeVerifier(t *testing.T, handler http.HandlerFunc) *Verifier {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	v, err := New(Config{ClientCert: testCert(t), BaseURL: srv.URL},
		WithHTTPClient(srv.Client()),
		WithClock(func() time.Time { return tossNow }),
	)
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}
	return v
}

func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

const testOrderID = "ait-order-0001"
const testSKU = "ait_gecko_galaxy"

// successBody는 결제 완료 응답이다. 상태만 바꿔가며 재사용한다.
func successBody(status, determinedAt string) string {
	return `{
      "resultType": "SUCCESS",
      "success": {
        "orderId": "` + testOrderID + `",
        "sku": "` + testSKU + `",
        "status": "` + status + `",
        "statusDeterminedAt": "` + determinedAt + `"
      }
    }`
}

func tossProof() domain.Proof {
	return domain.Proof{
		Platform:   domain.PlatformAppsInToss,
		ProductID:  testSKU,
		Token:      testOrderID,
		AITUserKey: "toss-user-key-abc",
	}
}

func TestVerifyPaymentCompleted(t *testing.T) {
	var gotUserKey, gotPath string

	v := newFakeVerifier(t, func(w http.ResponseWriter, r *http.Request) {
		gotUserKey = r.Header.Get("x-toss-user-key")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(successBody(statusPaymentCompleted, "2026-07-31T20:30:00")))
	})

	got, err := v.Verify(context.Background(), tossProof())
	if err != nil {
		t.Fatalf("검증 실패: %v", err)
	}

	// 불변식 1. AIT의 canonicalId는 orderId다.
	if got.CanonicalID != testOrderID {
		t.Errorf("canonicalId = %q, want orderId", got.CanonicalID)
	}
	if got.State != domain.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
	// 완료 처리는 클라이언트가 한다
	if got.Completion != domain.CompletionAppsInTossClient {
		t.Errorf("completion = %q, want apps_in_toss_client_complete", got.Completion)
	}
	if got.PlatformAccountID != "toss-user-key-abc" {
		t.Errorf("platformAccountId = %q", got.PlatformAccountID)
	}
	if !got.ObservedAt.Equal(tossNow) {
		t.Errorf("observedAt = %v, want 서버 관측 시각", got.ObservedAt)
	}

	// 사용자 신원은 헤더로 간다. body가 아니다.
	if gotUserKey != "toss-user-key-abc" {
		t.Errorf("x-toss-user-key = %q", gotUserKey)
	}
	if gotPath != orderStatusPath {
		t.Errorf("path = %q, want %q", gotPath, orderStatusPath)
	}
}

// PURCHASED는 이미 지급까지 끝난 상태다. 완료 처리를 또 시키지 않는다.
func TestVerifyPurchasedNeedsNoCompletion(t *testing.T) {
	v := newFakeVerifier(t, jsonHandler(http.StatusOK,
		successBody(statusPurchased, "2026-07-31T20:30:00")))

	got, err := v.Verify(context.Background(), tossProof())
	if err != nil {
		t.Fatalf("검증 실패: %v", err)
	}
	if got.State != domain.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
	if got.Completion != domain.CompletionNone {
		t.Errorf("completion = %q, want none", got.Completion)
	}
}

func TestVerifyStateMapping(t *testing.T) {
	tests := []struct {
		status    string
		want      domain.State
		wantError platformerr.Code
	}{
		{status: statusPurchased, want: domain.StateActive},
		{status: statusPaymentCompleted, want: domain.StateActive},
		{status: statusRefunded, want: domain.StateRevoked},
		{status: statusInProgress, want: domain.StatePending},
		{status: "FAILED", wantError: platformerr.CodePurchaseInvalid},
		{status: "NOT_FOUND", wantError: platformerr.CodePurchaseInvalid},
		{status: "MINIAPP_MISMATCH", wantError: platformerr.CodePurchaseInvalid},
		// ERROR는 AIT가 확정하지 못한 것이다. 재시도 가능해야 한다.
		{status: statusError, wantError: platformerr.CodeProviderUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			v := newFakeVerifier(t, jsonHandler(http.StatusOK,
				successBody(tt.status, "2026-07-31T20:30:00")))

			got, err := v.Verify(context.Background(), tossProof())

			if tt.wantError != "" {
				if code := platformerr.CodeOf(err); code != tt.wantError {
					t.Errorf("code = %q, want %q", code, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("검증 실패: %v", err)
			}
			if got.State != tt.want {
				t.Errorf("state = %q, want %q", got.State, tt.want)
			}
		})
	}
}

// ERROR는 재시도 가치가 있다. 워커가 다시 집어야 한다.
func TestErrorStatusIsRetryable(t *testing.T) {
	v := newFakeVerifier(t, jsonHandler(http.StatusOK,
		successBody(statusError, "2026-07-31T20:30:00")))

	_, err := v.Verify(context.Background(), tossProof())
	if !platformerr.IsRetryableErr(err) {
		t.Error("ERROR를 재시도 불가로 분류했다")
	}
}

// 다른 주문의 상태를 우리 주문 결과로 받아들이면 안 된다.
func TestVerifyRejectsOrderMismatch(t *testing.T) {
	body := strings.Replace(successBody(statusPaymentCompleted, "2026-07-31T20:30:00"),
		testOrderID, "다른주문", 1)
	v := newFakeVerifier(t, jsonHandler(http.StatusOK, body))

	_, err := v.Verify(context.Background(), tossProof())
	if code := platformerr.CodeOf(err); code != platformerr.CodePurchaseReplayMismatch {
		t.Errorf("code = %q, want purchase_replay_mismatch", code)
	}
}

func TestVerifyRejectsSKUMismatch(t *testing.T) {
	body := strings.Replace(successBody(statusPaymentCompleted, "2026-07-31T20:30:00"),
		testSKU, "다른상품", 1)
	v := newFakeVerifier(t, jsonHandler(http.StatusOK, body))

	_, err := v.Verify(context.Background(), tossProof())
	if code := platformerr.CodeOf(err); code != platformerr.CodeProductMismatch {
		t.Errorf("code = %q, want product_mismatch", code)
	}
}

// 타임존 없는 값은 KST다. UTC로 읽으면 9시간 앞서서 stale 비교가 뒤집힌다.
func TestTimestampParsing(t *testing.T) {
	v := newFakeVerifier(t, jsonHandler(http.StatusOK, `{}`))

	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{
			"타임존 없으면 KST로 읽는다",
			"2026-07-31T20:30:00",
			time.Date(2026, 7, 31, 11, 30, 0, 0, time.UTC),
		},
		{
			"타임존이 있으면 그대로 쓴다",
			"2026-07-31T11:30:00Z",
			time.Date(2026, 7, 31, 11, 30, 0, 0, time.UTC),
		},
		{"빈 값", "", time.Time{}},
		{"공백만", "   ", time.Time{}},
		{"해석 불가", "어제쯤", time.Time{}},
		// 마켓이 준 미래 시각으로 원장 순서를 뒤집지 못하게 한다
		{"너무 먼 미래", "2027-01-01T00:00:00Z", time.Time{}},
		// 약간의 오차는 허용한다
		{
			"허용 범위 안의 미래",
			"2026-07-31T12:01:00Z",
			time.Date(2026, 7, 31, 12, 1, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := v.parseTimestamp(tt.in); !got.Equal(tt.want) {
				t.Errorf("= %v, want %v", got, tt.want)
			}
		})
	}
}

// 확정된 주문에 시각이 없으면 stale 억제가 성립하지 않는다.
func TestVerifyRequiresTimestampWhenSettled(t *testing.T) {
	for _, status := range []string{statusPaymentCompleted, statusRefunded} {
		t.Run(status, func(t *testing.T) {
			v := newFakeVerifier(t, jsonHandler(http.StatusOK, successBody(status, "")))

			_, err := v.Verify(context.Background(), tossProof())
			if code := platformerr.CodeOf(err); code != platformerr.CodeProviderResponseInvalid {
				t.Errorf("code = %q, want provider_response_invalid", code)
			}
		})
	}

	// 진행 중인 주문에는 확정 시각이 없어도 된다
	t.Run("ORDER_IN_PROGRESS는 시각 없이 통과", func(t *testing.T) {
		v := newFakeVerifier(t, jsonHandler(http.StatusOK, successBody(statusInProgress, "")))

		got, err := v.Verify(context.Background(), tossProof())
		if err != nil {
			t.Fatalf("검증 실패: %v", err)
		}
		if got.State != domain.StatePending {
			t.Errorf("state = %q, want pending", got.State)
		}
	})
}

func TestVerifyRejectsBadEnvelope(t *testing.T) {
	tests := []struct {
		name string
		body string
		code platformerr.Code
	}{
		{"resultType이 SUCCESS가 아니다",
			`{"resultType":"FAIL","error":{"code":"x"}}`,
			platformerr.CodeProviderResponseInvalid},
		{"success가 없다",
			`{"resultType":"SUCCESS"}`,
			platformerr.CodeProviderResponseInvalid},
		{"orderId가 비었다",
			`{"resultType":"SUCCESS","success":{"orderId":"","sku":"x","status":"PURCHASED"}}`,
			platformerr.CodeProviderResponseInvalid},
		{"status가 비었다",
			`{"resultType":"SUCCESS","success":{"orderId":"o","sku":"x","status":""}}`,
			platformerr.CodeProviderResponseInvalid},
		{"JSON이 깨졌다", `{"resultType":`, platformerr.CodeProviderResponseInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newFakeVerifier(t, jsonHandler(http.StatusOK, tt.body))

			_, err := v.Verify(context.Background(), tossProof())
			if code := platformerr.CodeOf(err); code != tt.code {
				t.Errorf("code = %q, want %q", code, tt.code)
			}
		})
	}
}

func TestVerifyHTTPErrorMapping(t *testing.T) {
	tests := []struct {
		status    int
		wantCode  platformerr.Code
		retryable bool
	}{
		// mTLS 인증서 문제다. 재시도해도 같다.
		{http.StatusUnauthorized, platformerr.CodeProviderAuthFailed, false},
		{http.StatusForbidden, platformerr.CodeProviderAuthFailed, false},
		{http.StatusNotFound, platformerr.CodePurchaseNotFound, true},
		{http.StatusRequestTimeout, platformerr.CodeProviderUnavailable, true},
		{http.StatusTooManyRequests, platformerr.CodeProviderUnavailable, true},
		{http.StatusBadRequest, platformerr.CodePurchaseInvalid, false},
		{http.StatusInternalServerError, platformerr.CodeProviderUnavailable, true},
		{http.StatusBadGateway, platformerr.CodeProviderUnavailable, true},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			v := newFakeVerifier(t, jsonHandler(tt.status, `{"error":{}}`))

			_, err := v.Verify(context.Background(), tossProof())
			if err == nil {
				t.Fatal("에러를 기대했는데 성공했다")
			}
			if code := platformerr.CodeOf(err); code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if got := platformerr.IsRetryableErr(err); got != tt.retryable {
				t.Errorf("재시도 가능 = %v, want %v", got, tt.retryable)
			}
		})
	}
}

func TestVerifyRejectsBadInput(t *testing.T) {
	v := newFakeVerifier(t, jsonHandler(http.StatusOK,
		successBody(statusPurchased, "2026-07-31T20:30:00")))

	tests := []struct {
		name  string
		mutID func(*domain.Proof)
		code  platformerr.Code
	}{
		{"다른 마켓 증명",
			func(p *domain.Proof) { p.Platform = domain.PlatformGooglePlay },
			platformerr.CodePlatformMismatch},
		{"빈 주문 번호",
			func(p *domain.Proof) { p.Token = "" },
			platformerr.CodeProofInvalid},
		{"빈 상품",
			func(p *domain.Proof) { p.ProductID = "" },
			platformerr.CodeProofInvalid},
		// AITUserKey는 계정 바인딩을 대신한다. 없으면 검증할 수 없다.
		{"사용자 키 없음",
			func(p *domain.Proof) { p.AITUserKey = "" },
			platformerr.CodeProofInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tossProof()
			tt.mutID(&p)
			_, err := v.Verify(context.Background(), p)
			if code := platformerr.CodeOf(err); code != tt.code {
				t.Errorf("code = %q, want %q", code, tt.code)
			}
		})
	}
}

// 서버가 완료 처리를 하면 안 된다. 불리는 것 자체가 배선 실수다.
func TestCompleteGrantIsClientSide(t *testing.T) {
	v := newFakeVerifier(t, jsonHandler(http.StatusOK, `{}`))

	t.Run("AIT 구매는 거부", func(t *testing.T) {
		err := v.CompleteGrant(context.Background(), domain.VerifiedPurchase{
			Platform:   domain.PlatformAppsInToss,
			Completion: domain.CompletionAppsInTossClient,
		})
		if code := platformerr.CodeOf(err); code != platformerr.CodeCompletionMismatch {
			t.Errorf("code = %q, want completion_mismatch", code)
		}
	})

	t.Run("다른 마켓 구매도 거부", func(t *testing.T) {
		err := v.CompleteGrant(context.Background(), domain.VerifiedPurchase{
			Platform: domain.PlatformGooglePlay,
		})
		if code := platformerr.CodeOf(err); code != platformerr.CodePlatformMismatch {
			t.Errorf("code = %q, want platform_mismatch", code)
		}
	})
}

// 타임아웃은 재시도 가능이다.
func TestVerifyTimeout(t *testing.T) {
	// 핸들러를 r.Context()만으로 붙잡으면 안 된다.
	// POST 요청 body를 읽지 않는 핸들러는 클라이언트가 끊어도
	// 취소를 통보받지 못해서, srv.Close가 영원히 기다린다.
	//
	// defer로 푼다. t.Cleanup에 걸면 LIFO 때문에 나중에 등록된
	// srv.Close가 먼저 돌아 같은 교착에 빠진다.
	release := make(chan struct{})
	defer close(release)

	v := newFakeVerifier(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := v.Verify(ctx, tossProof())
	if err == nil {
		t.Fatal("타임아웃을 기대했는데 성공했다")
	}
	if !platformerr.IsRetryableErr(err) {
		t.Errorf("타임아웃을 재시도 불가로 분류했다: %v", err)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("인증서 없이 통과시켰다")
	}

	v, err := New(Config{ClientCert: testCert(t)})
	if err != nil {
		t.Fatalf("생성 실패: %v", err)
	}
	if v.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", v.baseURL, DefaultBaseURL)
	}
	if v.Platform() != domain.PlatformAppsInToss {
		t.Errorf("platform = %q", v.Platform())
	}
}
