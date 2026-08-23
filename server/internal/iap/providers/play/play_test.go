package play

import (
	"context"
	"io"
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
// 실제 Play 자격증명 없이도 검증 로직, 에러 매핑, 상태 전이를 확인할 수 있다.
// 원본 lizard-tycoon의 providers.unit.test.ts 744줄도 같은 방식이다.
// 실제 마켓 샌드박스 검증은 자격증명 확보 후 별도로 한다.

const testPackage = "com.seorilabs.lizardtycoon"

var fixedNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

// newFakeServer는 응답을 지정할 수 있는 fake Play API다.
func newFakeServer(t *testing.T, handler http.HandlerFunc) (*Verifier, *httptest.Server) {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	v, err := New(testPackage, srv.Client(),
		WithBaseURL(srv.URL),
		WithClock(func() time.Time { return fixedNow }),
	)
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}
	return v, srv
}

// jsonResponse는 고정 JSON을 돌려주는 핸들러다.
func jsonResponse(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

const purchasedBody = `{
  "kind": "androidpublisher#purchase",
  "purchaseStateContext": {"purchaseState": "PURCHASED"},
  "orderId": "GPA.1234-5678",
  "obfuscatedExternalAccountId": "account-ref-abc",
  "productLineItem": [
    {"productId": "gecko_galaxy", "productOfferDetails": {"quantity": 1}}
  ],
  "purchaseCompletionTime": "2026-07-31T11:00:00Z",
  "acknowledgementState": "ACKNOWLEDGEMENT_STATE_PENDING"
}`

func testProof() domain.Proof {
	return domain.Proof{
		Platform:    domain.PlatformGooglePlay,
		ProductID:   "gecko_galaxy",
		ProductType: domain.ProductNonConsumable,
		Token:       "purchase-token-xyz",
	}
}

const consumableBody = `{
  "purchaseTimeMillis": "1785495600000",
  "purchaseState": 0,
  "consumptionState": 0,
  "orderId": "GPA.9876-5432",
  "obfuscatedExternalAccountId": "account-ref-abc",
  "quantity": 1
}`

func TestVerifyPurchased(t *testing.T) {
	v, _ := newFakeServer(t, jsonResponse(http.StatusOK, purchasedBody))

	got, err := v.Verify(context.Background(), testProof())
	if err != nil {
		t.Fatalf("검증 실패: %v", err)
	}

	if got.State != domain.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
	// 불변식 1. Play의 canonicalId는 purchaseToken이다.
	if got.CanonicalID != "purchase-token-xyz" {
		t.Errorf("canonicalId = %q, want purchaseToken", got.CanonicalID)
	}
	if got.ProviderOrderID != "GPA.1234-5678" {
		t.Errorf("providerOrderId = %q", got.ProviderOrderID)
	}
	if got.PlatformAccountID != "account-ref-abc" {
		t.Errorf("platformAccountId = %q", got.PlatformAccountID)
	}
	if got.Completion != domain.CompletionGoogleAcknowledge {
		t.Errorf("completion = %q, want google_acknowledge", got.Completion)
	}
	if !got.ObservedAt.Equal(fixedNow) {
		t.Errorf("observedAt = %v, want %v", got.ObservedAt, fixedNow)
	}
	if got.PurchasedAt.IsZero() {
		t.Error("purchasedAt이 비었다")
	}
}

func TestVerifyConsumableUsesKnownProductEndpoint(t *testing.T) {
	var gotPath string
	v, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		jsonResponse(http.StatusOK, consumableBody)(w, r)
	})
	proof := testProof()
	proof.ProductType = domain.ProductConsumable

	got, err := v.Verify(context.Background(), proof)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotPath, "productsv2") || !strings.Contains(gotPath, "/products/gecko_galaxy/tokens/") {
		t.Fatalf("path=%q", gotPath)
	}
	if got.Completion != domain.CompletionGoogleConsume {
		t.Fatalf("completion=%q, want google_consume", got.Completion)
	}
	if got.CanonicalID != proof.Token || got.ProductID != proof.ProductID || got.PurchasedAt.IsZero() {
		t.Fatalf("purchase=%+v", got)
	}
}

func TestVerifyConsumedPurchaseDoesNotConsumeAgain(t *testing.T) {
	body := strings.Replace(consumableBody, `"consumptionState": 0`, `"consumptionState": 1`, 1)
	v, _ := newFakeServer(t, jsonResponse(http.StatusOK, body))
	proof := testProof()
	proof.ProductType = domain.ProductConsumable

	got, err := v.Verify(context.Background(), proof)
	if err != nil {
		t.Fatal(err)
	}
	if got.Completion != domain.CompletionNone {
		t.Fatalf("completion=%q, want none", got.Completion)
	}
}

