package verify

import (
	"context"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

type economyLedger interface {
	EconomySnapshot(context.Context, string) (ledger.EconomySnapshot, error)
	TransactEconomy(context.Context, string, ledger.EconomyRequest) (ledger.EconomyReceipt, error)
	TransactFreeEconomy(context.Context, string, ledger.EconomyRequest) (ledger.EconomyReceipt, error)
}

func (s *Service) RequiresLinkedAccount(appID string, platform domain.Platform, productID string) (bool, error) {
	p, e := s.catalog.ProductForApp(appID, platform, productID)
	if e != nil {
		return false, e
	}
	if appID != "lizard-tycoon" {
		return false, nil
	}
	c, e := ledger.LizardEconomyCatalog()
	if e != nil {
		return false, e
	}
	pack, ok := c.Packs[p.EntitlementID]
	if ok {
		expected := domain.ProductConsumable
		if pack.Once {
			expected = domain.ProductNonConsumable
		}
		if p.Type != expected {
			return true, platformerr.New(platformerr.CodeRuntimeConfigInvalid, "신규 재화 상품의 마켓 유형을 확인해 주세요")
		}
	}
	if ok && platform == domain.PlatformAppsInToss {
		return true, platformerr.New(platformerr.CodeProductNotAllowed, "이 마켓에서는 신규 재화 상품을 판매하지 않아요")
	}
	return ok, nil
}
func (s *Service) EconomySnapshot(ctx context.Context, appID, puid string) (ledger.EconomySnapshot, error) {
	if s.apps != nil {
		app, e := s.apps.GetUsable(ctx, appID)
		if e != nil {
			return ledger.EconomySnapshot{}, e
		}
		if !app.FeatureEnabled("iap") {
			return ledger.EconomySnapshot{}, platformerr.New(platformerr.CodeProductNotAllowed, "결제를 지원하지 않는 앱이에요")
		}
	}
	l, ok := s.ledgerFor(appID).(economyLedger)
	if !ok || appID != "lizard-tycoon" {
		return ledger.EconomySnapshot{}, platformerr.New(platformerr.CodeProductNotAllowed, "수집 경제를 지원하지 않는 앱이에요")
	}
	return l.EconomySnapshot(ctx, puid)
}
func (s *Service) TransactEconomy(ctx context.Context, appID, puid string, req ledger.EconomyRequest) (ledger.EconomyReceipt, error) {
	if _, e := s.EconomySnapshot(ctx, appID, puid); e != nil {
		return ledger.EconomyReceipt{}, e
	}
	l := s.ledgerFor(appID).(economyLedger)
	return l.TransactEconomy(ctx, puid, req)
}

func (s *Service) TransactFreeEconomy(ctx context.Context, appID, puid string, req ledger.EconomyRequest) (ledger.EconomyReceipt, error) {
	if _, e := s.EconomySnapshot(ctx, appID, puid); e != nil {
		return ledger.EconomyReceipt{}, e
	}
	return s.ledgerFor(appID).(economyLedger).TransactFreeEconomy(ctx, puid, req)
}
