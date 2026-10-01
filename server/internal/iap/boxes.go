package iap

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// boxService는 상자 유스케이스다. verify.Service가 구현한다. ADR 0029.
type boxService interface {
	BoxSnapshot(ctx context.Context, appID, puid string) (verify.BoxSnapshot, error)
	OpenBox(ctx context.Context, appID, puid, requestID string) (ledger.BoxReceipt, error)
}

// boxSnapshot은 상자 상태와 확률을 돌려준다.
//
// 구매 전 화면에서도 확률을 보여야 하므로(확률형 표시 RR-02) 연결 계정이 아니어도
// 읽을 수 있다. 익명 세션만 막는다. 개봉은 결제 세션(연결 계정 요구 포함)만 한다.
func (h *Handler) boxSnapshot(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.sessions.Authenticate(r)
	if err != nil {
		return err
	}
	if sess.IsAnonymous {
		return platformerr.New(platformerr.CodeAnonymousNotAllowed, "로그인 후에 볼 수 있어요")
	}
	svc, err := h.serviceFor(r, sess)
	if err != nil {
		return err
	}
	bs, ok := svc.(boxService)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "상자를 지원하지 않아요")
	}
	out, err := bs.BoxSnapshot(r.Context(), sess.AppID, sess.PlatformUserID)
	if err != nil {
		return err
	}
	out.Linked = sess.IsLinkedAccount
	httpx.WriteOK(w, http.StatusOK, out)
	return nil
}

// boxOpenRequest는 개봉 요청이다. 수량·친구·entitlement를 받지 않는다(불변식 8).
type boxOpenRequest struct {
	RequestID string `json:"requestId"`
}

func (h *Handler) boxOpen(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}
	svc, err := h.serviceFor(r, sess)
	if err != nil {
		return err
	}
	bs, ok := svc.(boxService)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "상자를 지원하지 않아요")
	}
	var req boxOpenRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	out, err := bs.OpenBox(r.Context(), sess.AppID, sess.PlatformUserID, req.RequestID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, out)
	return nil
}
