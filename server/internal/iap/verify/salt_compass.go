package verify

import (
	"context"

	"github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

type saltLedger interface {
	SaltSnapshot(context.Context, string) (ledger.SaltSnapshot, error)
	TransactSalt(context.Context, string, ledger.SaltRequest) (ledger.SaltReceipt, error)
}
type SaltRewardClaims interface {
	GetClaim(context.Context, string) (ads.Claim, error)
}

func (s *Service) saltLedgerFor(ctx context.Context, appID, puid string) (saltLedger, error) {
	if appID != "salt-compass" || puid == "" || s.apps == nil {
		return nil, platformerr.New(platformerr.CodeProductNotAllowed, "항해 인장 정산을 지원하지 않는 앱이에요")
	}
	app, err := s.apps.GetUsable(ctx, appID)
	if err != nil {
		return nil, err
	}
	if !app.FeatureEnabled("iap") {
		return nil, platformerr.New(platformerr.CodeProductNotAllowed, "결제 연결을 준비 중이에요")
	}
	// 앱별 원장이 누락됐을 때 legacy 공용 원장으로 fallback하지 않는다.
	l, ok := s.appLedgers[appID].(saltLedger)
	if !ok {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid, "인장 정산 연결을 확인해 주세요")
	}
	return l, nil
}
func (s *Service) SaltSnapshot(ctx context.Context, appID, puid string) (ledger.SaltSnapshot, error) {
	l, err := s.saltLedgerFor(ctx, appID, puid)
	if err != nil {
		return ledger.SaltSnapshot{}, err
	}
	return l.SaltSnapshot(ctx, puid)
}
func (s *Service) TransactSalt(ctx context.Context, appID, puid string, r ledger.SaltRequest) (ledger.SaltReceipt, error) {
	l, err := s.saltLedgerFor(ctx, appID, puid)
	if err != nil {
		return ledger.SaltReceipt{}, err
	}
	if r.Action == "ad_reward" {
		if s.saltRewardClaims == nil {
			return ledger.SaltReceipt{}, platformerr.New(platformerr.CodeClaimStateInvalid, "광고 보상을 확인 중이에요")
		}
		claim, err := s.saltRewardClaims.GetClaim(ctx, r.ClaimID)
		if err != nil {
			return ledger.SaltReceipt{}, err
		}
		if claim.AppID != appID || claim.PlatformUserID != puid || !ads.SettledClaim(claim) ||
			claim.PlacementID != "rewarded_offline_double" || claim.Reward.Key != "seal" || claim.Reward.Amount != 3 {
			return ledger.SaltReceipt{}, platformerr.New(platformerr.CodeClaimStateInvalid, "이 계정의 확정 인장 보상이 아니에요")
		}
		r = r.WithVerifiedReward()
	}
	return l.TransactSalt(ctx, puid, r)
}
