package ads

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type fakeRepo struct {
	claims        map[string]Claim
	operator      bool
	policyErr     error
	policyFailure int
	createErr     error
	ssvEvents     []recordedSSVEvent
}

type recordedSSVEvent struct {
	appID string
	event SSVEvent
}

func (f *fakeRepo) CreateClaim(_ context.Context, c Claim, _, _ int) (Claim, error) {
	if f.createErr != nil {
		return Claim{}, f.createErr
	}
	if f.claims == nil {
		f.claims = map[string]Claim{}
	}
	f.claims[c.ClaimID] = c
	return c, nil
}
func (f *fakeRepo) GetClaim(_ context.Context, id string) (Claim, error) {
	c, ok := f.claims[id]
	if !ok {
		return Claim{}, platformerr.New(platformerr.CodeClaimNotFound, "not found")
	}
	return c, nil
}
func (f *fakeRepo) ConfirmClaim(_ context.Context, in ConfirmInput) (Claim, error) {
	c := f.claims[in.ClaimID]
	c.State, c.Assurance, c.TransactionHash = StateConfirmed, in.Assurance, in.TransactionHash
	c.ConfirmedAt = &in.Now
	f.claims[in.ClaimID] = c
	return c, nil
}
func (f *fakeRepo) AcknowledgeClaim(_ context.Context, id, appID, puid string, now time.Time) (Claim, error) {
	c := f.claims[id]
	if c.AppID != appID || c.PlatformUserID != puid {
		return Claim{}, platformerr.New(platformerr.CodeClaimOwnershipMismatch, "mismatch")
	}
	c.State, c.AcknowledgedAt = StateDelivered, &now
	f.claims[id] = c
	return c, nil
}
func (f *fakeRepo) ExpireClaim(_ context.Context, id string, _ time.Time) (Claim, error) {
	c := f.claims[id]
	c.State = StateExpired
	f.claims[id] = c
	return c, nil
}
func (f *fakeRepo) OperatorSuppressed(context.Context, string, string) (bool, error) {
	return f.operator, f.policyErr
}
func (f *fakeRepo) GrantSuppression(context.Context, SuppressionRecord) (SuppressionResult, error) {
	return SuppressionResult{}, nil
}
func (f *fakeRepo) RevokeSuppression(context.Context, SuppressionRecord) (SuppressionResult, error) {
	return SuppressionResult{}, nil
}
func (f *fakeRepo) SuppressionHistory(context.Context, string, string, int) ([]SuppressionRecord, error) {
	return nil, nil
}
func (f *fakeRepo) ListClaims(context.Context, ClaimFilter) ([]Claim, error) { return nil, nil }
func (f *fakeRepo) Health(context.Context, time.Time) (Health, error)        { return Health{}, nil }
func (f *fakeRepo) RecordSSVResult(_ context.Context, appID string, event SSVEvent, _ time.Time) error {
	f.ssvEvents = append(f.ssvEvents, recordedSSVEvent{appID: appID, event: event})
	return nil
}
func (f *fakeRepo) RecordPolicyFailure(_ context.Context, _ string) error {
	f.policyFailure++
	return nil
}
func (f *fakeRepo) AppHealth(_ context.Context, appID string, now time.Time) (AppHealth, error) {
	return AppHealth{AppID: appID, Status: "ok", CheckedAt: now}, nil
}

type fakeApps map[string]registry.App

func (f fakeApps) GetUsable(_ context.Context, id string) (registry.App, error) {
	return f.Get(context.Background(), id)
}
func (f fakeApps) Get(_ context.Context, id string) (registry.App, error) {
	a, ok := f[id]
	if !ok {
		return registry.App{}, platformerr.New(platformerr.CodeAppUnknown, "unknown")
	}
	return a, nil
}
func (f fakeApps) List(context.Context) ([]registry.App, error) {
	out := make([]registry.App, 0, len(f))
	for _, a := range f {
		out = append(out, a)
	}
	return out, nil
}

type fakeEntitlements struct {
	ids []string
	err error
}

