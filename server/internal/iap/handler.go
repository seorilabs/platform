// Package iap는 결제 HTTP 경계다.
//
// 유스케이스는 verify 패키지에 있다. 여기는 요청을 해석하고
// 권한을 확인하고 응답을 만드는 일만 한다.
package iap

import (
	"context"
	"net/http"
	"strings"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// Sessions는 요청에서 세션을 꺼낸다.
//
// 소비자인 Handler가 인터페이스를 정의한다. identity.Handler가 구현한다.
type Sessions interface {
	Authenticate(r *http.Request) (identity.Session, error)
}

type Apps interface {
	GetUsable(ctx context.Context, appID string) (registry.App, error)
}

// Service는 결제 유스케이스다. verify.Service가 구현한다.
type Service interface {
	VerifyPurchase(ctx context.Context, appID, puid string, proof domain.Proof) (verify.Outcome, error)
	ListEntitlements(ctx context.Context, puid string) ([]string, error)
	AccountReferences(puid string) (google, apple string, err error)
}

type appScopedService interface {
	ListEntitlementsForApp(ctx context.Context, appID, puid string) ([]string, error)
	AccountReferencesForApp(ctx context.Context, appID, puid string) (google, apple string, err error)
}

// Handler는 결제 HTTP 핸들러다.
type Handler struct {
	svc      Service
	sessions Sessions
	apps     Apps
}

func NewHandler(svc Service, sessions Sessions) *Handler {
	return &Handler{svc: svc, sessions: sessions}
}

func (h *Handler) WithApps(apps Apps) *Handler {
	h.apps = apps
	return h
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
		// AppsInToss 주문은 Toss Login으로 연 세션에만 지급한다. lizard의
		// Firebase 세션이 같은 appId를 쓰더라도 AIT 주문을 가져갈 수 없다.
		const prefix = "ait:"
		if !strings.HasPrefix(sess.AppUserID, prefix) {
			return platformerr.New(platformerr.CodeAuthForbidden,
				"AppsInToss 구매에는 토스 로그인이 필요해요")
		}
		proof.AITAccountHash = strings.TrimPrefix(sess.AppUserID, prefix)
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
// status는 스펙에서 required다. 빼면 클라이언트가 응답을 통째로 거부하고
// 복원이 매번 실패한다. 그러면 로컬 캐시가 서버 원장으로 교정되지 않아,
// 환불된 유저가 상품을 계속 갖고 있는 것처럼 보인다.
//
// 계정 참조 필드명과 같은 종류의 실수다. 서버는 200을 주는데 앱만
// 실패하므로 서버 로그로는 드러나지 않는다.
type entitlementsResponse struct {
	Status       string   `json:"status"`
	Entitlements []string `json:"entitlements"`
}

func (h *Handler) listEntitlements(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}

	var list []string
	if scoped, ok := h.svc.(appScopedService); ok {
		list, err = scoped.ListEntitlementsForApp(r.Context(), sess.AppID, sess.PlatformUserID)
	} else {
		list, err = h.svc.ListEntitlements(r.Context(), sess.PlatformUserID)
	}
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, entitlementsResponse{
		Status:       "verified",
		Entitlements: list,
	})
	return nil
}

// accountReferencesResponse는 신규 구매 전에 클라이언트가 받아갈 계정 참조다.
//
// 마켓 결제 화면에 넣으면 마켓이 검증 응답에 그대로 돌려준다.
// 그 값을 대조해 다른 사용자가 시작한 구매를 가로채지 못하게 한다.
// 필드 이름은 spec/openapi.yaml이 정본이고 클라이언트가 그 이름을
// 그대로 검사한다. 짧게 줄여 뒀더니 클라이언트가 응답을 통째로
// 거부했다 — 서버는 200을 주는데 앱만 실패하는, 가장 알아채기
// 어려운 형태였다.
type accountReferencesResponse struct {
	// GooglePlay는 obfuscatedExternalAccountId에 넣는다.
	GooglePlay string `json:"googlePlayObfuscatedAccountId"`
	// AppStore는 appAccountToken에 넣는다. UUID 형식이다.
	AppStore string `json:"appStoreAppAccountToken"`
}

func (h *Handler) accountReferences(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}

	var google, apple string
	if scoped, ok := h.svc.(appScopedService); ok {
		google, apple, err = scoped.AccountReferencesForApp(r.Context(), sess.AppID, sess.PlatformUserID)
	} else {
		google, apple, err = h.svc.AccountReferences(sess.PlatformUserID)
	}
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
	if h.apps != nil {
		app, err := h.apps.GetUsable(r.Context(), sess.AppID)
		if err != nil {
			return identity.Session{}, err
		}
		if app.IAP.RequireLinkedAccount && !sess.IsLinkedAccount {
			return identity.Session{}, platformerr.New(platformerr.CodeAccountLinkRequired,
				"계정을 연결한 뒤 구매하거나 복원할 수 있어요")
		}
	}
	return sess, nil
}
