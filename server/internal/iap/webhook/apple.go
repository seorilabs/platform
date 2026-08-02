package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/richzw/appstore"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// App Store Server Notifications V2를 받는다.
//
// 이 엔드포인트에는 별도 인증이 없다. JWS 서명 검증 자체가 인증이다.
// Apple의 인증서 체인으로 서명된 페이로드만 통과하므로 아무나
// 위조 알림을 보낼 수 없다.
//
// 그래도 알림 내용만으로 원장을 바꾸지 않는다. 알림에서 transactionId만
// 꺼내 App Store Server API에 다시 물어본다.

// appleMaxBodyBytes는 알림 본문 상한이다.
//
// Apple 문서상 최대 64KB다. 여유를 두되 무한정 읽지는 않는다.
const appleMaxBodyBytes = 256 << 10

// appleRelevantTypes는 우리가 반응하는 알림이다.
//
// 1단계는 비소비성만 다루므로 구독 관련은 전부 무시한다.
// 무시한다고 실패로 처리하면 Apple이 계속 재전송한다.
var appleRelevantTypes = map[string]bool{
	string(appstore.NotificationTypeV2OneTimeCharge):  true,
	string(appstore.NotificationTypeV2Refund):         true,
	string(appstore.NotificationTypeV2RefundDeclined): true,
	string(appstore.NotificationTypeV2RefundReversed): true,
	string(appstore.NotificationTypeV2Revoke):         true,
}

// AppleParser는 알림 JWS를 검증하고 해석한다.
//
// 소비자인 이 패키지가 인터페이스를 정의한다.
// apple.Client가 감싸는 richzw/appstore가 실제 검증을 한다.
type AppleParser interface {
	ParseNotification(signedPayload string) (*appstore.NotificationPayload, error)
	ParseTransaction(signedTransactionInfo string) (*appstore.JWSTransaction, error)
}

// AppleHandler는 App Store 알림 핸들러다.
type AppleHandler struct {
	parser   AppleParser
	verifier Verifier
	bundleID string
	proc     *processor
}

// AppleConfig는 핸들러 조립 설정이다.
type AppleConfig struct {
	Parser     AppleParser
	Verifier   Verifier
	Events     Events
	Reconciler Reconciler
	Auditor    Auditor
	BundleID   string
}

func NewAppleHandler(cfg AppleConfig) (*AppleHandler, error) {
	if cfg.Parser == nil || cfg.Events == nil || cfg.Reconciler == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"App Store 알림 설정이 올바르지 않아요")
	}
	if cfg.BundleID == "" {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"App Store 번들 ID가 필요해요")
	}

	return &AppleHandler{
		parser:   cfg.Parser,
		verifier: cfg.Verifier,
		bundleID: cfg.BundleID,
		proc: &processor{
			events:     cfg.Events,
			reconciler: cfg.Reconciler,
			auditor:    cfg.Auditor,
			now:        time.Now,
		},
	}, nil
}

func (h *AppleHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/iap/webhooks/apple", h.serve)
}

// appleEnvelope는 Apple이 보내는 본문이다. 필드가 하나뿐이다.
type appleEnvelope struct {
	SignedPayload string `json:"signedPayload"`
}

// serve는 알림을 처리한다.
//
// 응답 형식이 다른 API와 다르다. Apple은 envelope를 보지 않고
// HTTP 상태 코드만 본다. 2xx면 성공, 그 외에는 재전송한다.
func (h *AppleHandler) serve(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, appleMaxBodyBytes))
	if err != nil {
		http.Error(w, "본문을 읽지 못했어요", http.StatusBadRequest)
		return
	}

	var env appleEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.SignedPayload == "" {
		// 형식이 틀렸다. 재전송해도 같으므로 400이다.
		http.Error(w, "알림 형식이 올바르지 않아요", http.StatusBadRequest)
		return
	}

	n, err := h.parse(env.SignedPayload)
	if err != nil {
		writeWebhookError(w, err)
		return
	}

	if err := h.proc.process(r.Context(), "app_store", n, h.verifier); err != nil {
		writeWebhookError(w, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// parse는 알림 JWS를 검증하고 재검증에 필요한 정보를 꺼낸다.
func (h *AppleHandler) parse(signedPayload string) (notification, error) {
	payload, err := h.parser.ParseNotification(signedPayload)
	if err != nil {
		// 서명이 맞지 않는다. 위조이거나 Apple 인증서가 바뀐 것이다.
		return notification{}, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"App Store 알림 서명을 확인하지 못했어요")
	}
	if payload.NotificationUUID == "" {
		return notification{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 알림 식별자가 없어요")
	}

	n := notification{
		EventKey:   payload.NotificationUUID,
		Kind:       payload.NotificationType,
		ObservedAt: appleSignedDate(payload),
	}

	// 다른 앱의 알림이다. 우리 원장과 무관하다.
	if payload.Data.BundleID != "" && payload.Data.BundleID != h.bundleID {
		return notification{}, platformerr.New(platformerr.CodeBundleMismatch,
			"다른 앱의 알림이에요")
	}

	// 관심 없는 알림은 점유만 하고 끝낸다.
	// 에러로 만들면 Apple이 계속 재전송한다.
	if !appleRelevantTypes[payload.NotificationType] {
		return n, nil
	}

	if payload.Data.SignedTransactionInfo == "" {
		return notification{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 알림에 거래 정보가 없어요")
	}

	tx, err := h.parser.ParseTransaction(payload.Data.SignedTransactionInfo)
	if err != nil {
		return notification{}, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"App Store 알림 거래 서명을 확인하지 못했어요")
	}
	if tx.TransactionID == "" || tx.ProductID == "" {
		return notification{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 알림 거래 정보가 올바르지 않아요")
	}

	// 재검증은 transactionId로 한다.
	// 검증기가 응답에서 originalTransactionId를 canonicalId로 삼는다.
	n.Proof = domain.Proof{
		Platform:  domain.PlatformAppStore,
		ProductID: tx.ProductID,
		Token:     tx.TransactionID,
	}
	return n, nil
}

// appleSignedDate는 알림 생성 시각이다.
//
// NotificationPayload는 RegisteredClaims를 품고 있어 iat에 들어온다.
func appleSignedDate(p *appstore.NotificationPayload) time.Time {
	if p.IssuedAt != nil {
		return p.IssuedAt.UTC()
	}
	return time.Time{}
}

// writeWebhookError는 마켓이 이해하는 형태로 실패를 알린다.
//
// 다른 API와 달리 envelope을 쓰지 않는다. 마켓은 상태 코드만 본다.
// 재시도 가능 여부가 여기서 갈린다 — 5xx는 재전송되고 4xx는 버려진다.
func writeWebhookError(w http.ResponseWriter, err error) {
	status := platformerr.StatusOf(err)

	// 재시도해도 소용없는 실패에 5xx를 주면 마켓이 영원히 재전송한다.
	if !platformerr.IsRetryableErr(err) && status >= 500 {
		status = http.StatusBadRequest
	}
	http.Error(w, string(platformerr.CodeOf(err)), status)
}
