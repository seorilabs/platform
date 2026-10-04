package admin

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type InboxIssuer interface {
	IssueInbox(context.Context, ledger.InboxIssue) (ledger.InboxMessage, error)
	CheckAdminMutationRate(context.Context, string) error
}

func RegisterInbox(mux *http.ServeMux, auth *Authenticator, apps Apps, users Users, auditor Auditor, resolve func(registry.App) InboxIssuer) {
	mux.Handle("POST /v1/admin/apps/{appId}/inbox", auth.Middleware(AccessWrite, http.HandlerFunc(httpx.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		appID := r.PathValue("appId")
		if !adminAppIDPattern.MatchString(appID) {
			return platformerr.New(platformerr.CodeRequestInvalid, "앱 식별자가 올바르지 않아요")
		}
		app, err := apps.Get(r.Context(), appID)
		if err != nil {
			return err
		}
		if err = app.EnsureUsable(); err != nil {
			return err
		}
		if !app.FeatureEnabled("inbox") {
			return platformerr.New(platformerr.CodeAuthForbidden, "우편함이 활성화되지 않았어요")
		}
		var req struct {
			RequestID      string               `json:"requestId"`
			PlatformUserID string               `json:"platformUserId"`
			Title          string               `json:"title"`
			Body           string               `json:"body"`
			Rewards        []ledger.InboxReward `json:"rewards"`
			ExpiresAt      int64                `json:"expiresAt"`
			Reason         string               `json:"reason"`
			Confirmation   string               `json:"confirmation"`
		}
		if err = httpx.DecodeStrict(w, r, &req); err != nil {
			return err
		}
		actor := actorLogin(Actor{Email: ActorFrom(r.Context()).Email})
		in := ledger.InboxIssue{RequestID: req.RequestID, PlatformUserID: req.PlatformUserID, Title: req.Title, Body: req.Body, Rewards: req.Rewards, ExpiresAt: req.ExpiresAt, Reason: req.Reason, Actor: actor}
		if err = in.Validate(); err != nil {
			return err
		}
		if req.Confirmation != "ISSUE INBOX "+appID+" "+req.PlatformUserID {
			return platformerr.New(platformerr.CodeRequestInvalid, "우편 대상 확인 문구가 달라요")
		}
		user, err := users.LookupSupportUser(r.Context(), req.PlatformUserID)
		if err != nil {
			return err
		}
		if user.PlatformUserID != req.PlatformUserID || user.AppID != appID {
			return platformerr.New(platformerr.CodeAuthForbidden, "이 앱의 사용자가 아니에요")
		}
		for _, reward := range req.Rewards {
			if !app.EntitlementAllowed(reward.EntitlementID) {
				return platformerr.New(platformerr.CodeProductNotAllowed, "이 앱에 허용되지 않은 보상이에요")
			}
		}
		service := resolve(app)
		if err = service.CheckAdminMutationRate(r.Context(), actor); err != nil {
			return err
		}
		out, err := service.IssueInbox(r.Context(), in)
		if err != nil {
			return err
		}
		if auditor != nil {
			auditor.Record(r.Context(), "inbox.issue", appID, req.PlatformUserID, "ok", map[string]any{"requestId": req.RequestID, "reason": req.Reason, "actor": actor})
		}
		httpx.WriteOK(w, 200, out)
		return nil
	}))))
}
