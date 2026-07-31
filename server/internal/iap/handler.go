// Package iap는 결제 HTTP 경계다.
//
// 유스케이스는 verify 패키지에 있다. 여기는 요청을 해석하고
// 권한을 확인하고 응답을 만드는 일만 한다.
package iap

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// Sessions는 요청에서 세션을 꺼낸다.
//
// 소비자인 Handler가 인터페이스를 정의한다. identity.Handler가 구현한다.
type Sessions interface {
	Authenticate(r *http.Request) (identity.Session, error)
}

// Service는 결제 유스케이스다. verify.Service가 구현한다.
type Service interface {
	VerifyPurchase(ctx context.Context, appID, puid string, proof domain.Proof) (verify.Outcome, error)
	ListEntitlements(ctx context.Context, puid string) ([]string, error)
	AccountReferences(puid string) (google, apple string, err error)
}

// Handler는 결제 HTTP 핸들러다.
type Handler struct {
	svc      Service
	sessions Sessions
}

func NewHandler(svc Service, sessions Sessions) *Handler {
	return &Handler{svc: svc, sessions: sessions}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/iap/verify", httpx.Wrap(h.verifyPurchase))
	mux.HandleFunc("GET /v1/iap/entitlements", httpx.Wrap(h.listEntitlements))
	mux.HandleFunc("POST /v1/iap/account-references", httpx.Wrap(h.accountReferences))
}

// verifyRequest는 구매 검증 요청이다.
//
// 권한을 결정하는 값 — platformUserId, entitlementId — 은 여기 없다.
// DecodeStrict가 미지 필드를 거부하므로 주입 시도가 400으로 막힌다.
// 불변식 8이다.
type verifyRequest struct {
	Platform  string `json:"platform"`
	ProductID string `json:"productId"`
	// Token은 마켓별 증명값이다.
	// Play는 purchaseToken, App Store는 transactionId, AIT는 orderId다.
	Token string `json:"token"`
}

func (h *Handler) verifyPurchase(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}

	var req verifyRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}

	platform := domain.Platform(req.Platform)
	if !platform.IsMarket() {
		return platformerr.New(platformerr.CodePlatformMismatch,
			"지원하지 않는 결제 수단이에요")
	}
	if req.ProductID == "" || req.Token == "" {
		return platformerr.New(platformerr.CodeProofInvalid,
			"구매 정보가 비어 있어요")
	}

	proof := domain.Proof{
		Platform:  platform,
		ProductID: req.ProductID,
		Token:     req.Token,
	}

	// AIT 사용자 키는 body가 아니라 검증된 세션에서만 온다.
	// 이것이 AIT의 계정 바인딩을 대신하므로 클라이언트가 넣게 두면
	// 다른 사람의 주문을 자기 것으로 만들 수 있다.
	if platform == domain.PlatformAppsInToss {
		proof.AITUserKey = sess.AppUserID
	}

	out, err := h.svc.VerifyPurchase(r.Context(), sess.AppID, sess.PlatformUserID, proof)
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, out)
	return nil
}

// entitlementsResponse는 활성 entitlement 목록이다.
//
// 마켓 SDK 없이도 환불 반영을 확인할 수 있는 경로다.
type entitlementsResponse struct {
	Entitlements []string `json:"entitlements"`
}

func (h *Handler) listEntitlements(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}

	list, err := h.svc.ListEntitlements(r.Context(), sess.PlatformUserID)
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, entitlementsResponse{Entitlements: list})
	return nil
}

// accountReferencesResponse는 신규 구매 전에 클라이언트가 받아갈 계정 참조다.
//
// 마켓 결제 화면에 넣으면 마켓이 검증 응답에 그대로 돌려준다.
// 그 값을 대조해 다른 사용자가 시작한 구매를 가로채지 못하게 한다.
type accountReferencesResponse struct {
	// GooglePlay는 obfuscatedExternalAccountId에 넣는다.
	GooglePlay string `json:"googlePlay"`
	// AppStore는 appAccountToken에 넣는다. UUID 형식이다.
	AppStore string `json:"appStore"`
}

func (h *Handler) accountReferences(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}

	google, apple, err := h.svc.AccountReferences(sess.PlatformUserID)
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, accountReferencesResponse{
		GooglePlay: google,
		AppStore:   apple,
	})
	return nil
}

// requirePayingSession은 결제 가능한 신원인지 확인한다.
//
// 익명 신원은 결제할 수 없다. getAnonymousKey 해시는 bearer 자격증명이
// 아니라 타인 사칭이 가능하기 때문이다. 남의 entitlement를 받아갈 수 있다.
//
// RemoteConfig 조회와 이벤트 로그는 익명도 허용한다. 결제만 막는다.
func (h *Handler) requirePayingSession(r *http.Request) (identity.Session, error) {
	sess, err := h.sessions.Authenticate(r)
	if err != nil {
		return identity.Session{}, err
	}
	if sess.IsAnonymous {
		return identity.Session{}, platformerr.New(platformerr.CodeAnonymousNotAllowed,
			"로그인 후에 구매할 수 있어요")
	}
	return sess, nil
}
