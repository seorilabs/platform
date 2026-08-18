package content

import (
	"context"
	"errors"
	"testing"
	"time"

	platformads "github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type fakeUnlocks struct {
	grant          UnlockGrant
	rewardClaim    string
	ticketRecorded bool
	ticketSource   string
	bindErr        error
}

func (f *fakeUnlocks) GetUnlock(context.Context, string, string, string, string) (UnlockGrant, error) {
	return f.grant, nil
}
func (f *fakeUnlocks) BindReward(_ context.Context, _, _, _, _, claim string) error {
	f.rewardClaim = claim
	if f.bindErr == nil {
		f.grant = UnlockGrant{Exists: true, Source: "reward_claim", Reference: claim}
	}
	return f.bindErr
}
func (f *fakeUnlocks) RecordTicket(_ context.Context, _, _, _, _, sourceKey string) error {
	f.ticketRecorded = true
	f.ticketSource = sourceKey
	f.grant = UnlockGrant{Exists: true, Source: "ticket", Reference: sourceKey}
	return nil
}

type fakeClaims struct {
	claim    platformads.Claim
	acked    bool
	ackErr   error
	ackCalls int
}

func (f *fakeClaims) GetClaim(context.Context, string) (platformads.Claim, error) {
	return f.claim, nil
}
func (f *fakeClaims) AcknowledgeClaim(
	context.Context, string, string, string, time.Time,
) (platformads.Claim, error) {
	f.ackCalls++
	if f.ackErr != nil {
		return platformads.Claim{}, f.ackErr
	}
	f.acked = true
	f.claim.State = platformads.StateDelivered
	return f.claim, nil
}

type fakeEntitlements struct {
	active       bool
	sourceActive bool
	consumed     bool
	requestKey   string
	sourceKey    string
}

