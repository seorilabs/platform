package admin

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

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
