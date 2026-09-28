package admin

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

type economyTesterManager interface {
	EconomyTester(context.Context, string) (ledger.EconomyTesterEnrollment, error)
	SetEconomyTester(context.Context, string, string, bool) (ledger.EconomyTesterEnrollment, error)
}

func (h *Handler) economyTesterTarget(r *http.Request) (economyTesterManager, string, error) {
	if h.ledger.Environment() != domain.EnvProduction || r.Header.Get("X-Seori-App") != "lizard-tycoon" ||
		r.PathValue("appId") != "lizard-tycoon" || !adminPlatformUserPattern.MatchString(r.PathValue("puid")) {
		return nil, "", platformerr.New(platformerr.CodeRequestInvalid, "시험 계정 등록 대상을 확인해 주세요")
	}
	app, err := h.apps.Get(r.Context(), "lizard-tycoon")
	if err != nil {
		return nil, "", err
	}
	if err := app.EnsureUsable(); err != nil {
		return nil, "", err
	}
	manager, ok := h.ledger.(economyTesterManager)
	if !ok {
		return nil, "", platformerr.New(platformerr.CodeProductNotAllowed, "시험 계정 등록을 지원하지 않아요")
	}
	return manager, r.PathValue("puid"), nil
}

func (h *Handler) collectionEconomyTester(w http.ResponseWriter, r *http.Request) error {
	manager, puid, err := h.economyTesterTarget(r)
	if err != nil {
		return err
	}
	result, err := manager.EconomyTester(r.Context(), puid)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}

func (h *Handler) setCollectionEconomyTester(w http.ResponseWriter, r *http.Request) error {
	manager, puid, err := h.economyTesterTarget(r)
	if err != nil {
		return err
	}
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if err := httpx.DecodeStrict(w, r, &request); err != nil {
		return err
	}
	if request.Enabled == nil {
		return platformerr.New(platformerr.CodeRequestInvalid, "등록 상태를 선택해 주세요")
	}
	actor := actorLogin(ActorFrom(r.Context()))
	if actor == "" {
		return platformerr.New(platformerr.CodeAuthForbidden, "운영자 권한을 확인해 주세요")
	}
	if *request.Enabled {
		user, err := h.users.LookupSupportUser(r.Context(), puid)
		if err != nil {
			return err
		}
		if user.PlatformUserID != puid || user.AppID != "lizard-tycoon" {
			return platformerr.New(platformerr.CodeAuthForbidden, "연결된 이 게임의 계정만 등록할 수 있어요")
		}
		linked, err := h.users.IsAccountLinked(r.Context(), "lizard-tycoon", puid)
		if err != nil {
			return err
		}
		if !linked {
			return platformerr.New(platformerr.CodeAccountLinkRequired, "게임에서 계정을 연결한 뒤 등록해 주세요")
		}
	}
	result, err := manager.SetEconomyTester(r.Context(), puid, actor, *request.Enabled)
	if err != nil {
		return err
	}
	if h.auditor != nil {
		h.auditor.Record(r.Context(), "iap.economy_tester", "lizard-tycoon", puid, "ok", map[string]any{"enabled": result.Enabled, "actor": actor})
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}

type economyReader interface {
	LookupEconomy(context.Context, string) (ledger.EconomyLookup, error)
}

func (h *Handler) collectionEconomy(w http.ResponseWriter, r *http.Request) error {
	if r.Header.Get("X-Seori-App") != r.PathValue("appId") || r.PathValue("appId") != "lizard-tycoon" || !adminPlatformUserPattern.MatchString(r.PathValue("puid")) {
		return platformerr.New(platformerr.CodeRequestInvalid, "수집 지갑 조회 대상을 확인해 주세요")
	}
	reader, ok := h.ledger.(economyReader)
	if !ok {
		return platformerr.New(platformerr.CodeProductNotAllowed, "이 원장은 수집 지갑 조회를 지원하지 않아요")
	}
	result, err := reader.LookupEconomy(r.Context(), r.PathValue("puid"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}
