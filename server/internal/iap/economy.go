package iap

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

type economyService interface {
	EconomySnapshot(context.Context, string, string) (ledger.EconomySnapshot, error)
	TransactEconomy(context.Context, string, string, ledger.EconomyRequest) (ledger.EconomyReceipt, error)
	TransactFreeEconomy(context.Context, string, string, ledger.EconomyRequest) (ledger.EconomyReceipt, error)
	RequiresLinkedAccount(string, domain.Platform, string) (bool, error)
}

func (h *Handler) economySnapshot(w http.ResponseWriter, r *http.Request) error {
	sess, e := h.sessions.Authenticate(r)
	if e != nil {
		return e
	}
	svc, e := h.serviceFor(r, sess)
	if e != nil {
		return e
	}
	es, ok := svc.(economyService)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "수집 경제를 지원하지 않아요")
	}
	result, e := es.EconomySnapshot(r.Context(), sess.AppID, sess.PlatformUserID)
	if e != nil {
		return e
	}
	result.Linked = sess.IsLinkedAccount
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}
func (h *Handler) economyTransaction(w http.ResponseWriter, r *http.Request) error {
	sess, e := h.requirePayingSession(r)
	if e != nil {
		return e
	}
	svc, e := h.serviceFor(r, sess)
	if e != nil {
		return e
	}
	es, ok := svc.(economyService)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "수집 경제를 지원하지 않아요")
	}
	var req ledger.EconomyRequest
	if e := httpx.DecodeStrict(w, r, &req); e != nil {
		return e
	}
	var result ledger.EconomyReceipt
	if sess.IsLinkedAccount {
		result, e = es.TransactEconomy(r.Context(), sess.AppID, sess.PlatformUserID, req)
	} else {
		result, e = es.TransactFreeEconomy(r.Context(), sess.AppID, sess.PlatformUserID, req)
	}
	if e != nil {
		return e
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}
