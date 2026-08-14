package ads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

type Repository interface {
	CreateClaim(context.Context, Claim, int, int) (Claim, error)
	GetClaim(context.Context, string) (Claim, error)
	ConfirmClaim(context.Context, ConfirmInput) (Claim, error)
	AcknowledgeClaim(context.Context, string, string, string, time.Time) (Claim, error)
	ExpireClaim(context.Context, string, time.Time) (Claim, error)
	OperatorSuppressed(context.Context, string, string) (bool, error)
	GrantSuppression(context.Context, SuppressionRecord) (SuppressionResult, error)
	RevokeSuppression(context.Context, SuppressionRecord) (SuppressionResult, error)
	SuppressionHistory(context.Context, string, string, int) ([]SuppressionRecord, error)
	ListClaims(context.Context, ClaimFilter) ([]Claim, error)
	Health(context.Context, time.Time) (Health, error)
	AppHealth(context.Context, string, time.Time) (AppHealth, error)
	RecordSSVResult(context.Context, string, SSVEvent, time.Time) error
	RecordPolicyFailure(context.Context, string) error
}

type Apps interface {
	GetUsable(context.Context, string) (registry.App, error)
	Get(context.Context, string) (registry.App, error)
	List(context.Context) ([]registry.App, error)
}

type Entitlements interface {
	ListActiveForApp(context.Context, registry.App, string) ([]string, error)
}

type SupportUsers interface {
	LookupSupportUser(context.Context, string) (identity.SupportUser, error)
}

type Service struct {
	repo         Repository
	apps         Apps
	entitlements Entitlements
	users        SupportUsers
	now          func() time.Time
}

func NewService(repo Repository, apps Apps, entitlements Entitlements, users SupportUsers) (*Service, error) {
	if repo == nil || apps == nil || entitlements == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid, "광고 서비스 설정이 올바르지 않아요")
	}
	return &Service{repo: repo, apps: apps, entitlements: entitlements, users: users, now: time.Now}, nil
}

func (s *Service) WithClock(now func() time.Time) *Service { s.now = now; return s }

type CreateClaimInput struct {
	RequestID, AppID, PlatformUserID, SupportCode, PlacementID, Provider, ClientPlatform string
	Reward                                                                               Reward
}

func (s *Service) Policy(ctx context.Context, appID, puid string) (Policy, error) {
	app, err := s.apps.GetUsable(ctx, appID)
	if err != nil {
		return Policy{}, s.policyError(ctx, appID, err)
	}
	now := s.now().UTC()
	if !app.FeatureEnabled("ads") {
		return Policy{AppUsesAds: false, AdsEnabled: false, DisabledBy: []string{}, CheckedAt: now}, nil
	}
	operator, err := s.repo.OperatorSuppressed(ctx, appID, puid)
	if err != nil {
		return Policy{}, s.policyError(ctx, appID, err)
	}
	active, err := s.entitlements.ListActiveForApp(ctx, app, puid)
	if err != nil {
		return Policy{}, s.policyError(ctx, appID, err)
	}
	disabled := make([]string, 0, 2)
	if operator {
		disabled = append(disabled, "operator")
	}
	for _, id := range active {
		if id == "ad_free" {
			disabled = append(disabled, "ad_free")
			break
		}
	}
	return Policy{AppUsesAds: true, AdsEnabled: len(disabled) == 0, DisabledBy: disabled, CheckedAt: now}, nil
}

func (s *Service) policyError(ctx context.Context, appID string, err error) error {
	_ = s.repo.RecordPolicyFailure(ctx, appID)
	return platformerr.Wrap(err, platformerr.CodePlatformUnavailable, "광고 정책을 확인하지 못했어요")
}

func (s *Service) CreateClaim(ctx context.Context, in CreateClaimInput) (Claim, error) {
	app, placement, err := s.validateClaimInput(ctx, in)
	if err != nil {
		return Claim{}, err
	}
	policy, err := s.Policy(ctx, app.AppID, in.PlatformUserID)
	if err != nil {
		return Claim{}, err
	}
	if !policy.AdsEnabled {
		return Claim{}, platformerr.New(platformerr.CodeAdsSuppressed, "광고가 차단된 사용자예요")
	}
	now := s.now().UTC()
	claim := Claim{
		ClaimID: "cl_" + uuid.NewString(), RequestID: in.RequestID, AppID: app.AppID,
		PlatformUserID: in.PlatformUserID, SupportCode: in.SupportCode,
		PlacementID: placement.ID, Provider: in.Provider, ClientPlatform: in.ClientPlatform,
		Reward: in.Reward, State: StateAccepted, Assurance: AssurancePending,
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), TTLAt: now.Add(90 * 24 * time.Hour),
	}
	// 보상을 받은 뒤 한도 초과로 거부되는 일을 줄이기 위해 claim 생성 시에도
	// 현재 사용량을 확인한다. 최종 원자적 한도 판정은 confirm에서 다시 한다.
	return s.repo.CreateClaim(ctx, claim, placement.DailyLimit, placement.CooldownSeconds)
}

