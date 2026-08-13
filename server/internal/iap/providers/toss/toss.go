// Package toss는 AppsInToss 구매를 검증한다.
//
// 다른 두 마켓과 다른 점이 셋 있다.
//
//   - 인증이 mTLS다. 토큰이 아니라 클라이언트 인증서로 신원을 증명한다.
//   - 완료 처리를 서버가 하지 않는다. 클라이언트가 completeProductGrant를
//     부르고, 서버는 그 지시만 응답에 담는다.
//   - 계정 바인딩이 면제다. tossUserKey가 검증된 claim에서 오므로
//     그 자체가 신뢰 경로다. HMAC 참조를 따로 두지 않는다.
package toss

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// DefaultBaseURL은 AppsInToss 파트너 API 주소다.
const DefaultBaseURL = "https://apps-in-toss-api.toss.im"

// orderStatusPath는 주문 상태 조회 경로다.
const orderStatusPath = "/api-partner/v1/apps-in-toss/order/get-order-status"

// requestTimeout은 AIT 호출 상한이다. 원본과 같은 10초다.
const requestTimeout = 10 * time.Second

// maxResponseBytes는 응답 크기 상한이다.
const maxResponseBytes = 1 << 20

// futureSkew는 허용하는 미래 시각 오차다.
// 이보다 앞선 타임스탬프는 신뢰하지 않는다.
const futureSkew = 5 * time.Minute

// AIT 주문 상태다.
const (
	statusPurchased        = "PURCHASED"
	statusPaymentCompleted = "PAYMENT_COMPLETED"
	statusRefunded         = "REFUNDED"
	statusInProgress       = "ORDER_IN_PROGRESS"
	statusError            = "ERROR"
)

// orderStatusResponse는 주문 상태 조회 응답이다.
type orderStatusResponse struct {
	ResultType string `json:"resultType"`
	Success    *struct {
		OrderID            string `json:"orderId"`
		SKU                string `json:"sku"`
		Status             string `json:"status"`
		StatusDeterminedAt string `json:"statusDeterminedAt"`
		Reason             string `json:"reason"`
	} `json:"success"`
	Error json.RawMessage `json:"error"`
}

// Verifier는 AppsInToss 구매 검증기다.
type Verifier struct {
	client  *http.Client
	baseURL string
	now     func() time.Time
}

// Config는 검증기 설정이다.
type Config struct {
	// ClientCert는 mTLS 클라이언트 인증서다. AIT가 발급한다.
	ClientCert tls.Certificate
	// RootCAs는 서버 인증서 검증용 루트다. nil이면 시스템 루트를 쓴다.
	RootCAs *x509.CertPool
	// BaseURL은 API 주소다. 비우면 운영 주소를 쓴다.
	BaseURL string
}

// Option은 검증기 설정이다.
type Option func(*Verifier)

// WithClock은 시계를 주입한다.
func WithClock(now func() time.Time) Option {
	return func(v *Verifier) { v.now = now }
}

// WithHTTPClient는 HTTP 클라이언트를 갈아끼운다.
// 테스트에서 fake 서버를 꽂을 때만 쓴다.
func WithHTTPClient(c *http.Client) Option {
	return func(v *Verifier) { v.client = c }
}

// New는 mTLS 검증기를 만든다.
func New(cfg Config, opts ...Option) (*Verifier, error) {
	if len(cfg.ClientCert.Certificate) == 0 {
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid,
			"AppsInToss 클라이언트 인증서가 필요해요")
	}

	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	if _, err := url.Parse(base); err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeProviderConfigInvalid,
			"AppsInToss 주소가 올바르지 않아요")
	}

	v := &Verifier{
		client: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{cfg.ClientCert},
					RootCAs:      cfg.RootCAs,
					MinVersion:   tls.VersionTLS12,
				},
			},
		},
		baseURL: strings.TrimSuffix(base, "/"),
		now:     time.Now,
	}
	for _, o := range opts {
		o(v)
	}
	return v, nil
}

func (v *Verifier) Platform() domain.Platform { return domain.PlatformAppsInToss }

