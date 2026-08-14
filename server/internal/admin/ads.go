package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	platformads "github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type AdsService interface {
	Health(context.Context) (platformads.Health, error)
	AppHealth(context.Context, string) (platformads.AppHealth, error)
	AppConfig(context.Context, string) (registry.App, error)
	LookupUserAds(context.Context, string) (platformads.UserAds, error)
	ListClaims(context.Context, platformads.ClaimFilter) ([]platformads.Claim, error)
	GrantSuppression(context.Context, platformads.SuppressionRecord) (platformads.SuppressionResult, error)
	RevokeSuppression(context.Context, platformads.SuppressionRecord) (platformads.SuppressionResult, error)
}

type adsAdminHandler struct{ service AdsService }

func RegisterAds(mux *http.ServeMux, auth *Authenticator, service AdsService) error {
	if mux == nil || auth == nil || service == nil {
		return platformerr.New(platformerr.CodeRuntimeConfigInvalid, "Ads Admin API 설정이 올바르지 않아요")
	}
	h := &adsAdminHandler{service: service}
	reads := map[string]httpx.Handler{"GET /v1/admin/ads/health": h.health, "GET /v1/admin/apps/{appId}/ads/health": h.appHealth, "GET /v1/admin/apps/{appId}/ads/config": h.config, "GET /v1/admin/users/{puid}/ads": h.user, "GET /v1/admin/ads/reward-claims": h.claims}
	writes := map[string]httpx.Handler{"POST /v1/admin/ads/suppressions/grant": h.grant, "POST /v1/admin/ads/suppressions/revoke": h.revoke}
	for pattern, handler := range reads {
		mux.Handle(pattern, auth.Middleware(AccessRead, http.HandlerFunc(httpx.Wrap(handler))))
	}
	for pattern, handler := range writes {
		mux.Handle(pattern, auth.Middleware(AccessWrite, http.HandlerFunc(httpx.Wrap(handler))))
	}
	return nil
}

func (h *adsAdminHandler) appHealth(w http.ResponseWriter, r *http.Request) error {
	result, err := h.service.AppHealth(r.Context(), r.PathValue("appId"))
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}

func (h *adsAdminHandler) health(w http.ResponseWriter, r *http.Request) error {
	result, err := h.service.Health(r.Context())
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}

type adsProviderView struct {
	AndroidAdUnitSuffix string `json:"androidAdUnitSuffix,omitempty"`
	IOSAdUnitSuffix     string `json:"iosAdUnitSuffix,omitempty"`
	AdGroupSuffix       string `json:"adGroupSuffix,omitempty"`
	RewardItem          string `json:"rewardItem,omitempty"`
	RewardAmount        int    `json:"rewardAmount,omitempty"`
}

func (h *adsAdminHandler) config(w http.ResponseWriter, r *http.Request) error {
	app, err := h.service.AppConfig(r.Context(), r.PathValue("appId"))
	if err != nil {
		return err
	}
	placements := make([]map[string]any, 0, len(app.Ads.Placements))
	for _, p := range app.Ads.Placements {
		providers := map[string]adsProviderView{}
		for name, cfg := range p.Providers {
			providers[name] = adsProviderView{AndroidAdUnitSuffix: suffix(cfg.AndroidAdUnitID), IOSAdUnitSuffix: suffix(cfg.IOSAdUnitID), AdGroupSuffix: suffix(cfg.AdGroupID), RewardItem: cfg.RewardItem, RewardAmount: cfg.RewardAmount}
		}
		placements = append(placements, map[string]any{"id": p.ID, "format": p.Format, "providers": providers, "reward": p.Reward, "dailyLimit": p.DailyLimit, "cooldownSeconds": p.CooldownSeconds})
	}
	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"appId": app.AppID, "providers": app.Ads.Providers, "placements": placements,
		"registrySyncedAt": app.RegistrySyncedAt,
	})
	return nil
}
func suffix(value string) string {
	if value == "" {
		return ""
	}
	if i := strings.LastIndexAny(value, "./"); i >= 0 {
		return value[i+1:]
	}
	if len(value) > 10 {
		return value[len(value)-10:]
	}
	return value
}
func (h *adsAdminHandler) user(w http.ResponseWriter, r *http.Request) error {
	result, err := h.service.LookupUserAds(r.Context(), r.PathValue("puid"))
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}
func (h *adsAdminHandler) claims(w http.ResponseWriter, r *http.Request) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := h.service.ListClaims(r.Context(), platformads.ClaimFilter{AppID: r.URL.Query().Get("appId"), Provider: r.URL.Query().Get("provider"), State: r.URL.Query().Get("state"), Assurance: r.URL.Query().Get("assurance"), Placement: r.URL.Query().Get("placement"), Reference: r.URL.Query().Get("reference"), Limit: limit})
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, map[string]any{"claims": result})
	return nil
}

type suppressionRequest struct {
	RequestID      string `json:"requestId"`
	AppID          string `json:"appId"`
	PlatformUserID string `json:"platformUserId"`
	GrantRequestID string `json:"grantRequestId,omitempty"`
	Reason         string `json:"reason"`
	Confirmation   string `json:"confirmation"`
}

func (h *adsAdminHandler) grant(w http.ResponseWriter, r *http.Request) error {
	return h.mutate(w, r, false)
}
func (h *adsAdminHandler) revoke(w http.ResponseWriter, r *http.Request) error {
	return h.mutate(w, r, true)
}
func (h *adsAdminHandler) mutate(w http.ResponseWriter, r *http.Request, revoke bool) error {
	var req suppressionRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	want := fmt.Sprintf("DISABLE ADS %s %s", req.AppID, req.PlatformUserID)
	if revoke {
		want = fmt.Sprintf("ENABLE ADS %s %s %s", req.AppID, req.PlatformUserID, req.GrantRequestID)
	}
	if req.Confirmation != want {
		return platformerr.New(platformerr.CodeRequestInvalid, "typed confirmation이 요청 내용과 맞지 않아요")
	}
	record := platformads.SuppressionRecord{RequestID: req.RequestID, GrantRequestID: req.GrantRequestID, AppID: req.AppID, PlatformUserID: req.PlatformUserID, ActorLogin: actorLogin(ActorFrom(r.Context())), Reason: req.Reason}
	var result platformads.SuppressionResult
	var err error
	if revoke {
		result, err = h.service.RevokeSuppression(r.Context(), record)
	} else {
		result, err = h.service.GrantSuppression(r.Context(), record)
	}
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}