func (f *fakeEntitlements) Active(context.Context, registry.App, string, string) (bool, error) {
	return f.active, nil
}
func (f *fakeEntitlements) SourceActive(
	context.Context, registry.App, string, string, string,
) (bool, error) {
	return f.sourceActive, nil
}
func (f *fakeEntitlements) Consume(
	_ context.Context, _ registry.App, _, _ string, _ int, key string,
) (string, error) {
	f.consumed, f.requestKey = true, key
	if f.sourceKey == "" {
		f.sourceKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	return f.sourceKey, nil
}

func accessApp() registry.App {
	return registry.App{AppID: "ungeul", Content: registry.ContentConfig{
		RewardKey: "deep-reading", TicketEntitlementID: "deep-ticket",
		TicketUnitsPerPurchase: 3, SeasonEntitlements: map[string]string{"2026": "season-2026"},
	}}
}

func TestRewardUnlockRequiresServerVerifiedClaim(t *testing.T) {
	unlocks := &fakeUnlocks{}
	claim := platformads.Claim{
		ClaimID: "cl_valid", AppID: "ungeul", PlatformUserID: "puid",
		State: platformads.StateConfirmed, Assurance: platformads.AssuranceClientConfirmed,
		Reward: platformads.Reward{Key: "deep-reading", Amount: 1},
	}
	claims := &fakeClaims{claim: claim}
	access := NewAccessService(unlocks, claims, nil)
	err := access.Unlock(t.Context(), accessApp(), "puid", "rk", "seun:2026", UnlockRequest{
		Kind: "reward_claim", Section: "seun", ClaimID: claim.ClaimID,
	})
	if platformerr.CodeOf(err) != platformerr.CodeContentClaimInvalid || unlocks.rewardClaim != "" {
		t.Fatalf("code=%q bound=%q err=%v", platformerr.CodeOf(err), unlocks.rewardClaim, err)
	}
}

func TestRewardUnlockBindsServerVerifiedClaim(t *testing.T) {
	unlocks := &fakeUnlocks{}
	claim := platformads.Claim{
		ClaimID: "cl_valid", AppID: "ungeul", PlatformUserID: "puid",
		State: platformads.StateConfirmed, Assurance: platformads.AssuranceServerVerified,
		Reward: platformads.Reward{Key: "deep-reading", Amount: 1},
	}
	claims := &fakeClaims{claim: claim}
	access := NewAccessService(unlocks, claims, nil)
	if err := access.Unlock(t.Context(), accessApp(), "puid", "rk", "seun:2026", UnlockRequest{
		Kind: "reward_claim", Section: "seun", ClaimID: claim.ClaimID,
	}); err != nil {
		t.Fatal(err)
	}
	if unlocks.rewardClaim != claim.ClaimID || !claims.acked {
		t.Fatalf("bound=%q acked=%v", unlocks.rewardClaim, claims.acked)
	}
}

func TestRewardUnlockDoesNotAcknowledgeBeforeBinding(t *testing.T) {
	bindErr := platformerr.New(platformerr.CodeContentUnavailable, "write failed")
	unlocks := &fakeUnlocks{bindErr: bindErr}
	claim := platformads.Claim{
		ClaimID: "cl_valid", AppID: "ungeul", PlatformUserID: "puid",
		State: platformads.StateConfirmed, Assurance: platformads.AssuranceServerVerified,
		Reward: platformads.Reward{Key: "deep-reading", Amount: 1},
	}
	claims := &fakeClaims{claim: claim}
	access := NewAccessService(unlocks, claims, nil)
	err := access.Unlock(t.Context(), accessApp(), "puid", "rk", "seun:2026", UnlockRequest{
		Kind: "reward_claim", Section: "seun", ClaimID: claim.ClaimID,
	})
	if platformerr.CodeOf(err) != platformerr.CodeContentUnavailable || claims.acked {
		t.Fatalf("code=%q acked=%v err=%v", platformerr.CodeOf(err), claims.acked, err)
	}
}

func TestRewardUnlockRetriesAcknowledgeAfterBinding(t *testing.T) {
	unlocks := &fakeUnlocks{}
	claim := platformads.Claim{
		ClaimID: "cl_valid", AppID: "ungeul", PlatformUserID: "puid",
		State: platformads.StateConfirmed, Assurance: platformads.AssuranceServerVerified,
		Reward: platformads.Reward{Key: "deep-reading", Amount: 1},
	}
	claims := &fakeClaims{claim: claim, ackErr: errors.New("ack failed")}
	access := NewAccessService(unlocks, claims, nil)
	req := UnlockRequest{Kind: "reward_claim", Section: "seun", ClaimID: claim.ClaimID}
	err := access.Unlock(t.Context(), accessApp(), "puid", "rk", "seun:2026", req)
	if platformerr.CodeOf(err) != platformerr.CodeContentUnavailable || !unlocks.grant.Exists || claims.ackCalls != 1 {
		t.Fatalf("code=%q grant=%+v ackCalls=%d err=%v", platformerr.CodeOf(err), unlocks.grant, claims.ackCalls, err)
	}

	claims.ackErr = nil
	allowed, err := access.Authorized(t.Context(), accessApp(), "puid", "rk", "seun:2026", 2026)
	if err != nil || !allowed {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
	if !claims.acked || claims.ackCalls != 2 {
		t.Fatalf("acked=%v ackCalls=%d", claims.acked, claims.ackCalls)
	}
}

func TestAuthorizedDoesNotConsumeClaimOpenedByAnotherSource(t *testing.T) {
	unlocks := &fakeUnlocks{grant: UnlockGrant{
		Exists: true, Source: "ticket",
		Reference: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	claims := &fakeClaims{}
	access := NewAccessService(unlocks, claims, &fakeEntitlements{sourceActive: true})
	allowed, err := access.Authorized(t.Context(), accessApp(), "puid", "rk", "seun:2026", 2026)
	if err != nil || !allowed {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
	if claims.ackCalls != 0 {
		t.Fatalf("다른 수단으로 열린 항목의 claim을 %d회 처리했다", claims.ackCalls)
	}
}

func TestTicketUnlockConsumesBeforeRecording(t *testing.T) {
	unlocks := &fakeUnlocks{}
	entitlements := &fakeEntitlements{}
	access := NewAccessService(unlocks, nil, entitlements)
	if err := access.Unlock(t.Context(), accessApp(), "puid", "rk", "wolun:2026", UnlockRequest{
		Kind: "ticket", Section: "wolun",
	}); err != nil {
		t.Fatal(err)
	}
	if !entitlements.consumed || !unlocks.ticketRecorded || entitlements.requestKey != "rk/wolun:2026" ||
		unlocks.ticketSource != entitlements.sourceKey {
		t.Fatalf("consume=%v record=%v key=%q source=%q", entitlements.consumed,
			unlocks.ticketRecorded, entitlements.requestKey, unlocks.ticketSource)
	}
}

func TestTicketUnlockRequiresConsumedSourceToRemainActive(t *testing.T) {
	sourceKey := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	unlocks := &fakeUnlocks{grant: UnlockGrant{
		Exists: true, Source: "ticket", Reference: sourceKey,
	}}
	entitlements := &fakeEntitlements{sourceActive: false}
	access := NewAccessService(unlocks, nil, entitlements)

	allowed, err := access.Authorized(t.Context(), accessApp(), "puid", "rk", "seun:2026", 2026)
	if err != nil || allowed {
		t.Fatalf("revoked source authorized=%v err=%v", allowed, err)
	}
	entitlements.sourceActive = true
	allowed, err = access.Authorized(t.Context(), accessApp(), "puid", "rk", "seun:2026", 2026)
	if err != nil || !allowed {
		t.Fatalf("active source authorized=%v err=%v", allowed, err)
	}
}

func TestSeasonPassIsCheckedByYear(t *testing.T) {
	entitlements := &fakeEntitlements{active: true}
	access := NewAccessService(&fakeUnlocks{}, nil, entitlements)
	got, err := access.Authorized(t.Context(), accessApp(), "puid", "rk", "seun:2026", 2026)
	if err != nil || !got {
		t.Fatalf("authorized=%v err=%v", got, err)
	}
}
