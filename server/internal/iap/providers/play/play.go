// Package play는 Google Play 구매를 검증한다.
//
// Play Developer API의 productsv2 엔드포인트를 쓴다.
// 자격증명은 ADC다. SA JSON 키를 배포하지 않는다는 조직 원칙을 지킨다.
// 런타임 SA에 Play Console 권한을 부여하는 방식이다.
package play

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/refundreview"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// DefaultBaseURL은 Play Developer API 주소다.
const DefaultBaseURL = "https://androidpublisher.googleapis.com"

// requestTimeout은 마켓 호출 상한이다.
// 원본과 같은 8초다. 이보다 길면 결제 화면이 멈춘 것처럼 보인다.
const requestTimeout = 8 * time.Second

// maxResponseBytes는 응답 크기 상한이다.
// 마켓 응답이 이상하게 커도 메모리를 다 쓰지 않게 한다.
const maxResponseBytes = 1 << 20

// purchaseState는 productsv2 응답의 구매 상태다.
const (
	statePurchased = "PURCHASED"
	statePending   = "PENDING"
	stateCancelled = "CANCELLED"
)

// productsV2Response는 purchases.productsv2.gettokens 응답이다.
//
// 필요한 필드만 정의한다. 미지 필드는 무시한다.
type productsV2Response struct {
	Kind                 string `json:"kind"`
	PurchaseStateContext struct {
		PurchaseState string `json:"purchaseState"`
	} `json:"purchaseStateContext"`
	TestPurchaseContext *struct {
		FopType string `json:"fopType"`
	} `json:"testPurchaseContext"`
	OrderID                     string `json:"orderId"`
	ObfuscatedExternalAccountID string `json:"obfuscatedExternalAccountId"`
	ProductLineItem             []struct {
		ProductID           string `json:"productId"`
		ProductOfferDetails struct {
			Quantity int64 `json:"quantity"`
		} `json:"productOfferDetails"`
	} `json:"productLineItem"`
	PurchaseCompletionTime string `json:"purchaseCompletionTime"`
	AcknowledgementState   string `json:"acknowledgementState"`
}

// Verifier는 Play 구매 검증기다.
type Verifier struct {
	packageName string
	client      *http.Client
	baseURL     string
	now         func() time.Time
}

// Option은 검증기 설정이다.
type Option func(*Verifier)

// WithBaseURL은 API 주소를 바꾼다. 테스트에서 fake 서버를 꽂는다.
func WithBaseURL(u string) Option {
	return func(v *Verifier) { v.baseURL = u }
}

// WithClock은 시계를 주입한다.
func WithClock(now func() time.Time) Option {
	return func(v *Verifier) { v.now = now }
}

// New는 검증기를 만든다.
//
// client는 ADC로 인증된 클라이언트여야 한다.
// google.golang.org/api/option과 oauth2/google로 만든다.
func New(packageName string, client *http.Client, opts ...Option) (*Verifier, error) {
	if packageName == "" {
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"Play 패키지 이름이 필요해요")
	}
	if client == nil {
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"Play 인증 클라이언트가 필요해요")
	}

	v := &Verifier{
		packageName: packageName,
		client:      client,
		baseURL:     DefaultBaseURL,
		now:         time.Now,
	}
	for _, o := range opts {
		o(v)
	}
	return v, nil
}

func (v *Verifier) Platform() domain.Platform { return domain.PlatformGooglePlay }

