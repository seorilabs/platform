package iap

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// passService는 기간 이용권 유스케이스다. verify.Service가 구현한다. ADR 0036.
type passService interface {
	PassSnapshot(ctx context.Context, appID, puid string) (verify.PassSnapshot, error)
	ClaimPass(ctx context.Context, appID, puid, entitlementID, requestID string) (ledger.PassReceipt, error)
}

// passSnapshot은 이용권 상태를 돌려준다. 남은 날은 구매 원장이 정하므로 결제 세션만 읽는다.
func (h *Handler) passSnapshot(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}
	svc, err := h.serviceFor(r, sess)
	if err != nil {
		return err
	}
	ps, ok := svc.(passService)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "이용권을 지원하지 않아요")
	}
	out, err := ps.PassSnapshot(r.Context(), sess.AppID, sess.PlatformUserID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, out)
	return nil
}

// passClaimRequest는 수령 요청이다. 지급량, 날짜, 사용자를 받지 않는다(불변식 8).
// entitlementId는 자기 이용권 중 어느 것을 받을지 고르는 값이다.
type passClaimRequest struct {
	EntitlementID string `json:"entitlementId"`
	RequestID     string `json:"requestId"`
}

func (h *Handler) passClaim(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}
	svc, err := h.serviceFor(r, sess)
	if err != nil {
		return err
	}
	ps, ok := svc.(passService)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "이용권을 지원하지 않아요")
	}
	var req passClaimRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	out, err := ps.ClaimPass(r.Context(), sess.AppID, sess.PlatformUserID, req.EntitlementID, req.RequestID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, out)
	return nil
}