func TestVerifyConsumableRejectsMultipleQuantity(t *testing.T) {
	body := strings.Replace(consumableBody, `"quantity": 1`, `"quantity": 2`, 1)
	v, _ := newFakeServer(t, jsonResponse(http.StatusOK, body))
	proof := testProof()
	proof.ProductType = domain.ProductConsumable

	_, err := v.Verify(context.Background(), proof)
	if platformerr.CodeOf(err) != platformerr.CodeProviderResponseInvalid {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestVerifyStateMapping(t *testing.T) {
	tests := []struct {
		playState string
		want      domain.State
	}{
		{"PURCHASED", domain.StateActive},
		{"PENDING", domain.StatePending},
		{"CANCELLED", domain.StateRevoked},
	}

	for _, tt := range tests {
		t.Run(tt.playState, func(t *testing.T) {
			body := strings.Replace(purchasedBody, "PURCHASED", tt.playState, 1)
			v, _ := newFakeServer(t, jsonResponse(http.StatusOK, body))

			got, err := v.Verify(context.Background(), testProof())
			if err != nil {
				t.Fatalf("검증 실패: %v", err)
			}
			if got.State != tt.want {
				t.Errorf("state = %q, want %q", got.State, tt.want)
			}
		})
	}

	t.Run("모르는 상태는 거부", func(t *testing.T) {
		body := strings.Replace(purchasedBody, "PURCHASED", "SOMETHING_NEW", 1)
		v, _ := newFakeServer(t, jsonResponse(http.StatusOK, body))

		_, err := v.Verify(context.Background(), testProof())
		if code := platformerr.CodeOf(err); code != platformerr.CodePurchaseInvalid {
			t.Errorf("code = %q, want purchase_invalid", code)
		}
	})
}

// 이미 acknowledge된 구매는 다시 부르지 않는다.
func TestVerifySkipsAcknowledgedCompletion(t *testing.T) {
	body := strings.Replace(purchasedBody,
		"ACKNOWLEDGEMENT_STATE_PENDING", "ACKNOWLEDGEMENT_STATE_ACKNOWLEDGED", 1)
	v, _ := newFakeServer(t, jsonResponse(http.StatusOK, body))

	got, err := v.Verify(context.Background(), testProof())
	if err != nil {
		t.Fatalf("검증 실패: %v", err)
	}
	if got.Completion != domain.CompletionNone {
		t.Errorf("completion = %q, want none", got.Completion)
	}
}

func TestVerifyProductMismatch(t *testing.T) {
	body := strings.Replace(purchasedBody, "gecko_galaxy", "다른상품", 1)
	v, _ := newFakeServer(t, jsonResponse(http.StatusOK, body))

	_, err := v.Verify(context.Background(), testProof())
	if code := platformerr.CodeOf(err); code != platformerr.CodeProductMismatch {
		t.Errorf("code = %q, want product_mismatch", code)
	}
}

// 여러 상품이 한 토큰에 묶이면 어느 entitlement를 줄지 정할 수 없다.
func TestVerifyRejectsMultipleLineItems(t *testing.T) {
	body := `{
      "purchaseStateContext": {"purchaseState": "PURCHASED"},
      "productLineItem": [
        {"productId": "a"}, {"productId": "b"}
      ]
    }`
	v, _ := newFakeServer(t, jsonResponse(http.StatusOK, body))

	_, err := v.Verify(context.Background(), testProof())
	if code := platformerr.CodeOf(err); code != platformerr.CodeProviderResponseInvalid {
		t.Errorf("code = %q, want provider_response_invalid", code)
	}
}

// HTTP 상태 매핑이 재시도 가능 여부를 가른다.
func TestVerifyHTTPErrorMapping(t *testing.T) {
	tests := []struct {
		status    int
		wantCode  platformerr.Code
		retryable bool
	}{
		// 마켓 반영 지연일 수 있어 재시도 가능
		{http.StatusNotFound, platformerr.CodePurchaseNotFound, true},
		{http.StatusBadRequest, platformerr.CodePurchaseInvalid, false},
		// 설정 문제라 재시도해도 같다
		{http.StatusUnauthorized, platformerr.CodeProviderAuthFailed, false},
		{http.StatusForbidden, platformerr.CodeProviderAuthFailed, false},
		{http.StatusTooManyRequests, platformerr.CodeProviderUnavailable, true},
		{http.StatusInternalServerError, platformerr.CodeProviderUnavailable, true},
		{http.StatusBadGateway, platformerr.CodeProviderUnavailable, true},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			v, _ := newFakeServer(t, jsonResponse(tt.status, `{"error":{}}`))

			_, err := v.Verify(context.Background(), testProof())
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
	v, _ := newFakeServer(t, jsonResponse(http.StatusOK, purchasedBody))

	t.Run("다른 마켓 증명", func(t *testing.T) {
		p := testProof()
		p.Platform = domain.PlatformAppStore
		_, err := v.Verify(context.Background(), p)
		if code := platformerr.CodeOf(err); code != platformerr.CodePlatformMismatch {
			t.Errorf("code = %q, want platform_mismatch", code)
		}
	})

	t.Run("빈 토큰", func(t *testing.T) {
		p := testProof()
		p.Token = ""
		_, err := v.Verify(context.Background(), p)
		if code := platformerr.CodeOf(err); code != platformerr.CodeProofInvalid {
			t.Errorf("code = %q, want purchase_proof_invalid", code)
		}
	})
}

// acknowledge는 정확한 경로로 POST 해야 한다.
func TestCompleteGrantCallsAcknowledge(t *testing.T) {
	var gotPath, gotMethod string

	v, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	p := domain.VerifiedPurchase{
		Platform:    domain.PlatformGooglePlay,
		ProductID:   "gecko_galaxy",
		CanonicalID: "purchase-token-xyz",
		Completion:  domain.CompletionGoogleAcknowledge,
	}

	if err := v.CompleteGrant(context.Background(), p); err != nil {
		t.Fatalf("완료 처리 실패: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if !strings.Contains(gotPath, ":acknowledge") {
		t.Errorf("path = %q, acknowledge가 없다", gotPath)
	}
	if !strings.Contains(gotPath, "purchase-token-xyz") {
		t.Errorf("path = %q, 토큰이 없다", gotPath)
	}
}

func TestCompleteGrantCallsConsume(t *testing.T) {
	var gotPath, gotMethod, gotBody string
	v, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusNoContent)
	})

	err := v.CompleteGrant(context.Background(), domain.VerifiedPurchase{
		Platform: domain.PlatformGooglePlay, ProductID: "gecko_galaxy",
		CanonicalID: "purchase-token-xyz", Completion: domain.CompletionGoogleConsume,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || !strings.HasSuffix(gotPath, ":consume") {
		t.Fatalf("method=%s path=%q", gotMethod, gotPath)
	}
	if gotBody != "" {
		t.Fatalf("consume body=%q, want empty", gotBody)
	}
}

// Play는 acknowledge가 성공하면 204 No Content를 준다.
//
// 200만 성공으로 보면 성공한 완료 처리가 실패로 기록되고, 워커가
// 영원히 재시도하다 dead-letter로 간다. 그 사이 유저는 이미 물건을
// 받은 상태고, 원장에는 "완료하지 못한 주문"으로 남는다.
//
// 위 테스트가 200을 돌려주는 가짜 서버라 이 결함을 잡지 못했다.
// 실제 활성 구매에 acknowledge를 보내고 나서야 드러났다 — 환불된
// 구매로는 Play가 400을 주기 때문에 이 경로를 타지 않는다.
func TestCompleteGrantAcceptsNoContent(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"204 No Content", http.StatusNoContent, ""},
		{"200 빈 본문", http.StatusOK, ""},
		{"200 빈 객체", http.StatusOK, `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				if tt.body != "" {
					_, _ = w.Write([]byte(tt.body))
				}
			})

			err := v.CompleteGrant(context.Background(), domain.VerifiedPurchase{
				Platform:    domain.PlatformGooglePlay,
				ProductID:   "gecko_galaxy",
				CanonicalID: "purchase-token-xyz",
				Completion:  domain.CompletionGoogleAcknowledge,
			})
			if err != nil {
				t.Fatalf("완료 처리를 실패로 봤다: %v", err)
			}
		})
	}
}

func TestCompleteGrantRejectsWrongCompletion(t *testing.T) {
	v, _ := newFakeServer(t, jsonResponse(http.StatusOK, `{}`))

	p := domain.VerifiedPurchase{
		Platform:   domain.PlatformGooglePlay,
		Completion: domain.CompletionAppleFinish, // 잘못된 방식
	}
	err := v.CompleteGrant(context.Background(), p)
	if code := platformerr.CodeOf(err); code != platformerr.CodeCompletionMismatch {
		t.Errorf("code = %q, want completion_mismatch", code)
	}
}

// 타임아웃은 재시도 가능으로 분류한다. 워커가 다시 집는다.
func TestVerifyTimeout(t *testing.T) {
	// 핸들러를 r.Context()만으로 붙잡으면 srv.Close가 영원히 기다릴 수
	// 있다. 클라이언트가 끊어도 서버가 통보받지 못하는 경우가 있다.
	// defer로 푼다. t.Cleanup은 LIFO라 srv.Close가 먼저 돌아 안 된다.
	release := make(chan struct{})
	defer close(release)

	v, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := v.Verify(ctx, testProof())
	if err == nil {
		t.Fatal("타임아웃을 기대했는데 성공했다")
	}
	code := platformerr.CodeOf(err)
	if code != platformerr.CodeProviderTimeout && code != platformerr.CodeProviderUnavailable {
		t.Errorf("code = %q, want provider_timeout 또는 provider_unavailable", code)
	}
	if !platformerr.IsRetryableErr(err) {
		t.Error("타임아웃이 재시도 불가로 분류됐다")
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New("", http.DefaultClient); err == nil {
		t.Error("빈 패키지 이름을 허용했다")
	}
	if _, err := New(testPackage, nil); err == nil {
		t.Error("nil 클라이언트를 허용했다")
	}
}
