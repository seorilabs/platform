package presence

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

type AppRegistry interface {
	GetUsable(ctx context.Context, appID string) (registry.App, error)
}

type TokenIssuer interface {
	Issue(appID, sessionID string) (string, time.Time, error)
}

type Handler struct {
	registry AppRegistry
	issuer   TokenIssuer
	edgeURL  string
}

func NewHandler(reg AppRegistry, issuer TokenIssuer, edgeURL string) *Handler {
	return &Handler{registry: reg, issuer: issuer, edgeURL: strings.TrimRight(edgeURL, "/")}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/presence/token", httpx.Wrap(h.issue))
}

type tokenRequest struct {
	SessionID  string `json:"sessionId"`
	Platform   string `json:"platform"`
	AppVersion string `json:"appVersion,omitempty"`
}

type tokenResponse struct {
	Enabled                  bool   `json:"enabled"`
	Token                    string `json:"token,omitempty"`
	ExpiresAt                string `json:"expiresAt,omitempty"`
	ExpiresIn                int    `json:"expiresIn,omitempty"`
	EdgeURL                  string `json:"edgeUrl,omitempty"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
}

func (h *Handler) issue(w http.ResponseWriter, r *http.Request) error {
	var req tokenRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if !sessionIDPattern.MatchString(req.SessionID) {
		return platformerr.New(platformerr.CodeRequestInvalid, "sessionId 형식이 올바르지 않아요")
	}
	if !validPlatform(req.Platform) {
		return platformerr.New(platformerr.CodePlatformInvalid, "platform이 올바르지 않아요")
	}
	if len(req.AppVersion) > 32 {
		return platformerr.New(platformerr.CodeRequestInvalid, "appVersion이 너무 길어요")
	}
	appID, err := httpx.Header(r, "X-Seori-App", platformerr.CodeRequestInvalid)
	if err != nil {
		return err
	}
	app, err := h.registry.GetUsable(r.Context(), appID)
	if err != nil {
		return err
	}
	base := tokenResponse{HeartbeatIntervalSeconds: int(HeartbeatInterval / time.Second)}
	if !app.FeatureEnabled("presence") || h.issuer == nil || h.edgeURL == "" {
		httpx.WriteOK(w, http.StatusOK, base)
		return nil
	}

	signed, expiresAt, err := h.issuer.Issue(appID, req.SessionID)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeConfigUnavailable, "presence token을 발급하지 못했어요")
	}
	base.Enabled = true
	base.Token = signed
	base.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	base.ExpiresIn = int(DefaultTokenTTL / time.Second)
	base.EdgeURL = h.edgeURL
	httpx.WriteOK(w, http.StatusOK, base)
	return nil
}

func validPlatform(value string) bool {
	switch value {
	case "android", "ios", "web", "ait":
		return true
	default:
		return false
	}
}