// Verify는 orderId로 AIT에 주문 상태를 확인한다.
func (v *Verifier) Verify(ctx context.Context, proof domain.Proof) (domain.VerifiedPurchase, error) {
	if proof.Platform != domain.PlatformAppsInToss {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodePlatformMismatch,
			"AppsInToss 검증기에 다른 마켓 증명이 왔어요")
	}
	// 계정 해시는 body가 아니라 검증된 appLogin 세션에서만 온다.
	// 원본 Toss userKey는 PII 최소화 원칙상 저장하거나 세션에 싣지 않는다.
	if proof.Token == "" || proof.ProductID == "" || proof.AITAccountHash == "" {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProofInvalid,
			"구매 정보가 비어 있어요")
	}

	observedAt := v.now().UTC()

	resp, err := v.fetchOrderStatus(ctx, proof.Token)
	if err != nil {
		return domain.VerifiedPurchase{}, err
	}

	if resp.ResultType != "SUCCESS" || resp.Success == nil {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"AppsInToss 주문 조회에 실패했어요")
	}
	order := resp.Success

	if order.OrderID == "" || order.SKU == "" || order.Status == "" {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"AppsInToss 응답에 주문 정보가 없어요")
	}

	// 다른 주문의 상태를 우리 주문 결과로 받아들이면 안 된다.
	if order.OrderID != proof.Token {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodePurchaseReplayMismatch,
			"조회한 주문이 요청과 달라요")
	}
	if order.SKU != proof.ProductID {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProductMismatch,
			"구매한 상품이 요청과 달라요")
	}

	// ERROR는 AIT가 상태를 확정하지 못한 것이다.
	// invalid로 굳히지 않고 재시도 가능한 에러로 올린다.
	if order.Status == statusError {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProviderUnavailable,
			"AppsInToss가 주문 상태를 확정하지 못했어요")
	}

	state := mapOrderStatus(order.Status)
	if state == domain.StateInvalid {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodePurchaseInvalid,
			"구매 상태를 확인할 수 없어요")
	}

	purchasedAt := v.parseTimestamp(order.StatusDeterminedAt)

	// 확정된 주문에 시각이 없으면 응답을 신뢰할 수 없다.
	// 원장의 stale 억제가 시각에 기대기 때문이다.
	if (state == domain.StateActive || state == domain.StateRevoked) && purchasedAt.IsZero() {
		return domain.VerifiedPurchase{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"AppsInToss 주문 확정 시각이 올바르지 않아요")
	}

	// PAYMENT_COMPLETED만 클라이언트 완료 처리가 남아 있다.
	// PURCHASED는 이미 지급까지 끝난 상태다.
	completion := domain.CompletionNone
	if state == domain.StateActive && order.Status == statusPaymentCompleted {
		completion = domain.CompletionAppsInTossClient
	}

	return domain.VerifiedPurchase{
		Platform:    domain.PlatformAppsInToss,
		ProductID:   order.SKU,
		CanonicalID: order.OrderID, // 불변식 1. AIT는 orderId다
		// AIT는 별도 주문 번호가 없다. orderId가 둘 다 겸한다.
		ProviderOrderID:   order.OrderID,
		PlatformAccountID: proof.AITAccountHash,
		PurchasedAt:       purchasedAt,
		// 원본은 observedAt에 마켓 시각을 넣었지만 서버 관측 시각을 쓴다.
		// Play·Apple과 같은 기준이어야 원장의 stale 비교가 성립하고,
		// pending처럼 마켓 시각이 없는 상태에서 0값이 새지 않는다.
		ObservedAt: observedAt,
		State:      state,
		Completion: completion,
	}, nil
}

// CompleteGrant는 아무것도 하지 않는다.
//
// AIT는 클라이언트가 completeProductGrant를 부른다.
// 서버는 지급을 커밋하고 응답에 지시만 담는다.
// 이 메서드가 불리는 것 자체가 배선 실수다.
func (v *Verifier) CompleteGrant(_ context.Context, p domain.VerifiedPurchase) error {
	if p.Platform != domain.PlatformAppsInToss {
		return platformerr.New(platformerr.CodePlatformMismatch,
			"AppsInToss 완료 처리에 다른 마켓 구매가 왔어요")
	}
	return platformerr.New(platformerr.CodeCompletionMismatch,
		"AppsInToss 완료 처리는 클라이언트가 해요")
}