func (s *Service) validateClaimInput(ctx context.Context, in CreateClaimInput) (registry.App, registry.AdsPlacementConfig, error) {
	if !requestIDPattern.MatchString(in.RequestID) || in.PlatformUserID == "" || in.SupportCode == "" {
		return registry.App{}, registry.AdsPlacementConfig{}, platformerr.New(platformerr.CodeRequestInvalid, "requestId와 사용자 정보가 필요해요")
	}
	if in.Provider != "admob" && in.Provider != "apps_in_toss" {
		return registry.App{}, registry.AdsPlacementConfig{}, platformerr.New(platformerr.CodeProviderConfigInvalid, "지원하지 않는 광고 provider예요")
	}
	if in.ClientPlatform != "android" && in.ClientPlatform != "ios" && in.ClientPlatform != "apps_in_toss" {
		return registry.App{}, registry.AdsPlacementConfig{}, platformerr.New(platformerr.CodePlatformInvalid, "광고 플랫폼이 올바르지 않아요")
	}
	if (in.Provider == "admob" && in.ClientPlatform == "apps_in_toss") ||
		(in.Provider == "apps_in_toss" && in.ClientPlatform != "apps_in_toss") {
		return registry.App{}, registry.AdsPlacementConfig{}, platformerr.New(
			platformerr.CodePlatformInvalid, "광고 provider와 실행 플랫폼이 일치하지 않아요")
	}
	app, err := s.apps.GetUsable(ctx, in.AppID)
	if err != nil {
		return registry.App{}, registry.AdsPlacementConfig{}, err
	}
	if !app.FeatureEnabled("ads") {
		return registry.App{}, registry.AdsPlacementConfig{}, platformerr.New(platformerr.CodeAdsNotEnabled, "광고 기능을 사용하지 않는 앱이에요")
	}
	placement, ok := app.AdsPlacement(in.PlacementID)
	if !ok || placement.Format != "rewarded" {
		return registry.App{}, registry.AdsPlacementConfig{}, platformerr.New(platformerr.CodeAdPlacementInvalid, "허용되지 않은 보상 광고 지면이에요")
	}
	if _, ok := placement.Providers[in.Provider]; !ok {
		return registry.App{}, registry.AdsPlacementConfig{}, platformerr.New(platformerr.CodeProviderConfigInvalid, "지면에 허용되지 않은 provider예요")
	}
	if placement.Reward == nil || in.Reward.Key != placement.Reward.Key || in.Reward.Amount < placement.Reward.MinAmount || in.Reward.Amount > placement.Reward.MaxAmount {
		return registry.App{}, registry.AdsPlacementConfig{}, platformerr.New(platformerr.CodeAdRewardInvalid, "보상 범위가 올바르지 않아요")
	}
	return app, placement, nil
}

func (s *Service) GetClaim(ctx context.Context, appID, puid, claimID string) (Claim, error) {
	claim, err := s.repo.GetClaim(ctx, claimID)
	if err != nil {
		return Claim{}, err
	}
	if claim.AppID != appID || claim.PlatformUserID != puid {
		return Claim{}, platformerr.New(platformerr.CodeClaimOwnershipMismatch, "보상 claim을 찾을 수 없어요")
	}
	if claim.State == StateAccepted && !s.now().Before(claim.ExpiresAt) {
		return s.repo.ExpireClaim(ctx, claimID, s.now().UTC())
	}
	return claim, nil
}

