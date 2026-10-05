// Package inbox is the authenticated transport for the existing grant ledger.
package inbox

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type Sessions interface {
	Authenticate(*http.Request) (identity.Session, error)
}
type Apps interface {
	GetUsable(context.Context, string) (registry.App, error)
}
type Mailbox interface {
	ListInbox(context.Context, string, string) (ledger.InboxPage, error)
	ReadInbox(context.Context, string, string) (ledger.InboxMessage, error)
	ClaimInbox(context.Context, string, string) (ledger.InboxMessage, error)
}
type Handler struct {
	sessions Sessions
	apps     Apps
	forApp   func(registry.App) Mailbox
}

func NewHandler(s Sessions, a Apps, resolve func(registry.App) Mailbox) *Handler {
	return &Handler{s, a, resolve}
}
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/inbox", httpx.Wrap(h.list))
	mux.HandleFunc("POST /v1/inbox/{id}/read", httpx.Wrap(h.read))
	mux.HandleFunc("POST /v1/inbox/{id}/claim", httpx.Wrap(h.claim))
	mux.HandleFunc("POST /v1/inbox/claim-batch", httpx.Wrap(h.batch))
}
func (h *Handler) scope(r *http.Request) (Mailbox, string, error) {
	s, e := h.sessions.Authenticate(r)
	if e != nil {
		return nil, "", e
	}
	a, e := h.apps.GetUsable(r.Context(), s.AppID)
	if e != nil {
		return nil, "", e
	}
	if !a.FeatureEnabled("inbox") {
		return nil, "", platformerr.New(platformerr.CodeAuthForbidden, "이 게임은 서버 우편함을 사용하지 않아요")
	}
	return h.forApp(a), s.PlatformUserID, nil
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	service, puid, e := h.scope(r)
	if e != nil {
		return e
	}
	out, e := service.ListInbox(r.Context(), puid, r.URL.Query().Get("cursor"))
	if e != nil {
		return e
	}
	httpx.WriteOK(w, 200, out)
	return nil
}
func (h *Handler) read(w http.ResponseWriter, r *http.Request) error  { return h.mutate(w, r, false) }
func (h *Handler) claim(w http.ResponseWriter, r *http.Request) error { return h.mutate(w, r, true) }
func (h *Handler) mutate(w http.ResponseWriter, r *http.Request, claim bool) error {
	service, puid, e := h.scope(r)
	if e != nil {
		return e
	}
	var req struct{}
	if e = httpx.DecodeStrict(w, r, &req); e != nil {
		return e
	}
	var out ledger.InboxMessage
	if claim {
		out, e = service.ClaimInbox(r.Context(), puid, r.PathValue("id"))
	} else {
		out, e = service.ReadInbox(r.Context(), puid, r.PathValue("id"))
	}
	if e != nil {
		return e
	}
	httpx.WriteOK(w, 200, out)
	return nil
}

type BatchItem struct {
	ID        string               `json:"id"`
	OK        bool                 `json:"ok"`
	Message   *ledger.InboxMessage `json:"message,omitempty"`
	ErrorCode platformerr.Code     `json:"errorCode,omitempty"`
}

func (h *Handler) batch(w http.ResponseWriter, r *http.Request) error {
	service, puid, e := h.scope(r)
	if e != nil {
		return e
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if e = httpx.DecodeStrict(w, r, &req); e != nil {
		return e
	}
	if len(req.IDs) == 0 || len(req.IDs) > 50 {
		return platformerr.New(platformerr.CodeRequestInvalid, "우편은 1~50개씩 수령해 주세요")
	}
	seen := map[string]bool{}
	for _, id := range req.IDs {
		if seen[id] {
			return platformerr.New(platformerr.CodeRequestInvalid, "우편 식별자가 중복됐어요")
		}
		seen[id] = true
	}
	results := make([]BatchItem, 0, len(req.IDs))
	for _, id := range req.IDs {
		out, e := service.ClaimInbox(r.Context(), puid, id)
		item := BatchItem{ID: id, OK: e == nil}
		if e != nil {
			item.ErrorCode = platformerr.CodeOf(e)
		} else {
			item.Message = &out
		}
		results = append(results, item)
	}
	httpx.WriteOK(w, 200, map[string]any{"results": results})
	return nil
}