func (f fakeEntitlements) ListActiveForApp(context.Context, registry.App, string) ([]string, error) {
	return f.ids, f.err
}

type fakeUsers struct{ user identity.SupportUser }

func (f fakeUsers) LookupSupportUser(context.Context, string) (identity.SupportUser, error) {
	return f.user, nil
}

func rewardedApp() registry.App {
	return registry.App{
		AppID: "happy-farm", Status: registry.StatusActive,
		Features: map[string]bool{"ads": true},
		Ads: registry.AdsConfig{
			Providers: []string{"admob", "apps_in_toss"},
			Placements: []registry.AdsPlacementConfig{{
				ID: "harvest_boost", Format: "rewarded", DailyLimit: 20, CooldownSeconds: 30,
				Reward: &registry.AdsRewardConfig{Key: "harvest_boost", MinAmount: 1, MaxAmount: 1},
				Providers: map[string]registry.AdsProviderConfig{
					"admob":        {AndroidAdUnitID: "ca-app-pub-0000000000000000/1234567890"},
					"apps_in_toss": {AdGroupID: "ait-group"},
				},
			}},
		},
	}
}

func newTestService(t *testing.T, repo *fakeRepo, ent fakeEntitlements) *Service {
	t.Helper()
	apps := fakeApps{
		"happy-farm":    rewardedApp(),
		"lizard-tycoon": {AppID: "lizard-tycoon", Status: registry.StatusActive, Features: map[string]bool{"ads": false}},
	}
	svc, err := NewService(repo, apps, ent, fakeUsers{})
	if err != nil {
		t.Fatal(err)
	}
	svc.WithClock(func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) })
	return svc
}

func TestPolicyCombinesAllSuppressionReasons(t *testing.T) {
	tests := []struct {
		name        string
		operator    bool
		entitlement []string
		want        []string
	}{
		{name: "허용", want: []string{}},
		{name: "운영자 차단", operator: true, want: []string{"operator"}},
		{name: "ad_free", entitlement: []string{"ad_free"}, want: []string{"ad_free"}},
		{name: "두 원인", operator: true, entitlement: []string{"ad_free"}, want: []string{"operator", "ad_free"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t, &fakeRepo{operator: tt.operator}, fakeEntitlements{ids: tt.entitlement})
			got, err := svc.Policy(context.Background(), "happy-farm", "pu_1")
			if err != nil {
				t.Fatal(err)
			}
			if got.AdsEnabled != (len(tt.want) == 0) || len(got.DisabledBy) != len(tt.want) {
				t.Fatalf("policy = %#v, want reasons %v", got, tt.want)
			}
			for i := range tt.want {
				if got.DisabledBy[i] != tt.want[i] {
					t.Fatalf("reasons = %v, want %v", got.DisabledBy, tt.want)
				}
			}
		})
	}
}

func TestPolicyFailureIsFailClosedAndCounted(t *testing.T) {
	repo := &fakeRepo{policyErr: errors.New("firestore unavailable")}
	svc := newTestService(t, repo, fakeEntitlements{})
	if _, err := svc.Policy(context.Background(), "happy-farm", "pu_1"); platformerr.CodeOf(err) != platformerr.CodePlatformUnavailable {
		t.Fatalf("code = %q", platformerr.CodeOf(err))
	}
	if repo.policyFailure != 1 {
		t.Fatalf("policy failure count = %d", repo.policyFailure)
	}
}

func TestLizardTycoonHasNoAdsContract(t *testing.T) {
	svc := newTestService(t, &fakeRepo{}, fakeEntitlements{err: errors.New("must not read IAP")})
	policy, err := svc.Policy(context.Background(), "lizard-tycoon", "pu_1")
	if err != nil {
		t.Fatal(err)
	}
	if policy.AppUsesAds || policy.AdsEnabled {
		t.Fatalf("policy = %#v", policy)
	}
}

