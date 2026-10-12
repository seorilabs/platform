package identity

import (
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
)

type createAccountLinkRecoveryChallengeRequest struct {
	Provider          string `json:"provider"`
	ExpectedAppUserID string `json:"expectedAppUserId"`
}

type createAccountLinkRecoveryRequest struct {
	Provider          string `json:"provider"`
	IDToken           string `json:"idToken"`
	Nonce             string `json:"nonce"`
	ExpectedAppUserID string `json:"expectedAppUserId"`
}

func (h *Handler) createAccountLinkRecoveryChallenge(w http.ResponseWriter, r *http.Request) error {
	var req createAccountLinkRecoveryChallengeRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	sess, err := h.Authenticate(r)
	if err != nil {
		return err
	}
	if err := h.svc.VerifyAccountLinkAppCheck(r.Context(), sess.AppID, r.Header.Get("X-Firebase-AppCheck")); err != nil {
		return err
	}
	challenge, err := h.svc.BeginAccountLinkRecovery(r.Context(), sess, req.Provider, req.ExpectedAppUserID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusCreated, accountLinkChallengeResponse(challenge))
	return nil
}

func (h *Handler) createAccountLinkRecovery(w http.ResponseWriter, r *http.Request) error {
	var req createAccountLinkRecoveryRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	sess, err := h.Authenticate(r)
	if err != nil {
		return err
	}
	if err := h.svc.VerifyAccountLinkAppCheck(r.Context(), sess.AppID, r.Header.Get("X-Firebase-AppCheck")); err != nil {
		return err
	}
	result, err := h.svc.RecoverAccountLink(r.Context(), sess, req.Provider, req.IDToken, req.Nonce, req.ExpectedAppUserID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, accountLinkResponse{Session: h.sessionResponse(result.Session), FirebaseCustomToken: result.FirebaseCustomToken, Provider: result.Provider, Restored: result.Restored})
	return nil
}
