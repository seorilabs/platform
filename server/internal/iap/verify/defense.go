package verify

import (
	"context"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

type defenseLedger interface {
	DefenseSnapshot(context.Context, string) (ledger.DefenseSnapshot, error)
	ActDefense(context.Context, string, ledger.DefenseRequest) (ledger.DefenseReceipt, error)
}

func (s *Service) defenseLedgerFor(ctx context.Context, appID string) (defenseLedger, error) {
	if appID != "mechanical-frontier" || s.apps == nil {
		return nil, platformerr.New(platformerr.CodeProductNotAllowed, "방어 자산을 지원하지 않는 앱이에요")
	}
	app, e := s.apps.GetUsable(ctx, appID)
	if e != nil {
		return nil, e
	}
	if !app.FeatureEnabled("iap") {
		return nil, platformerr.New(platformerr.CodeProductNotAllowed, "결제 연결을 준비 중이에요")
	}
	l, ok := s.appLedgers[appID].(defenseLedger)
	if !ok {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid, "방어 원장 연결을 확인해 주세요")
	}
	return l, nil
}
func (s *Service) DefenseSnapshot(ctx context.Context, appID, puid string) (ledger.DefenseSnapshot, error) {
	l, e := s.defenseLedgerFor(ctx, appID)
	if e != nil {
		return ledger.DefenseSnapshot{}, e
	}
	return l.DefenseSnapshot(ctx, puid)
}
func (s *Service) ActDefense(ctx context.Context, appID, puid string, r ledger.DefenseRequest) (ledger.DefenseReceipt, error) {
	l, e := s.defenseLedgerFor(ctx, appID)
	if e != nil {
		return ledger.DefenseReceipt{}, e
	}
	return l.ActDefense(ctx, puid, r)
}