// Verify는 구매 토큰으로 Play에 확인한다.
func (v *Verifier) Verify(ctx context.Context, proof domain.Proof) (domain.VerifiedPurchase, error) {
	if proof.Platform != domain.PlatformGooglePlay {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodePlatformMismatch,
			"Play 검증기에 다른 마켓 증명이 왔어요")
	}
	// productId는 없어도 된다. 조회는 토큰만으로 하고 상품은 응답에서 온다.
	// 환불 알림에는 sku가 실려오지 않아 이 경로가 필요하다.
	if proof.Token == "" {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProofInvalid,
			"구매 정보가 비어 있어요")
	}

	observedAt := v.now().UTC()

	endpoint := fmt.Sprintf("%s/androidpublisher/v3/applications/%s/purchases/productsv2/tokens/%s",
		v.baseURL, url.PathEscape(v.packageName), url.PathEscape(proof.Token))

	var resp productsV2Response
	if err := v.doJSON(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
		return domain.VerifiedPurchase{}, err
	}

	// 원본과 같은 제약이다. 여러 상품이 한 토큰에 묶이면
	// 어느 entitlement를 줄지 정할 수 없다.
	if len(resp.ProductLineItem) != 1 {
		return domain.VerifiedPurchase{}, platformerr.Newf(platformerr.CodeProviderResponseInvalid,
			"구매 항목이 %d개예요", len(resp.ProductLineItem))
	}

	item := resp.ProductLineItem[0]
	// 요청이 상품을 지정했으면 대조한다. 지정하지 않았으면 응답을 따른다.
	if proof.ProductID != "" && item.ProductID != proof.ProductID {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProductMismatch,
			"구매한 상품이 요청과 달라요")
	}

	state := mapPurchaseState(resp.PurchaseStateContext.PurchaseState)
	if state == domain.StateInvalid {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodePurchaseInvalid,
			"구매 상태를 확인할 수 없어요")
	}

	purchasedAt := parseRFC3339(resp.PurchaseCompletionTime)

	// 이미 acknowledge된 구매는 다시 부르지 않는다.
	completion := domain.CompletionGoogleAcknowledge
	if state != domain.StateActive || resp.AcknowledgementState == "ACKNOWLEDGEMENT_STATE_ACKNOWLEDGED" {
		completion = domain.CompletionNone
	}

	return domain.VerifiedPurchase{
		Platform:          domain.PlatformGooglePlay,
		ProductID:         item.ProductID,
		CanonicalID:       proof.Token, // 불변식 1. Play는 purchaseToken이다
		ProviderOrderID:   resp.OrderID,
		PlatformAccountID: resp.ObfuscatedExternalAccountID,
		PurchasedAt:       purchasedAt,
		ObservedAt:        observedAt,
		State:             state,
		Completion:        completion,
	}, nil
}

// CompleteGrant는 Play에 acknowledge를 보낸다.
//
// 3일 안에 하지 않으면 Play가 자동 환불한다.
// 실패해도 지급은 롤백하지 않고 워커가 재시도한다. 불변식 7이다.
func (v *Verifier) CompleteGrant(ctx context.Context, p domain.VerifiedPurchase) error {
	if p.Platform != domain.PlatformGooglePlay {
		return platformerr.New(platformerr.CodePlatformMismatch,
			"Play 완료 처리에 다른 마켓 구매가 왔어요")
	}
	if p.Completion != domain.CompletionGoogleAcknowledge {
		return platformerr.New(platformerr.CodeCompletionMismatch,
			"완료 처리 방식이 올바르지 않아요")
	}

	endpoint := fmt.Sprintf(
		"%s/androidpublisher/v3/applications/%s/purchases/products/%s/tokens/%s:acknowledge",
		v.baseURL,
		url.PathEscape(v.packageName),
		url.PathEscape(p.ProductID),
		url.PathEscape(p.CanonicalID),
	)

	return v.doJSON(ctx, http.MethodPost, endpoint, []byte(`{}`), nil)
}

