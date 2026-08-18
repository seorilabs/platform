package content

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

const appCheckHeader = "X-Firebase-AppCheck"

type Sessions interface {
	Authenticate(*http.Request) (identity.Session, error)
}

type AppChecks interface {
	VerifyAppCheck(context.Context, string, string) error
}

type Handler struct {
	service   *Service
	sessions  Sessions
	appChecks AppChecks
}

func NewHandler(service *Service, sessions Sessions, appChecks AppChecks) (*Handler, error) {
	if service == nil || sessions == nil || appChecks == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"콘텐츠 HTTP 핸들러 설정이 올바르지 않아요")
	}
	return &Handler{service: service, sessions: sessions, appChecks: appChecks}, nil
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/content/version", httpx.Wrap(h.version))
	mux.HandleFunc("POST /v1/content/readings:resolve", httpx.Wrap(h.resolve))
	mux.HandleFunc("GET /v1/content/terms/{termId}", httpx.Wrap(h.term))
}

func (h *Handler) authenticated(r *http.Request) (identity.Session, error) {
	sess, err := h.sessions.Authenticate(r)
	if err != nil {
		return identity.Session{}, err
	}
	if err := sess.EnsureNotAnonymous(); err != nil {
		return identity.Session{}, err
	}
	// 콘텐츠 앱은 registry 검증에서 require_app_check=true가 강제된다.
	// 이 호출을 각 경로에서 생략하지 않아 세션과 attestation을 함께 요구한다.
	if err := h.appChecks.VerifyAppCheck(
		r.Context(), sess.AppID, r.Header.Get(appCheckHeader),
	); err != nil {
		return identity.Session{}, err
	}
	return sess, nil
}

func (h *Handler) version(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.authenticated(r)
	if err != nil {
		return err
	}
	result, err := h.service.Version(r.Context(), sess.AppID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}

func (h *Handler) resolve(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.authenticated(r)
	if err != nil {
		return err
	}
	var req ResolveRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	result, err := h.service.Resolve(
		r.Context(), sess.AppID, sess.PlatformUserID, req,
	)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}

func (h *Handler) term(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.authenticated(r)
	if err != nil {
		return err
	}
	result, err := h.service.Term(
		r.Context(), sess.AppID, sess.PlatformUserID, r.PathValue("termId"),
	)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}
