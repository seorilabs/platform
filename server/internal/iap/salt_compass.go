package iap

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

type saltService interface {
	SaltSnapshot(context.Context, string, string) (ledger.SaltSnapshot, error)
	TransactSalt(context.Context, string, string, ledger.SaltRequest) (ledger.SaltReceipt, error)
}

func (h *Handler) saltSnapshot(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}
	svc, err := h.serviceFor(r, sess)
	if err != nil {
		return err
	}
	s, ok := svc.(saltService)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "인장 정산을 준비 중이에요")
	}
	result, err := s.SaltSnapshot(r.Context(), sess.AppID, sess.PlatformUserID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}
func (h *Handler) saltTransaction(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.requirePayingSession(r)
	if err != nil {
		return err
	}
	svc, err := h.serviceFor(r, sess)
	if err != nil {
		return err
	}
	s, ok := svc.(saltService)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "인장 정산을 준비 중이에요")
	}
	var req ledger.SaltRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	result, err := s.TransactSalt(r.Context(), sess.AppID, sess.PlatformUserID, req)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}