func TestLookupUserAdsReturnsSafeAuthType(t *testing.T) {
	repo := &fakeRepo{}
	apps := fakeApps{"happy-farm": rewardedApp()}
	users := fakeUsers{user: identity.SupportUser{
		PlatformUserID: "pu_1", AppID: "happy-farm", SupportCode: "HF-SUPPORT",
		AuthType: "apps_in_toss", LastSeenAt: time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC),
	}}
	svc, err := NewService(repo, apps, fakeEntitlements{}, users)
	if err != nil {
		t.Fatal(err)
	}
	svc.WithClock(func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) })

	got, err := svc.LookupUserAds(context.Background(), "pu_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AuthType != "apps_in_toss" || got.SupportCode != "HF-SUPPORT" {
		t.Fatalf("user ads = %#v", got)
	}
}

func TestCreateClaimValidatesProviderPlatformAndSuppression(t *testing.T) {
	base := CreateClaimInput{RequestID: "req-1", AppID: "happy-farm", PlatformUserID: "pu_1", SupportCode: "SUPPORT", PlacementID: "harvest_boost", Reward: Reward{Key: "harvest_boost", Amount: 1}}
	tests := []struct {
		name     string
		provider string
		platform string
		operator bool
		wantCode platformerr.Code
	}{
		{name: "AdMob Android", provider: "admob", platform: "android"},
		{name: "AIT", provider: "apps_in_toss", platform: "apps_in_toss"},
		{name: "AIT와 mobile 불일치", provider: "apps_in_toss", platform: "android", wantCode: platformerr.CodePlatformInvalid},
		{name: "AdMob과 AIT 불일치", provider: "admob", platform: "apps_in_toss", wantCode: platformerr.CodePlatformInvalid},
		{name: "정책 차단", provider: "admob", platform: "ios", operator: true, wantCode: platformerr.CodeAdsSuppressed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t, &fakeRepo{operator: tt.operator}, fakeEntitlements{})
			in := base
			in.Provider, in.ClientPlatform = tt.provider, tt.platform
			claim, err := svc.CreateClaim(context.Background(), in)
			if tt.wantCode != "" {
				if platformerr.CodeOf(err) != tt.wantCode {
					t.Fatalf("code = %q, want %q", platformerr.CodeOf(err), tt.wantCode)
				}
				return
			}
			if err != nil || claim.State != StateAccepted || claim.Assurance != AssurancePending {
				t.Fatalf("claim=%#v err=%v", claim, err)
			}
			if claim.ExpiresAt.Sub(claim.CreatedAt) != 24*time.Hour || claim.TTLAt.Sub(claim.CreatedAt) != 90*24*time.Hour {
				t.Fatalf("claim lifecycle = %#v", claim)
			}
		})
	}
}

func TestClientConfirmationCannotUpgradeAdMob(t *testing.T) {
	repo := &fakeRepo{claims: map[string]Claim{"cl_1": {ClaimID: "cl_1", AppID: "happy-farm", PlatformUserID: "pu_1", Provider: "admob", State: StateAccepted}}}
	svc := newTestService(t, repo, fakeEntitlements{})
	_, err := svc.ConfirmClient(context.Background(), "happy-farm", "pu_1", "cl_1", "transaction")
	if platformerr.CodeOf(err) != platformerr.CodeClaimAssuranceInvalid {
		t.Fatalf("code = %q", platformerr.CodeOf(err))
	}
}

func TestServerConfirmationPersistsExpiredClaim(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{claims: map[string]Claim{"cl_1": {
		ClaimID: "cl_1", AppID: "happy-farm", PlatformUserID: "pu_1",
		Provider: "admob", State: StateAccepted, ExpiresAt: now.Add(-time.Second),
	}}}
	svc := newTestService(t, repo, fakeEntitlements{})

	_, err := svc.ConfirmServer(context.Background(), "happy-farm", "cl_1", "transaction")
	if platformerr.CodeOf(err) != platformerr.CodeClaimExpired {
		t.Fatalf("code = %q, want %q", platformerr.CodeOf(err), platformerr.CodeClaimExpired)
	}
	if got := repo.claims["cl_1"].State; got != StateExpired {
		t.Fatalf("state = %q, want %q", got, StateExpired)
	}
}