func (s *Service) ConfirmClient(ctx context.Context, appID, puid, claimID, transactionID string) (Claim, error) {
	claim, err := s.GetClaim(ctx, appID, puid, claimID)
	if err != nil {
		return Claim{}, err
	}
	if claim.Provider != "apps_in_toss" {
		return Claim{}, platformerr.New(platformerr.CodeClaimAssuranceInvalid, "이 provider는 클라이언트 확인을 허용하지 않아요")
	}
	app, err := s.apps.GetUsable(ctx, appID)
	if err != nil {
		return Claim{}, err
	}
	placement, ok := app.AdsPlacement(claim.PlacementID)
	if !ok {
		return Claim{}, platformerr.New(platformerr.CodeAdPlacementInvalid, "광고 지면 설정을 찾을 수 없어요")
	}
	return s.repo.ConfirmClaim(ctx, ConfirmInput{ClaimID: claimID, AppID: appID, PlatformUserID: puid, Provider: claim.Provider, TransactionHash: hash(strings.TrimSpace(transactionID)), Assurance: AssuranceClientConfirmed, Now: s.now().UTC(), DailyLimit: placement.DailyLimit, CooldownSeconds: placement.CooldownSeconds})
}

func (s *Service) ConfirmServer(ctx context.Context, appID, claimID, transactionID string) (Claim, error) {
	claim, err := s.repo.GetClaim(ctx, claimID)
	if err != nil {
		return Claim{}, err
	}
	if claim.AppID != appID || claim.Provider != "admob" {
		return Claim{}, platformerr.New(platformerr.CodeClaimOwnershipMismatch, "보상 claim을 찾을 수 없어요")
	}
	if claim.State == StateAccepted && !s.now().Before(claim.ExpiresAt) {
		if _, err := s.repo.ExpireClaim(ctx, claimID, s.now().UTC()); err != nil {
			return Claim{}, err
		}
		return Claim{}, platformerr.New(platformerr.CodeClaimExpired, "보상 claim이 만료됐어요")
	}
	app, err := s.apps.GetUsable(ctx, appID)
	if err != nil {
		return Claim{}, err
	}
	placement, ok := app.AdsPlacement(claim.PlacementID)
	if !ok {
		return Claim{}, platformerr.New(platformerr.CodeAdPlacementInvalid, "광고 지면 설정을 찾을 수 없어요")
	}
	return s.repo.ConfirmClaim(ctx, ConfirmInput{ClaimID: claimID, AppID: appID, PlatformUserID: claim.PlatformUserID, Provider: "admob", TransactionHash: hash(transactionID), Assurance: AssuranceServerVerified, Now: s.now().UTC(), DailyLimit: placement.DailyLimit, CooldownSeconds: placement.CooldownSeconds})
}

func (s *Service) ConfirmAdMob(ctx context.Context, appID string, result SSVResult) (Claim, error) {
	claim, err := s.repo.GetClaim(ctx, result.ClaimID)
	if err != nil {
		return Claim{}, err
	}
	if claim.AppID != appID || claim.PlatformUserID != result.PlatformUserID || claim.Provider != "admob" {
		return Claim{}, platformerr.New(platformerr.CodeClaimOwnershipMismatch, "보상 claim 소유자가 일치하지 않아요")
	}
	app, err := s.apps.GetUsable(ctx, appID)
	if err != nil {
		return Claim{}, err
	}
	placement, ok := app.AdsPlacement(claim.PlacementID)
	if !ok {
		return Claim{}, platformerr.New(platformerr.CodeAdPlacementInvalid, "광고 지면 설정을 찾을 수 없어요")
	}
	provider, ok := placement.Providers["admob"]
	if !ok {
		return Claim{}, platformerr.New(platformerr.CodeProviderConfigInvalid, "AdMob 지면 설정을 찾을 수 없어요")
	}
	wantUnit := provider.AndroidAdUnitID
	if claim.ClientPlatform == "ios" {
		wantUnit = provider.IOSAdUnitID
	}
	if wantUnit == "" || adUnitSuffix(wantUnit) != result.AdUnitID {
		return Claim{}, platformerr.New(platformerr.CodeAdUnitMismatch, "광고 unit이 claim과 일치하지 않아요")
	}
	if result.RewardAmount <= 0 || provider.RewardItem != "" && result.RewardItem != provider.RewardItem || provider.RewardAmount > 0 && result.RewardAmount != provider.RewardAmount {
		return Claim{}, platformerr.New(platformerr.CodeAdRewardInvalid, "AdMob 보상 설정이 일치하지 않아요")
	}
	return s.ConfirmServer(ctx, appID, result.ClaimID, result.TransactionID)
}

func (s *Service) Acknowledge(ctx context.Context, appID, puid, claimID string) (Claim, error) {
	return s.repo.AcknowledgeClaim(ctx, claimID, appID, puid, s.now().UTC())
}

