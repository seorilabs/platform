package iap

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

type walletService interface {
	WalletSnapshot(context.Context, string, string) (map[string]int, error)
}

func (h *Handler) walletSnapshot(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}
	if r.URL.RawQuery != "" {
		return platformerr.New(platformerr.CodeRequestInvalid, "잔액 대상과 수량은 서버가 결정해요")
	}
	svc, err := h.serviceFor(r, sess)
	if err != nil {
		return err
	}
	wallet, ok := svc.(walletService)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "이 앱은 티켓 잔액을 지원하지 않아요")
	}
	balances, err := wallet.WalletSnapshot(r.Context(), sess.AppID, sess.PlatformUserID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, map[string]any{"balances": balances})
	return nil
}