// fetchOrderStatus는 주문 상태를 조회한다.
func (v *Verifier) fetchOrderStatus(ctx context.Context, orderID string) (*orderStatusResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]string{"orderId": orderID})
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeProviderConfigInvalid,
			"AppsInToss 요청을 만들지 못했어요")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		v.baseURL+orderStatusPath, bytes.NewReader(body))
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeProviderConfigInvalid,
			"AppsInToss 요청을 만들지 못했어요")
	}
	req.Header.Set("Content-Type", "application/json")
	// 공식 IAP API에서 x-toss-user-key는 선택값이다. 원본 userKey를
	// 저장하지 않는 대신 UUID v7 orderId를 조회하고, 공통 원장이 최초
	// 지급 사용자와 canonical order를 멱등하게 고정한다. 불변식 1·5다.

	resp, err := v.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, platformerr.Wrap(err, platformerr.CodeProviderTimeout,
				"AppsInToss 응답이 늦어요")
		}
		return nil, platformerr.Wrap(err, platformerr.CodeProviderUnavailable,
			"AppsInToss에 연결하지 못했어요")
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeProviderUnavailable,
			"AppsInToss 응답을 읽지 못했어요")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, mapHTTPStatus(resp.StatusCode)
	}

	var parsed orderStatusResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"AppsInToss 응답을 해석하지 못했어요")
	}
	return &parsed, nil
}

// mapOrderStatus는 AIT 주문 상태를 도메인 상태로 바꾼다.
func mapOrderStatus(s string) domain.State {
	switch s {
	case statusPurchased, statusPaymentCompleted:
		return domain.StateActive
	case statusRefunded:
		return domain.StateRevoked
	case statusInProgress:
		return domain.StatePending
	default:
		// FAILED, NOT_FOUND, MINIAPP_MISMATCH가 여기로 온다.
		return domain.StateInvalid
	}
}

// mapHTTPStatus는 AIT HTTP 상태를 플랫폼 에러로 바꾼다.
func mapHTTPStatus(status int) error {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		// mTLS 인증서 문제다. 재시도해도 같다.
		return platformerr.New(platformerr.CodeProviderAuthFailed,
			"AppsInToss 인증에 실패했어요")
	case status == http.StatusNotFound:
		return platformerr.New(platformerr.CodePurchaseNotFound,
			"주문을 찾을 수 없어요")
	case status == http.StatusRequestTimeout,
		status == http.StatusTooEarly,
		status == http.StatusTooManyRequests:
		return platformerr.New(platformerr.CodeProviderUnavailable,
			"AppsInToss 응답이 지연되고 있어요")
	case status >= 400 && status < 500:
		return platformerr.New(platformerr.CodePurchaseInvalid,
			"주문 정보가 올바르지 않아요")
	default:
		return platformerr.Newf(platformerr.CodeProviderUnavailable,
			"AppsInToss 응답이 %d예요", status)
	}
}

// naiveTimestamp는 타임존이 없는 타임스탬프다.
var naiveTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$`)

// parseTimestamp는 AIT 타임스탬프를 시각으로 바꾼다.
//
// 타임존이 빠진 값이 오는데 AIT는 KST 기준이다.
// 그대로 UTC로 읽으면 9시간 앞선 시각이 되어 stale 비교가 뒤집힌다.
//
// 해석 불가하거나 미래로 너무 앞선 값은 0값을 준다.
// 마켓이 준 시각으로 원장의 순서를 뒤집지 못하게 한다.
func (v *Verifier) parseTimestamp(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if naiveTimestamp.MatchString(s) {
		s += "+09:00"
	}

	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	if t.After(v.now().Add(futureSkew)) {
		return time.Time{}
	}
	return t.UTC()
}
