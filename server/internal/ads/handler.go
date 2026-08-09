package ads

import (
	"context"
	"net/http"
	"strings"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

type Sessions interface {
	Authenticate(*http.Request) (identity.Session, error)
}
type SSVVerifier interface {
	Verify(context.Context, string) (SSVResult, error)
}

type Handler struct {
	service  *Service
	sessions Sessions
	verifier SSVVerifier
}

func NewHandler(service *Service, sessions Sessions, verifier SSVVerifier) *Handler {
	return &Handler{service: service, sessions: sessions, verifier: verifier}
}
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ads/policy", httpx.Wrap(h.policy))
	mux.HandleFunc("POST /v1/ads/reward-claims", httpx.Wrap(h.createClaim))
	mux.HandleFunc("GET /v1/ads/reward-claims/{claimId}", httpx.Wrap(h.getClaim))
	mux.HandleFunc("POST /v1/ads/reward-claims/{claimId}/confirm", httpx.Wrap(h.confirmClaim))
	mux.HandleFunc("POST /v1/ads/reward-claims/{claimId}/ack", httpx.Wrap(h.ackClaim))
	mux.HandleFunc("GET /v1/ads/admob/ssv/{appId}", httpx.Wrap(h.admobSSV))
}

func (h *Handler) session(r *http.Request) (identity.Session, error) {
	sess, err := h.sessions.Authenticate(r)
	if err != nil {
		return identity.Session{}, err
	}
	if err := sess.EnsureNotAnonymous(); err != nil {
		return identity.Session{}, err
	}
	return sess, nil
}
func (h *Handler) policy(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.session(r)
	if err != nil {
		return err
	}
	policy, err := h.service.Policy(r.Context(), sess.AppID, sess.PlatformUserID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, policy)
	return nil
}

type createClaimRequest struct {
	RequestID      string `json:"requestId"`
	Placement      string `json:"placement"`
	Provider       string `json:"provider"`
	ClientPlatform string `json:"clientPlatform"`
	Reward         Reward `json:"reward"`
}
type createClaimResponse struct {
	Claim
	AdMobSSV map[string]string `json:"admobSsv,omitempty"`
}

func (h *Handler) createClaim(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.session(r)
	if err != nil {
		return err
	}
	var req createClaimRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	claim, err := h.service.CreateClaim(r.Context(), CreateClaimInput{RequestID: req.RequestID, AppID: sess.AppID, PlatformUserID: sess.PlatformUserID, SupportCode: identity.NewSupportCode(sess.AppID, sess.PlatformUserID), PlacementID: req.Placement, Provider: req.Provider, ClientPlatform: req.ClientPlatform, Reward: req.Reward})
	if err != nil {
		return err
	}
	out := createClaimResponse{Claim: claim}
	if claim.Provider == "admob" {
		out.AdMobSSV = map[string]string{"customData": claim.ClaimID, "userId": sess.PlatformUserID}
	}
	httpx.WriteOK(w, http.StatusCreated, out)
	return nil
}
func (h *Handler) getClaim(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.session(r)
	if err != nil {
		return err
	}
	claim, err := h.service.GetClaim(r.Context(), sess.AppID, sess.PlatformUserID, r.PathValue("claimId"))
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, claim)
	return nil
}

type confirmRequest struct {
	TransactionID string `json:"transactionId"`
}

func (h *Handler) confirmClaim(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.session(r)
	if err != nil {
		return err
	}
	var req confirmRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.TransactionID) == "" || len(req.TransactionID) > 256 {
		return platformerr.New(platformerr.CodeRequestInvalid, "광고 transaction ID가 필요해요")
	}
	claim, err := h.service.ConfirmClient(r.Context(), sess.AppID, sess.PlatformUserID, r.PathValue("claimId"), req.TransactionID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, claim)
	return nil
}
func (h *Handler) ackClaim(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.session(r)
	if err != nil {
		return err
	}
	claim, err := h.service.Acknowledge(r.Context(), sess.AppID, sess.PlatformUserID, r.PathValue("claimId"))
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, claim)
	return nil
}
func (h *Handler) admobSSV(w http.ResponseWriter, r *http.Request) error {
	if h.verifier == nil {
		return platformerr.New(platformerr.CodeRuntimeConfigInvalid, "AdMob SSV 검증기가 준비되지 않았어요")
	}
	result, err := h.verifier.Verify(r.Context(), r.URL.RawQuery)
	if err != nil {
		if platformerr.CodeOf(err) == platformerr.CodeSSVSignatureInvalid {
			_ = h.service.repo.RecordSSVResult(r.Context(), false, h.service.now().UTC())
		}
		return err
	}
	if isVerificationProbe(result) {
		_ = h.service.repo.RecordSSVResult(r.Context(), true, h.service.now().UTC())
		httpx.WriteOK(w, http.StatusOK, map[string]bool{"verified": true})
		return nil
	}
	if _, err := h.service.ConfirmAdMob(r.Context(), r.PathValue("appId"), result); err != nil {
		return err
	}
	_ = h.service.repo.RecordSSVResult(r.Context(), true, h.service.now().UTC())
	httpx.WriteOK(w, http.StatusOK, map[string]bool{"confirmed": true})
	return nil
}
