package webhook

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// Real-time developer notifications를 Pub/Sub push로 받는다.
//
// Firebase Functions에서는 onMessagePublished 트리거였다. Cloud Run에는
// 그 트리거가 없어 push subscription으로 바꿨고, 그래서 인증을
// 우리가 해야 한다. Pub/Sub이 붙여 보내는 OIDC 토큰을 검증한다.
//
// 검증하지 않으면 아무나 환불 알림을 흉내 내 남의 entitlement를
// 회수할 수 있다. Apple과 달리 페이로드 자체에는 서명이 없다.

// playMaxBodyBytes는 push 본문 상한이다.
const playMaxBodyBytes = 256 << 10

// Play 알림의 환불 종류다.
const (
	refundTypeFull    = 1
	refundTypePartial = 2
)

// TokenValidator는 Pub/Sub OIDC 토큰을 검증한다.
//
// 소비자인 이 패키지가 인터페이스를 정의한다.
// idtoken.Validate를 감싼 구현이 들어온다.
type TokenValidator interface {
	// Validate는 토큰을 검증하고 발급 주체 이메일을 돌려준다.
	Validate(ctx context.Context, token string) (email string, err error)
}

// pubsubPush는 Pub/Sub push 요청 본문이다.
type pubsubPush struct {
	Message struct {
		Data        string `json:"data"`
		MessageID   string `json:"messageId"`
		PublishTime string `json:"publishTime"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

// developerNotification은 Play가 보내는 알림이다.
type developerNotification struct {
	Version         string `json:"version"`
	PackageName     string `json:"packageName"`
	EventTimeMillis string `json:"eventTimeMillis"`

	OneTimeProductNotification *struct {
		NotificationType int    `json:"notificationType"`
		PurchaseToken    string `json:"purchaseToken"`
		SKU              string `json:"sku"`
	} `json:"oneTimeProductNotification"`

	VoidedPurchaseNotification *struct {
		PurchaseToken string `json:"purchaseToken"`
		OrderID       string `json:"orderId"`
		ProductType   int    `json:"productType"`
		RefundType    int    `json:"refundType"`
	} `json:"voidedPurchaseNotification"`

	TestNotification *struct {
		Version string `json:"version"`
	} `json:"testNotification"`
}

// PlayHandler는 Play RTDN 핸들러다.
type PlayHandler struct {
	validator   TokenValidator
	verifier    Verifier
	packageName string
	// allowedEmails가 비어 있지 않으면 그 서비스 계정만 허용한다.
	allowedEmails map[string]bool
	proc          *processor
}

// PlayConfig는 핸들러 조립 설정이다.
type PlayConfig struct {
	Validator   TokenValidator
	Verifier    Verifier
	Events      Events
	Reconciler  Reconciler
	Auditor     Auditor
	PackageName string
	// AllowedEmails는 push subscription의 서비스 계정이다.
	//
	// 비워두면 토큰 유효성만 본다. 그 경우 audience가 맞는 어떤
	// Google 계정이든 알림을 보낼 수 있어 좁혀두는 편이 낫다.
	AllowedEmails []string
}

func NewPlayHandler(cfg PlayConfig) (*PlayHandler, error) {
	if cfg.Validator == nil || cfg.Events == nil || cfg.Reconciler == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"Play 알림 설정이 올바르지 않아요")
	}
	if cfg.PackageName == "" {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"Play 패키지 이름이 필요해요")
	}

	allowed := make(map[string]bool, len(cfg.AllowedEmails))
	for _, e := range cfg.AllowedEmails {
		if e = strings.TrimSpace(e); e != "" {
			allowed[e] = true
		}
	}

	return &PlayHandler{
		validator:     cfg.Validator,
		verifier:      cfg.Verifier,
		packageName:   cfg.PackageName,
		allowedEmails: allowed,
		proc: &processor{
			events:     cfg.Events,
			reconciler: cfg.Reconciler,
			auditor:    cfg.Auditor,
			now:        time.Now,
		},
	}, nil
}

func (h *PlayHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/iap/webhooks/play", h.serve)
}

func (h *PlayHandler) serve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := h.authenticate(ctx, r); err != nil {
		// 인증 실패에 5xx를 주면 Pub/Sub이 재전송한다.
		// 설정 문제라 재전송해도 같으므로 401로 끊는다.
		http.Error(w, string(platformerr.CodeOf(err)), http.StatusUnauthorized)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, playMaxBodyBytes))
	if err != nil {
		http.Error(w, "본문을 읽지 못했어요", http.StatusBadRequest)
		return
	}

	n, err := h.parse(raw)
	if err != nil {
		writeWebhookError(w, err)
		return
	}

	if err := h.proc.process(ctx, "google_play", n, h.verifier); err != nil {
		writeWebhookError(w, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// authenticate는 Pub/Sub이 붙인 OIDC 토큰을 검증한다.
func (h *PlayHandler) authenticate(ctx context.Context, r *http.Request) error {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		return platformerr.New(platformerr.CodeAuthRequired,
			"알림 인증 토큰이 없어요")
	}

	email, err := h.validator.Validate(ctx, strings.TrimSpace(token))
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeAuthInvalid,
			"알림 인증 토큰이 올바르지 않아요")
	}

	if len(h.allowedEmails) > 0 && !h.allowedEmails[email] {
		return platformerr.New(platformerr.CodeAuthInvalid,
			"허용되지 않은 알림 발신자예요")
	}
	return nil
}

// parse는 push 본문에서 재검증에 필요한 정보를 꺼낸다.
func (h *PlayHandler) parse(raw []byte) (notification, error) {
	var push pubsubPush
	if err := json.Unmarshal(raw, &push); err != nil {
		return notification{}, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"알림 형식이 올바르지 않아요")
	}
	if push.Message.MessageID == "" {
		return notification{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"알림 식별자가 없어요")
	}

	// 멱등 키는 Pub/Sub messageId다.
	// 같은 알림을 재전송해도 messageId는 유지된다.
	n := notification{EventKey: push.Message.MessageID}

	// data가 비면 Pub/Sub 연결 확인용 빈 메시지다. 점유만 하고 끝낸다.
	if push.Message.Data == "" {
		n.Kind = "empty"
		return n, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(push.Message.Data)
	if err != nil {
		return notification{}, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"알림 내용을 읽지 못했어요")
	}

	var dn developerNotification
	if err := json.Unmarshal(decoded, &dn); err != nil {
		return notification{}, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"알림 내용을 해석하지 못했어요")
	}

	// 다른 앱의 알림이다. 우리 원장과 무관하다.
	if dn.PackageName != "" && dn.PackageName != h.packageName {
		return notification{}, platformerr.New(platformerr.CodeBundleMismatch,
			"다른 앱의 알림이에요")
	}

	n.ObservedAt = playEventTime(dn.EventTimeMillis)

	switch {
	case dn.TestNotification != nil:
		// Play Console에서 보내는 연결 확인이다. 200을 줘야 설정이 완료된다.
		n.Kind = "test"
		return n, nil

	case dn.VoidedPurchaseNotification != nil:
		v := dn.VoidedPurchaseNotification
		n.Kind = "voided_purchase"

		// 부분 환불은 수량 기반이라 비소비성 entitlement에 대응되지 않는다.
		// 일부만 회수한다는 개념이 없어서 조용히 전부 회수하면 안 된다.
		if v.RefundType == refundTypePartial {
			return notification{}, platformerr.New(platformerr.CodePartialRefundUnsupported,
				"부분 환불은 처리할 수 없어요")
		}
		if v.PurchaseToken == "" {
			return notification{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
				"알림에 구매 토큰이 없어요")
		}

		// 환불 알림에는 sku가 없다. productId 없이는 재검증할 수 없어
		// 원장에서 상품을 찾아 채운다.
		n.Proof = domain.Proof{
			Platform: domain.PlatformGooglePlay,
			Token:    v.PurchaseToken,
		}
		return n, nil

	case dn.OneTimeProductNotification != nil:
		o := dn.OneTimeProductNotification
		n.Kind = "one_time_product"

		if o.PurchaseToken == "" || o.SKU == "" {
			return notification{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
				"알림에 구매 정보가 없어요")
		}
		n.Proof = domain.Proof{
			Platform:  domain.PlatformGooglePlay,
			ProductID: o.SKU,
			Token:     o.PurchaseToken,
		}
		return n, nil

	default:
		// 구독 알림 등 우리가 다루지 않는 종류다.
		// 에러로 만들면 Pub/Sub이 계속 재전송한다.
		n.Kind = "ignored"
		return n, nil
	}
}

// playEventTime은 밀리초 문자열을 시각으로 바꾼다.
func playEventTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
