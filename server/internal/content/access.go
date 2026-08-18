package content

import (
	"context"
	"time"

	platformads "github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type Unlocks interface {
	GetUnlock(context.Context, string, string, string, string) (UnlockGrant, error)
	BindReward(context.Context, string, string, string, string, string) error
	RecordTicket(context.Context, string, string, string, string, string) error
}

type UnlockGrant struct {
	Exists    bool
	Source    string
	Reference string
}

type RewardClaims interface {
	GetClaim(context.Context, string) (platformads.Claim, error)
	AcknowledgeClaim(context.Context, string, string, string, time.Time) (platformads.Claim, error)
}

type Entitlements interface {
	Active(context.Context, registry.App, string, string) (bool, error)
	SourceActive(context.Context, registry.App, string, string, string) (bool, error)
	Consume(context.Context, registry.App, string, string, int, string) (string, error)
}

type AccessService struct {
	unlocks      Unlocks
	claims       RewardClaims
	entitlements Entitlements
	now          func() time.Time
}

func NewAccessService(unlocks Unlocks, claims RewardClaims, entitlements Entitlements) *AccessService {
	return &AccessService{unlocks: unlocks, claims: claims, entitlements: entitlements, now: time.Now}
}

func (s *AccessService) Authorized(
	ctx context.Context,
	app registry.App,
	puid, readingKey, deepKey string,
	year int,
) (bool, error) {
	if s.unlocks != nil {
		grant, err := s.unlocks.GetUnlock(ctx, app.AppID, puid, readingKey, deepKey)
		if err != nil {
			return false, err
		}
		if grant.Exists {
			if grant.Source == "reward_claim" {
				claim, err := s.verifiedRewardClaim(ctx, app, puid, grant.Reference)
				if err != nil {
					return false, err
				}
				if claim.State != platformads.StateDelivered {
					if _, err := s.claims.AcknowledgeClaim(
						ctx, claim.ClaimID, app.AppID, puid, s.now().UTC(),
					); err != nil {
						return false, platformerr.Wrap(err, platformerr.CodeContentUnavailable,
							"광고 보상 적용 완료를 기록하지 못했어요")
					}
				}
				return true, nil
			}
			if grant.Source == "ticket" {
				if s.entitlements == nil || app.Content.TicketEntitlementID == "" || grant.Reference == "" {
					return false, platformerr.New(platformerr.CodeLedgerStateInvalid,
						"열람권 잠금 해제 근거가 올바르지 않아요")
				}
				active, err := s.entitlements.SourceActive(
					ctx, app, puid, app.Content.TicketEntitlementID, grant.Reference,
				)
				if err != nil {
					return false, err
				}
				if active {
					return true, nil
				}
			}
		}
	}
	if s.entitlements == nil {
		return false, nil
	}
	entitlementID := app.Content.SeasonEntitlements[yearString(year)]
	if entitlementID == "" {
		return false, nil
	}
	return s.entitlements.Active(ctx, app, puid, entitlementID)
}

func (s *AccessService) Unlock(
	ctx context.Context,
	app registry.App,
	puid, readingKey, deepKey string,
	req UnlockRequest,
) error {
	switch req.Kind {
	case "reward_claim":
		claim, err := s.verifiedRewardClaim(ctx, app, puid, req.ClaimID)
		if err != nil {
			return err
		}
		if err := s.unlocks.BindReward(ctx, app.AppID, puid, readingKey, deepKey, claim.ClaimID); err != nil {
			return err
		}
		if _, err := s.claims.AcknowledgeClaim(
			ctx, claim.ClaimID, app.AppID, puid, s.now().UTC(),
		); err != nil {
			return platformerr.Wrap(err, platformerr.CodeContentUnavailable,
				"광고 보상 적용 완료를 기록하지 못했어요")
		}
		return nil

	case "ticket":
		if app.Content.TicketEntitlementID == "" || app.Content.TicketUnitsPerPurchase <= 0 ||
			s.entitlements == nil || s.unlocks == nil {
			return platformerr.New(platformerr.CodeContentLocked,
				"열람권이 운영 설정되지 않았어요")
		}
		requestKey := readingKey + "/" + deepKey
		sourceKey, err := s.entitlements.Consume(
			ctx, app, puid, app.Content.TicketEntitlementID,
			app.Content.TicketUnitsPerPurchase, requestKey,
		)
		if err != nil {
			return err
		}
		return s.unlocks.RecordTicket(ctx, app.AppID, puid, readingKey, deepKey, sourceKey)
	default:
		return platformerr.New(platformerr.CodeContentSelectorInvalid,
			"지원하지 않는 심화 권한 수단이에요")
	}
}

func (s *AccessService) verifiedRewardClaim(
	ctx context.Context,
	app registry.App,
	puid, claimID string,
) (platformads.Claim, error) {
	if app.Content.RewardKey == "" || s.claims == nil || s.unlocks == nil {
		return platformads.Claim{}, platformerr.New(platformerr.CodeContentLocked,
			"보상형 광고 권한이 운영 설정되지 않았어요")
	}
	claim, err := s.claims.GetClaim(ctx, claimID)
	if err != nil {
		return platformads.Claim{}, platformerr.Wrap(err, platformerr.CodeContentClaimInvalid,
			"광고 보상을 확인할 수 없어요")
	}
	if claim.AppID != app.AppID || claim.PlatformUserID != puid ||
		(claim.State != platformads.StateConfirmed && claim.State != platformads.StateDelivered) ||
		claim.Assurance != platformads.AssuranceServerVerified ||
		claim.Reward.Key != app.Content.RewardKey || claim.Reward.Amount <= 0 {
		return platformads.Claim{}, platformerr.New(platformerr.CodeContentClaimInvalid,
			"서버 검증된 광고 보상이 아니에요")
	}
	return claim, nil
}

func yearString(year int) string {
	// 1900~2200만 selector를 통과하므로 네 자리 10진수다.
	return string([]byte{
		byte('0' + year/1000%10), byte('0' + year/100%10),
		byte('0' + year/10%10), byte('0' + year%10),
	})
}
