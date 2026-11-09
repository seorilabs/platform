package iap

import (
	"context"
	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"net/http"
)

type defenseService interface {
	DefenseSnapshot(context.Context, string, string) (ledger.DefenseSnapshot, error)
	ActDefense(context.Context, string, string, ledger.DefenseRequest) (ledger.DefenseReceipt, error)
}

func (h *Handler) defense(w http.ResponseWriter, r *http.Request) error {
	sess, e := h.requirePayingSession(r)
	if e != nil {
		return e
	}
	if !sess.IsLinkedAccount {
		return platformerr.New(platformerr.CodeAccountLinkRequired, "계정을 연결한 뒤 사용할 수 있어요")
	}
	svc, e := h.serviceFor(r, sess)
	if e != nil {
		return e
	}
	s, ok := svc.(defenseService)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "방어 자산을 준비 중이에요")
	}
	if r.Method == http.MethodGet {
		out, e := s.DefenseSnapshot(r.Context(), sess.AppID, sess.PlatformUserID)
		if e != nil {
			return e
		}
		httpx.WriteOK(w, 200, out)
		return nil
	}
	var req ledger.DefenseRequest
	if e = httpx.DecodeStrict(w, r, &req); e != nil {
		return e
	}
	out, e := s.ActDefense(r.Context(), sess.AppID, sess.PlatformUserID, req)
	if e != nil {
		return e
	}
	httpx.WriteOK(w, 200, out)
	return nil
}
