package verify

import (
	"context"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

const starterAppID = "starter-game"
const starterTicketID = "starter_test_ticket_1"

type walletLedger interface {
	ContentUnitsRemaining(context.Context, string, string, int) (int, error)
}

// WalletSnapshot reuses verified order sources as one-ticket grants (ADR 0037).
// Grant already commits order, source and projection in one transaction.
// No client amount or second wallet write can duplicate grants (invariants 1, 2, 8).
func (s *Service) WalletSnapshot(ctx context.Context, appID, puid string) (map[string]int, error) {
	if appID != starterAppID || puid == "" || s.apps == nil {
		return nil, platformerr.New(platformerr.CodeProductNotAllowed, "이 앱은 티켓 잔액을 지원하지 않아요")
	}
	app, err := s.apps.GetUsable(ctx, appID)
	if err != nil {
		return nil, err
	}
	if !app.FeatureEnabled("iap") || app.IAP.LedgerEnvironment != registry.LedgerSandbox ||
		!app.EntitlementAllowed(starterTicketID) || !s.catalog.HasForApp(appID, starterTicketID) {
		return nil, platformerr.New(platformerr.CodeProductNotAllowed, "테스트 티켓 설정을 확인해 주세요")
	}
	// Missing app wiring must never fall back to the legacy global ledger.
	l, ok := s.appLedgers[appID].(walletLedger)
	if !ok {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid, "테스트 티켓 원장이 준비되지 않았어요")
	}
	quantity, err := l.ContentUnitsRemaining(ctx, puid, starterTicketID, 1)
	if err != nil {
		return nil, err
	}
	return map[string]int{starterTicketID: quantity}, nil
}
