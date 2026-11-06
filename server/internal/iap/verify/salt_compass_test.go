package verify

import (
	"context"
	"testing"

	"github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/registry"
)

type saltFixtureLedger struct {
	fakeLedger
	calls int
}

func (l *saltFixtureLedger) SaltSnapshot(context.Context, string) (ledger.SaltSnapshot, error) {
	return ledger.SaltSnapshot{}, nil
}
func (l *saltFixtureLedger) TransactSalt(context.Context, string, ledger.SaltRequest) (ledger.SaltReceipt, error) {
	l.calls++
	return ledger.SaltReceipt{}, nil
}

type saltFixtureClaim struct{ claim ads.Claim }

func (c saltFixtureClaim) GetClaim(context.Context, string) (ads.Claim, error) { return c.claim, nil }

func TestSaltServiceRejectsForeignScopeAndUnsettledReward(t *testing.T) {
	l := &saltFixtureLedger{}
	app := registry.App{AppID: "salt-compass", Features: map[string]bool{"iap": true}}
	s := &Service{apps: boxApps{app: app}, appLedgers: map[string]Ledger{"salt-compass": l}}
	ctx := context.Background()
	if _, err := s.SaltSnapshot(ctx, "other-app", "owner"); err == nil {
		t.Fatal("foreign app reached Salt state")
	}
	s.appLedgers = nil
	if _, err := s.SaltSnapshot(ctx, "salt-compass", "owner"); err == nil {
		t.Fatal("missing scoped ledger used legacy fallback")
	}
	s.appLedgers = map[string]Ledger{"salt-compass": l}
	base := ads.Claim{AppID: "salt-compass", PlatformUserID: "owner", Provider: ads.ProviderAdMob, State: ads.StateConfirmed, Assurance: ads.AssuranceServerVerified, PlacementID: "rewarded_offline_double", Reward: ads.Reward{Key: "seal", Amount: 3}}
	for _, change := range []func(*ads.Claim){
		func(c *ads.Claim) { c.PlatformUserID = "other" },
		func(c *ads.Claim) { c.AppID = "other-app" },
		func(c *ads.Claim) { c.Assurance = ads.AssuranceClientConfirmed },
		func(c *ads.Claim) { c.Reward.Amount = 60 },
		func(c *ads.Claim) { c.PlacementID = "rewarded_timer_skip" },
	} {
		claim := base
		change(&claim)
		s.saltRewardClaims = saltFixtureClaim{claim}
		if _, err := s.TransactSalt(ctx, "salt-compass", "owner", ledger.SaltRequest{RequestID: "r", Action: "ad_reward", ClaimID: "c"}); err == nil || l.calls != 0 {
			t.Fatal("untrusted reward reached credit")
		}
	}
	s.saltRewardClaims = saltFixtureClaim{base}
	if _, err := s.TransactSalt(ctx, "salt-compass", "owner", ledger.SaltRequest{RequestID: "r", Action: "ad_reward", ClaimID: "c"}); err != nil || l.calls != 1 {
		t.Fatalf("settled owner reward rejected: %v", err)
	}
}