func (s *Service) SSVData(ctx context.Context, appID, puid, claimID string) (map[string]string, error) {
	claim, err := s.GetClaim(ctx, appID, puid, claimID)
	if err != nil {
		return nil, err
	}
	if claim.Provider != "admob" {
		return nil, platformerr.New(platformerr.CodeProviderConfigInvalid, "AdMob claim이 아니에요")
	}
	return map[string]string{"customData": claim.ClaimID, "userId": claim.PlatformUserID}, nil
}

func (s *Service) LookupUserAds(ctx context.Context, puid string) (UserAds, error) {
	if s.users == nil {
		return UserAds{}, platformerr.New(platformerr.CodeRuntimeConfigInvalid, "사용자 조회가 준비되지 않았어요")
	}
	user, err := s.users.LookupSupportUser(ctx, puid)
	if err != nil {
		return UserAds{}, err
	}
	policy, err := s.Policy(ctx, user.AppID, user.PlatformUserID)
	if err != nil {
		return UserAds{}, err
	}
	history, err := s.repo.SuppressionHistory(ctx, user.AppID, user.PlatformUserID, 100)
	if err != nil {
		return UserAds{}, err
	}
	return UserAds{AppID: user.AppID, PlatformUserID: user.PlatformUserID, SupportCode: user.SupportCode, IsAnonymous: user.IsAnonymous, AuthType: user.AuthType, LastSeenAt: user.LastSeenAt, Policy: policy, AuditHistory: history}, nil
}

func (s *Service) Health(ctx context.Context) (Health, error) {
	return s.repo.Health(ctx, s.now().UTC())
}
func (s *Service) AppHealth(ctx context.Context, appID string) (AppHealth, error) {
	if _, err := s.AppConfig(ctx, appID); err != nil {
		return AppHealth{}, err
	}
	return s.repo.AppHealth(ctx, appID, s.now().UTC())
}
func (s *Service) ListClaims(ctx context.Context, filter ClaimFilter) ([]Claim, error) {
	return s.repo.ListClaims(ctx, filter)
}
func (s *Service) AppConfig(ctx context.Context, appID string) (registry.App, error) {
	app, err := s.apps.Get(ctx, appID)
	if err != nil {
		return registry.App{}, err
	}
	if !app.FeatureEnabled("ads") {
		return registry.App{}, platformerr.New(platformerr.CodeAdsNotEnabled, "광고 기능을 사용하지 않는 앱이에요")
	}
	return app, nil
}

func (s *Service) GrantSuppression(ctx context.Context, record SuppressionRecord) (SuppressionResult, error) {
	return s.mutateSuppression(ctx, record, false)
}
func (s *Service) RevokeSuppression(ctx context.Context, record SuppressionRecord) (SuppressionResult, error) {
	return s.mutateSuppression(ctx, record, true)
}

func (s *Service) mutateSuppression(ctx context.Context, record SuppressionRecord, revoke bool) (SuppressionResult, error) {
	if !requestIDPattern.MatchString(record.RequestID) || record.AppID == "" || record.PlatformUserID == "" || record.ActorLogin == "" || !ValidAdminReason(record.Reason) {
		return SuppressionResult{}, platformerr.New(platformerr.CodeRequestInvalid, "운영자 광고 차단 요청이 올바르지 않아요")
	}
	app, err := s.apps.GetUsable(ctx, record.AppID)
	if err != nil {
		return SuppressionResult{}, err
	}
	if !app.FeatureEnabled("ads") {
		return SuppressionResult{}, platformerr.New(platformerr.CodeAdsNotEnabled, "광고 기능을 사용하지 않는 앱이에요")
	}
	if s.users != nil {
		user, err := s.users.LookupSupportUser(ctx, record.PlatformUserID)
		if err != nil {
			return SuppressionResult{}, err
		}
		if user.AppID != record.AppID {
			return SuppressionResult{}, platformerr.New(platformerr.CodeAppUserMismatch, "사용자와 앱이 일치하지 않아요")
		}
	}
	record.CreatedAt = s.now().UTC()
	if revoke {
		record.Operation = "revoke"
		return s.repo.RevokeSuppression(ctx, record)
	}
	record.Operation = "grant"
	return s.repo.GrantSuppression(ctx, record)
}

func ValidAdminReason(reason string) bool {
	switch reason {
	case "customer_support_compensation", "incorrect_grant_correction", "incident_recovery", "internal_validation":
		return true
	default:
		return false
	}
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func adUnitSuffix(value string) string {
	if i := strings.LastIndexByte(value, '/'); i >= 0 {
		return value[i+1:]
	}
	return value
}