// ReviewRefund는 Google Play 환불 검토 의견을 제출한다.
//
// Google은 첫 호출만 판단에 사용한다. 호출자는 외부 요청 전에 결정을
// 영구 확정하고 재시도마다 정확히 같은 input을 전달해야 한다. ADR 0014.
func (v *Verifier) ReviewRefund(ctx context.Context, in refundreview.Submission) error {
	if in.PackageName == "" || in.PackageName != v.packageName || in.OrderID == "" ||
		in.PendingRefundToken == "" || !validRefundPreference(in.RefundPreference) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"Play 환불 검토 요청이 올바르지 않아요")
	}
	body, err := json.Marshal(struct {
		PendingRefundToken    string `json:"pendingRefundToken"`
		RefundPreference      string `json:"refundPreference"`
		SampleContentProvided bool   `json:"sampleContentProvided"`
	}{
		PendingRefundToken:    in.PendingRefundToken,
		RefundPreference:      in.RefundPreference,
		SampleContentProvided: in.SampleContentProvided,
	})
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal,
			"Play 환불 검토 요청을 만들지 못했어요")
	}
	endpoint := fmt.Sprintf("%s/androidpublisher/v3/applications/%s/orders/%s:reviewrefund",
		v.baseURL, url.PathEscape(in.PackageName), url.PathEscape(in.OrderID))
	return v.doJSON(ctx, http.MethodPost, endpoint, body, nil)
}

func validRefundPreference(value string) bool {
	switch value {
	case "DECLINE", "APPROVE", "NEUTRAL":
		return true
	default:
		return false
	}
}

// doJSON은 요청을 보내고 응답을 파싱한다.
func (v *Verifier) doJSON(ctx context.Context, method, endpoint string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderConfigInvalid,
			"Play 요청을 만들지 못했어요")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := v.client.Do(req)
	if err != nil {
		// 타임아웃과 네트워크 실패를 구분한다.
		// 타임아웃은 재시도 가치가 있고 워커가 다시 집는다.
		if ctx.Err() != nil {
			return platformerr.Wrap(err, platformerr.CodeProviderTimeout,
				"Play 응답이 늦어요")
		}
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable,
			"Play에 연결하지 못했어요")
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable,
			"Play 응답을 읽지 못했어요")
	}

	// acknowledge는 성공하면 204 No Content를 준다. 200만 성공으로 보면
	// 성공한 완료 처리가 실패로 기록되고, 워커가 영원히 재시도하다가
	// dead-letter로 간다. 그 사이 유저는 이미 물건을 받은 상태다.
	//
	// 환불된 구매로는 이 경로가 드러나지 않는다 — Play가 400을 주기
	// 때문이다. 활성 구매로 실제 acknowledge를 보내고 나서야 알았다.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return mapHTTPError(resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	// 204에는 본문이 없다. 파싱하려 들면 거기서 깨진다.
	if resp.StatusCode == http.StatusNoContent || len(raw) == 0 {
		return nil
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"Play 응답을 해석하지 못했어요")
	}
	return nil
}

// mapHTTPError는 Play HTTP 상태를 플랫폼 에러로 바꾼다.
//
// 원본의 매핑을 그대로 옮겼다. 재시도 가능 여부가 여기서 갈린다.
func mapHTTPError(status int) error {
	switch status {
	case http.StatusNotFound:
		// 마켓 반영 지연일 수 있어 재시도 가능으로 둔다.
		return platformerr.New(platformerr.CodePurchaseNotFound,
			"구매 내역을 찾을 수 없어요")
	case http.StatusBadRequest:
		return platformerr.New(platformerr.CodePurchaseInvalid,
			"구매 정보가 올바르지 않아요")
	case http.StatusUnauthorized, http.StatusForbidden:
		// 설정 문제다. 재시도해도 같다.
		return platformerr.New(platformerr.CodeProviderAuthFailed,
			"Play 권한 확인에 실패했어요")
	case http.StatusTooManyRequests:
		return platformerr.New(platformerr.CodeProviderUnavailable,
			"Play 요청이 너무 많아요")
	default:
		return platformerr.Newf(platformerr.CodeProviderUnavailable,
			"Play 응답이 %d예요", status)
	}
}

func mapPurchaseState(s string) domain.State {
	switch s {
	case statePurchased:
		return domain.StateActive
	case statePending:
		return domain.StatePending
	case stateCancelled:
		return domain.StateRevoked
	default:
		return domain.StateInvalid
	}
}

func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
