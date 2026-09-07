// Package registry는 앱 레지스트리를 로드하고 캐시한다.
//
// 레지스트리의 source of truth는 repo의 registry/apps/*.json이다.
// CI는 스키마만 검증한다. Firestore로 올리는 것은 cmd/regsync를 사람이
// 돌리는 별도 단계다. 런타임은 여기서 캐시해 읽는다.
// 콘솔이나 Firestore를 직접 수정하지 않는다.
package registry

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// Status는 앱의 운영 상태다.
type Status string

const (
	// StatusActive는 정상이다.
	StatusActive Status = "active"
	// StatusPaused는 kill switch다. 모든 플랫폼 호출이 403이 된다.
	//
	// 진행 중인 결제도 막힌다. 이미 마켓에서 과금된 구매가 지급되지 않고
	// pending으로 남는다. 정지 해제 후 클라이언트의 pending proof 복구가
	// 처리하지만 정지가 길수록 CS가 늘어난다.
	StatusPaused Status = "paused"
)

// LedgerEnvironment는 IAP 원장 환경이다.
//
// production과 sandbox를 자동으로 넘나들지 않는다. 불변식 9다.
type LedgerEnvironment string

const (
	LedgerProduction LedgerEnvironment = "production"
	LedgerSandbox    LedgerEnvironment = "sandbox"
)

// App은 레지스트리 항목이다.
//
// json 태그는 registry/apps/*.json 파일용이고 firestore 태그는 저장용이다.
// 둘을 같은 이름으로 맞춘다. 다르면 파일과 Firestore를 대조할 때 헷갈린다.
type App struct {
	AppID             string `json:"app_id" firestore:"app_id"`
	DisplayName       string `json:"display_name" firestore:"display_name"`
	FirebaseProjectID string `json:"firebase_project_id" firestore:"firebase_project_id"`
	// FirebaseCustomTokenServiceAccount는 custom token 서명에 쓸 앱 프로젝트 SA다.
	// private key는 저장하지 않고 platform-api가 IAM Credentials API로 원격 서명한다.
	FirebaseCustomTokenServiceAccount string `json:"firebase_custom_token_service_account,omitempty" firestore:"firebase_custom_token_service_account,omitempty"`
	Status                            Status `json:"status" firestore:"status"`

	Features map[string]bool `json:"features" firestore:"features"`

	// RequireAppCheck는 공개 bootstrap 경로에서도 유효한 App Check
	// token을 요구한다. 앱별 단계 적용을 위해 전역 환경변수 대신 원장에 둔다.
	RequireAppCheck bool `json:"require_app_check" firestore:"require_app_check"`

	GA4 GA4Config `json:"ga4" firestore:"ga4"`
	// Auth는 외부 계정 공급자 allowlist와 공개 audience를 보관한다.
	// provider secret과 토큰은 레지스트리에 저장하지 않는다.
	Auth AuthConfig `json:"auth,omitempty" firestore:"auth,omitempty"`
	IAP  IAPConfig  `json:"iap" firestore:"iap"`
	Ads  AdsConfig  `json:"ads" firestore:"ads"`
	// Content는 private GCS 릴리스와 사용자별 조회 한도의 원장이다.
	// bucket에는 gs://를 넣지 않고, prefix에는 환경(staging/production)을 넣지 않는다.
	Content ContentConfig `json:"content,omitempty" firestore:"content,omitempty"`
	// Store는 마켓 배포 페이지 주소다. 업데이트 안내가 어디를 열지 결정한다.
	Store StoreConfig `json:"store,omitempty" firestore:"store,omitempty"`

	// PlatformEventAllowlist에 없는 이벤트는 플랫폼으로 보내지 않는다.
	// 비용과 QPS를 규모와 무관한 상수로 묶는 장치다.
	PlatformEventAllowlist []string `json:"platform_event_allowlist" firestore:"platform_event_allowlist"`

	// CORSOrigins는 웹, AIT와 Capacitor WebView 빌드용이다. 비어 있으면 CORS를 허용하지 않는다.
	CORSOrigins []string `json:"cors_origins" firestore:"cors_origins"`

	// RegistrySyncedAt은 regsync가 Firestore에 반영한 시각이다. JSON 원장에는
	// 들어가지 않으며 운영툴이 파일 변경과 런타임 반영을 구분할 때만 쓴다.
	RegistrySyncedAt time.Time `json:"-" firestore:"registry_synced_at,omitempty"`
}

type GA4Config struct {
	// EventPrefix는 플랫폼으로 보낼 때 벗겨낼 접두사다.
	//
	// 앱마다 접두사가 다르지만(game_, cp_, bc_) GA4는 앱별 속성이라
	// 원래 불필요했다. 플랫폼 테이블은 app_id 컬럼이 있으므로
	// 접두사를 벗겨야 횡단 쿼리가 가능해진다.
	// GA4로는 기존 이름 그대로 보내 시계열을 끊지 않는다.
	EventPrefix string `json:"event_prefix" firestore:"event_prefix"`
}

type IAPConfig struct {
	LedgerEnvironment LedgerEnvironment `json:"ledger_environment" firestore:"ledger_environment"`
	// LegacyUnscopedLedger는 다중 앱 이전에 생성된 원장 경로를 유지한다.
	// 신규 앱에서는 사용하지 않는다. lizard의 기존 IAP 데이터/SDK 회귀용이다.
	LegacyUnscopedLedger bool     `json:"legacy_unscoped_ledger,omitempty" firestore:"legacy_unscoped_ledger,omitempty"`
	Markets              []string `json:"markets" firestore:"markets"`
	// GooglePlayPackageName은 RTDN packageName을 appId에 묶는 원장이다.
	// 환경변수나 알림 payload만으로 앱을 추측하지 않는다. ADR 0014.
	GooglePlayPackageName string `json:"google_play_package_name,omitempty" firestore:"google_play_package_name,omitempty"`
	// AppStoreBundleID는 Apple 거래를 어느 앱에 묶을지 결정한다.
	// provider 전역 환경변수에 두면 여러 앱을 한 서비스에서 검증할 수 없다.
	AppStoreBundleID string `json:"app_store_bundle_id,omitempty" firestore:"app_store_bundle_id,omitempty"`
	// AppleSandboxEnabled는 기존 기본 환경과 별도로 Apple 테스트 거래만
	// 허용한다. 검증기·원장·worker 모두 이 허용 범위를 따른다. ADR 0027.
	AppleSandboxEnabled bool `json:"apple_sandbox_enabled,omitempty" firestore:"apple_sandbox_enabled,omitempty"`
	// EntitlementIDs는 이 앱에 지급할 수 있는 entitlement allowlist다.
	// 전역 SKU 카탈로그는 상품 매핑의 원장이고, 이 목록은 앱 경계의 원장이다.
	EntitlementIDs []string `json:"entitlement_ids" firestore:"entitlement_ids"`
	// RequireLinkedAccount는 결제·복원 전에 검증된 외부 계정 연결을 요구한다.
	// 기존 앱은 기본 false로 동작을 유지한다.
	RequireLinkedAccount bool `json:"require_linked_account,omitempty" firestore:"require_linked_account,omitempty"`
}

// IAPEnvironmentAllowed는 기본 환경을 보존하고 명시적으로 허용한 Apple
// sandbox만 추가한다. 클라이언트가 환경 이름만 바꿔 원장을 고를 수 없다.
func (a App) IAPEnvironmentAllowed(env LedgerEnvironment) bool {
	if env != LedgerProduction && env != LedgerSandbox {
		return false
	}
	return env == a.IAP.LedgerEnvironment ||
		(env == LedgerSandbox && a.IAP.AppleSandboxEnabled && a.FeatureEnabled("iap") && a.MarketEnabled("app_store"))
}

type AuthConfig struct {
	AccountProviders map[string]AuthProviderConfig `json:"account_providers,omitempty" firestore:"account_providers,omitempty"`
}

type AuthProviderConfig struct {
	// Audience는 OIDC ID token의 aud와 정확히 대조하는 공개 식별자다.
	Audience string `json:"audience" firestore:"audience"`
}

// AdsConfig는 보상 광고 정책의 앱별 원장이다.
// 광고 unit과 reward 정책은 콘솔이 아니라 registry/apps/*.json에서만 바꾼다.
type AdsConfig struct {
	Providers  []string             `json:"providers" firestore:"providers"`
	Placements []AdsPlacementConfig `json:"placements" firestore:"placements"`
}

type AdsPlacementConfig struct {
	ID              string                       `json:"id" firestore:"id"`
	Format          string                       `json:"format" firestore:"format"`
	Providers       map[string]AdsProviderConfig `json:"providers" firestore:"providers"`
	Reward          *AdsRewardConfig             `json:"reward,omitempty" firestore:"reward,omitempty"`
	DailyLimit      int                          `json:"daily_limit" firestore:"daily_limit"`
	CooldownSeconds int                          `json:"cooldown_seconds" firestore:"cooldown_seconds"`
}

type AdsProviderConfig struct {
	AndroidAdUnitID string `json:"android_ad_unit_id,omitempty" firestore:"android_ad_unit_id,omitempty"`
	IOSAdUnitID     string `json:"ios_ad_unit_id,omitempty" firestore:"ios_ad_unit_id,omitempty"`
	AdGroupID       string `json:"ad_group_id,omitempty" firestore:"ad_group_id,omitempty"`
	RewardItem      string `json:"reward_item,omitempty" firestore:"reward_item,omitempty"`
	RewardAmount    int    `json:"reward_amount,omitempty" firestore:"reward_amount,omitempty"`
}

type AdsRewardConfig struct {
	Key       string `json:"key" firestore:"key"`
	MinAmount int    `json:"min_amount" firestore:"min_amount"`
	MaxAmount int    `json:"max_amount" firestore:"max_amount"`
}

// ContentConfig는 인증된 콘텐츠 전달에 필요한 앱별 경계다.
// 실제 GCS IAM, Firebase App Check, AdMob/IAP 콘솔 활성 상태는 별도 운영 gate다.
type ContentConfig struct {
	Bucket            string `json:"bucket" firestore:"bucket"`
	Prefix            string `json:"prefix" firestore:"prefix"`
	ReadingDailyLimit int    `json:"reading_daily_limit" firestore:"reading_daily_limit"`
	TermDailyLimit    int    `json:"term_daily_limit" firestore:"term_daily_limit"`

	// RewardKey는 server_verified claim이 가져야 하는 광고 보상 key다.
	RewardKey string `json:"reward_key,omitempty" firestore:"reward_key,omitempty"`
	// TicketEntitlementID의 활성 IAP source 하나가 TicketUnitsPerPurchase만큼의
	// 심화 열람권을 만든다. 차감은 IAP 원장에서 request key로 멱등 처리한다.
	TicketEntitlementID    string `json:"ticket_entitlement_id,omitempty" firestore:"ticket_entitlement_id,omitempty"`
	TicketUnitsPerPurchase int    `json:"ticket_units_per_purchase,omitempty" firestore:"ticket_units_per_purchase,omitempty"`
	// SeasonEntitlements는 연도 문자열을 활성 entitlement에 연결한다.
	SeasonEntitlements map[string]string `json:"season_entitlements,omitempty" firestore:"season_entitlements,omitempty"`
}

// StoreConfig는 마켓 배포 페이지의 원장이다.
//
// 최소 지원 버전 같은 정책은 Firestore configs/{appId}에 있고 여기에는 거의
// 바뀌지 않는 주소만 둔다. 운영자가 차단 화면에서 URL을 직접 타이핑하지
// 못하게 하는 경계이기도 하다. 오타 하나가 유저를 엉뚱한 앱으로 보낸다.
//
// AppsInToss와 웹은 없다. 미니앱 번들은 토스가 전달하므로 유저가 "설치본을
// 업데이트"할 대상이 존재하지 않는다.
type StoreConfig struct {
	GooglePlayURL string `json:"google_play_url,omitempty" firestore:"google_play_url,omitempty"`
	AppStoreURL   string `json:"app_store_url,omitempty" firestore:"app_store_url,omitempty"`
}

var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var entitlementIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var serviceAccountPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{5,29}@[a-z0-9][a-z0-9-]{5,29}\.iam\.gserviceaccount\.com$`)
var androidPackagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$`)
var adsIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
var admobUnitPattern = regexp.MustCompile(`^ca-app-pub-[0-9]{16}/[0-9]{10}$`)
var gcsBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$`)
var contentPrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9/_-]{0,127}$`)
var authProviderAudiencePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,256}$`)

// 추적 파라미터를 허용하지 않는다. &hl=ko나 &utm_source=가 붙은 채 굳으면
// 나중에 아무도 걷어내지 못한다.
var playStoreURLPattern = regexp.MustCompile(
	`^https://play\.google\.com/store/apps/details\?id=[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$`)

// 국가 코드와 앱 이름 세그먼트는 선택이다. 정본은 뒤의 숫자 App ID다.
var appStoreURLPattern = regexp.MustCompile(
	`^https://apps\.apple\.com/(?:[a-z]{2}/)?app/(?:[^/?#]+/)?id[0-9]{6,12}$`)

// Validate는 레지스트리 항목을 검증한다.
//
// regsync가 Firestore로 올리기 전에 부르고, 런타임 로드에서도 부른다.
// 잘못된 항목이 하나 있어도 나머지는 살아야 하므로 항목 단위로 판정한다.
func (a App) Validate() error {
	if !appIDPattern.MatchString(a.AppID) {
		return fmt.Errorf("app_id가 형식에 맞지 않는다: %q", a.AppID)
	}
	if a.FirebaseProjectID == "" {
		return fmt.Errorf("%s: firebase_project_id가 필요하다", a.AppID)
	}
	if a.FeatureEnabled("firebase_custom_token_bridge") {
		if !serviceAccountPattern.MatchString(a.FirebaseCustomTokenServiceAccount) {
			return fmt.Errorf("%s: firebase custom token bridge에는 유효한 service account가 필요하다", a.AppID)
		}
		projectSuffix := "@" + a.FirebaseProjectID + ".iam.gserviceaccount.com"
		if !strings.HasSuffix(a.FirebaseCustomTokenServiceAccount, projectSuffix) {
			return fmt.Errorf("%s: custom token service account가 Firebase 프로젝트와 다르다", a.AppID)
		}
	} else if a.FirebaseCustomTokenServiceAccount != "" {
		return fmt.Errorf("%s: bridge가 비활성인데 custom token service account가 설정됐다", a.AppID)
	}
	switch a.Status {
	case StatusActive, StatusPaused:
	default:
		return fmt.Errorf("%s: status가 active 또는 paused여야 한다: %q", a.AppID, a.Status)
	}
	if a.IAP.LedgerEnvironment != "" {
		switch a.IAP.LedgerEnvironment {
		case LedgerProduction, LedgerSandbox:
		default:
			return fmt.Errorf("%s: iap.ledger_environment가 올바르지 않다: %q",
				a.AppID, a.IAP.LedgerEnvironment)
		}
	}
	if a.FeatureEnabled("iap") && len(a.IAP.EntitlementIDs) == 0 {
		return fmt.Errorf("%s: IAP 활성 앱에는 iap.entitlement_ids가 필요하다", a.AppID)
	}
	if err := a.validateAuth(); err != nil {
		return err
	}
	if a.IAP.RequireLinkedAccount && (!a.FeatureEnabled("iap") || len(a.Auth.AccountProviders) == 0) {
		return fmt.Errorf("%s: 연결 계정 필수 IAP에는 활성 IAP와 auth provider가 필요하다", a.AppID)
	}
	if a.IAP.AppleSandboxEnabled && (!a.FeatureEnabled("iap") || !a.MarketEnabled("app_store") || a.IAP.LedgerEnvironment != LedgerProduction) {
		return fmt.Errorf("%s: 추가 Apple sandbox에는 production 기본 환경과 활성 App Store IAP가 필요하다", a.AppID)
	}
	if a.FeatureEnabled("iap") && a.MarketEnabled("google_play") {
		if !androidPackagePattern.MatchString(a.IAP.GooglePlayPackageName) ||
			isPlaceholder(a.IAP.GooglePlayPackageName) {
			return fmt.Errorf("%s: Google Play IAP에는 유효한 iap.google_play_package_name이 필요하다", a.AppID)
		}
	} else if a.IAP.GooglePlayPackageName != "" {
		return fmt.Errorf("%s: Google Play IAP가 비활성인데 package name이 설정됐다", a.AppID)
	}
	if a.FeatureEnabled("iap") && a.MarketEnabled("app_store") {
		if !androidPackagePattern.MatchString(a.IAP.AppStoreBundleID) || isPlaceholder(a.IAP.AppStoreBundleID) {
			return fmt.Errorf("%s: App Store IAP에는 유효한 iap.app_store_bundle_id가 필요하다", a.AppID)
		}
	} else if a.IAP.AppStoreBundleID != "" {
		return fmt.Errorf("%s: App Store IAP가 비활성인데 bundle id가 설정됐다", a.AppID)
	}
	if len(a.IAP.EntitlementIDs) > 100 {
		return fmt.Errorf("%s: iap.entitlement_ids는 최대 100개다", a.AppID)
	}
	seenEntitlements := make(map[string]struct{}, len(a.IAP.EntitlementIDs))
	for _, entitlementID := range a.IAP.EntitlementIDs {
		if !entitlementIDPattern.MatchString(entitlementID) || isPlaceholder(entitlementID) {
			return fmt.Errorf("%s: iap.entitlement_ids 값이 올바르지 않다: %q",
				a.AppID, entitlementID)
		}
		if _, exists := seenEntitlements[entitlementID]; exists {
			return fmt.Errorf("%s: iap.entitlement_ids가 중복됐다: %q",
				a.AppID, entitlementID)
		}
		seenEntitlements[entitlementID] = struct{}{}
	}
	if err := a.validateAds(); err != nil {
		return err
	}
	if err := a.validateContent(); err != nil {
		return err
	}
	if err := a.validateCORSOrigins(); err != nil {
		return err
	}
	if err := a.validateStore(); err != nil {
		return err
	}
	// placeholder가 남은 채 배포되면 런타임에 이상하게 동작한다.
	// 부팅 시점에 잡는 편이 낫다.
	for _, v := range []string{a.AppID, a.DisplayName, a.FirebaseProjectID} {
		if isPlaceholder(v) {
			return fmt.Errorf("%s: placeholder가 남아 있다: %q", a.AppID, v)
		}
	}
	return nil
}

func (a App) validateAuth() error {
	if len(a.Auth.AccountProviders) == 0 {
		return nil
	}
	if !a.RequireAppCheck || !a.FeatureEnabled("firebase_custom_token_bridge") {
		return fmt.Errorf("%s: 외부 계정 연결에는 App Check와 firebase custom token bridge가 필요하다", a.AppID)
	}
	for provider, cfg := range a.Auth.AccountProviders {
		if provider != "kakao" && provider != "apple" {
			return fmt.Errorf("%s: 지원하지 않는 auth provider다: %q", a.AppID, provider)
		}
		if !authProviderAudiencePattern.MatchString(cfg.Audience) || isPlaceholder(cfg.Audience) {
			return fmt.Errorf("%s: %s auth audience가 올바르지 않다", a.AppID, provider)
		}
	}
	return nil
}

func (a App) validateContent() error {
	cfg := a.Content
	if !a.FeatureEnabled("content") {
		if cfg.Bucket != "" || cfg.Prefix != "" || cfg.ReadingDailyLimit != 0 ||
			cfg.TermDailyLimit != 0 || cfg.RewardKey != "" || cfg.TicketEntitlementID != "" ||
			cfg.TicketUnitsPerPurchase != 0 || len(cfg.SeasonEntitlements) != 0 {
			return fmt.Errorf("%s: content가 비활성인데 content 설정이 존재한다", a.AppID)
		}
		return nil
	}
	if !a.RequireAppCheck || !a.FeatureEnabled("firebase_custom_token_bridge") {
		return fmt.Errorf("%s: content 활성 앱은 App Check와 firebase custom token bridge가 필수다", a.AppID)
	}
	if !gcsBucketPattern.MatchString(cfg.Bucket) || strings.HasPrefix(cfg.Bucket, "gs://") ||
		isPlaceholder(cfg.Bucket) {
		return fmt.Errorf("%s: content.bucket이 올바르지 않다", a.AppID)
	}
	if !contentPrefixPattern.MatchString(cfg.Prefix) || strings.HasPrefix(cfg.Prefix, "/") ||
		strings.HasSuffix(cfg.Prefix, "/") || strings.Contains(cfg.Prefix, "//") ||
		strings.Contains(cfg.Prefix, "..") || hasEnvironmentPrefix(cfg.Prefix) || isPlaceholder(cfg.Prefix) {
		return fmt.Errorf("%s: content.prefix가 올바르지 않다", a.AppID)
	}
	if cfg.ReadingDailyLimit <= 0 || cfg.ReadingDailyLimit > 100 {
		return fmt.Errorf("%s: content.reading_daily_limit은 1~100이어야 한다", a.AppID)
	}
	if cfg.TermDailyLimit <= 0 || cfg.TermDailyLimit > 1000 {
		return fmt.Errorf("%s: content.term_daily_limit은 1~1000이어야 한다", a.AppID)
	}
	if cfg.RewardKey != "" {
		if !a.FeatureEnabled("ads") || !adsIDPattern.MatchString(cfg.RewardKey) {
			return fmt.Errorf("%s: content.reward_key에는 활성 광고 reward가 필요하다", a.AppID)
		}
		matched := false
		for _, placement := range a.Ads.Placements {
			if placement.Reward != nil && placement.Reward.Key == cfg.RewardKey {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("%s: content.reward_key가 광고 placement와 이어지지 않는다", a.AppID)
		}
	}
	if cfg.TicketEntitlementID == "" {
		if cfg.TicketUnitsPerPurchase != 0 {
			return fmt.Errorf("%s: ticket entitlement 없이 단위 수를 둘 수 없다", a.AppID)
		}
	} else if !a.FeatureEnabled("iap") || !a.EntitlementAllowed(cfg.TicketEntitlementID) ||
		cfg.TicketUnitsPerPurchase <= 0 || cfg.TicketUnitsPerPurchase > 100 {
		return fmt.Errorf("%s: content 열람권 IAP 설정이 올바르지 않다", a.AppID)
	}
	for year, entitlementID := range cfg.SeasonEntitlements {
		y, err := strconv.Atoi(year)
		if err != nil || y < 1900 || y > 2200 || !a.FeatureEnabled("iap") ||
			!a.EntitlementAllowed(entitlementID) {
			return fmt.Errorf("%s: content 시즌 entitlement가 올바르지 않다: %q", a.AppID, year)
		}
	}
	return nil
}

func hasEnvironmentPrefix(prefix string) bool {
	first, _, _ := strings.Cut(prefix, "/")
	return first == "staging" || first == "production"
}

func (a App) validateCORSOrigins() error {
	seen := make(map[string]struct{}, len(a.CORSOrigins))
	for _, origin := range a.CORSOrigins {
		u, err := url.Parse(origin)
		webOrigin := u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
		// Capacitor iOS는 WKWebView가 가로챌 수 없는 전용 scheme을 기본 origin으로 쓴다.
		// 임의 custom scheme을 열지 않고 공식 기본값 하나만 exact allowlist에 허용한다.
		capacitorIOSOrigin := u != nil && u.Scheme == "capacitor" && u.Host == "localhost"
		if err != nil || (!webOrigin && !capacitorIOSOrigin) ||
			u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%s: cors_origins 값이 올바른 origin이 아니다: %q", a.AppID, origin)
		}
		if _, exists := seen[origin]; exists {
			return fmt.Errorf("%s: cors_origins가 중복됐다: %q", a.AppID, origin)
		}
		seen[origin] = struct{}{}
	}
	return nil
}

// validateStore는 마켓 주소를 검증한다.
//
// ads·content와 달리 기능 플래그와 묶지 않는다. 무과금 앱도 스토어에 있고
// 업데이트 안내가 필요하다. 둘 다 비어 있는 것도 정상이다. 스토어 등록 전
// 앱이 부팅에 실패하면 안 된다.
func (a App) validateStore() error {
	if url := a.Store.GooglePlayURL; url != "" {
		if !playStoreURLPattern.MatchString(url) || isPlaceholder(url) {
			return fmt.Errorf("%s: store.google_play_url이 Play 스토어 주소 형식이 아니다: %q", a.AppID, url)
		}
		// 같은 파일 안에 이미 있는 사실을 대조하지 않을 이유가 없다.
		// 패키지명이 어긋난 링크는 유저를 다른 앱으로 보낸다.
		if pkg := a.IAP.GooglePlayPackageName; pkg != "" {
			if _, id, _ := strings.Cut(url, "?id="); id != pkg {
				return fmt.Errorf("%s: store.google_play_url이 iap.google_play_package_name과 다르다", a.AppID)
			}
		}
	}
	if url := a.Store.AppStoreURL; url != "" {
		if !appStoreURLPattern.MatchString(url) || isPlaceholder(url) {
			return fmt.Errorf("%s: store.app_store_url이 App Store 주소 형식이 아니다: %q", a.AppID, url)
		}
	}
	return nil
}

func (a App) validateAds() error {
	if !a.FeatureEnabled("ads") {
		if len(a.Ads.Providers) != 0 || len(a.Ads.Placements) != 0 {
			return fmt.Errorf("%s: 광고가 비활성인데 ads 설정이 존재한다", a.AppID)
		}
		return nil
	}
	if len(a.Ads.Providers) == 0 || len(a.Ads.Placements) == 0 {
		return fmt.Errorf("%s: 광고 활성 앱에는 provider와 placement가 필요하다", a.AppID)
	}
	providers := make(map[string]bool, len(a.Ads.Providers))
	for _, provider := range a.Ads.Providers {
		if provider != "admob" && provider != "apps_in_toss" {
			return fmt.Errorf("%s: 지원하지 않는 광고 provider다: %q", a.AppID, provider)
		}
		if providers[provider] {
			return fmt.Errorf("%s: 광고 provider가 중복됐다: %q", a.AppID, provider)
		}
		providers[provider] = true
	}
	seen := make(map[string]bool, len(a.Ads.Placements))
	for _, placement := range a.Ads.Placements {
		if !adsIDPattern.MatchString(placement.ID) || seen[placement.ID] {
			return fmt.Errorf("%s: 광고 placement id가 올바르지 않다: %q", a.AppID, placement.ID)
		}
		seen[placement.ID] = true
		if placement.Format != "rewarded" && placement.Format != "interstitial" {
			return fmt.Errorf("%s/%s: 광고 format이 올바르지 않다", a.AppID, placement.ID)
		}
		if placement.DailyLimit <= 0 || placement.CooldownSeconds < 0 {
			return fmt.Errorf("%s/%s: 일일 한도와 cooldown이 올바르지 않다", a.AppID, placement.ID)
		}
		if len(placement.Providers) == 0 {
			return fmt.Errorf("%s/%s: provider 설정이 필요하다", a.AppID, placement.ID)
		}
		for provider, cfg := range placement.Providers {
			if !providers[provider] {
				return fmt.Errorf("%s/%s: 앱에 허용되지 않은 provider다: %s", a.AppID, placement.ID, provider)
			}
			switch provider {
			case "admob":
				if cfg.AndroidAdUnitID != "" && !admobUnitPattern.MatchString(cfg.AndroidAdUnitID) {
					return fmt.Errorf("%s/%s: Android AdMob unit이 올바르지 않다", a.AppID, placement.ID)
				}
				if cfg.IOSAdUnitID != "" && !admobUnitPattern.MatchString(cfg.IOSAdUnitID) {
					return fmt.Errorf("%s/%s: iOS AdMob unit이 올바르지 않다", a.AppID, placement.ID)
				}
				if cfg.AndroidAdUnitID == "" && cfg.IOSAdUnitID == "" {
					return fmt.Errorf("%s/%s: AdMob unit이 하나 이상 필요하다", a.AppID, placement.ID)
				}
			case "apps_in_toss":
				if strings.TrimSpace(cfg.AdGroupID) == "" {
					return fmt.Errorf("%s/%s: AppsInToss ad group id가 필요하다", a.AppID, placement.ID)
				}
			}
		}
		if placement.Format == "rewarded" {
			if placement.Reward == nil || !adsIDPattern.MatchString(placement.Reward.Key) ||
				placement.Reward.MinAmount <= 0 || placement.Reward.MaxAmount < placement.Reward.MinAmount {
				return fmt.Errorf("%s/%s: reward 범위가 올바르지 않다", a.AppID, placement.ID)
			}
		} else if placement.Reward != nil {
			return fmt.Errorf("%s/%s: interstitial에는 reward를 둘 수 없다", a.AppID, placement.ID)
		}
	}
	return nil
}

func (a App) AdsPlacement(id string) (AdsPlacementConfig, bool) {
	for _, placement := range a.Ads.Placements {
		if placement.ID == id {
			return placement, true
		}
	}
	return AdsPlacementConfig{}, false
}

// AdMobUnits는 앱이 사용하는 AdMob unit을 중복 없이 돌려준다.
//
// 한 앱이 여러 placement에서 같은 unit을 공유하는 것은 허용하지만,
// 서로 다른 앱이 같은 unit을 공유하면 AdMob Console의 단일 SSV callback이
// 어느 appId로 가야 하는지 모호해진다.
func (a App) AdMobUnits() []string {
	seen := map[string]struct{}{}
	units := make([]string, 0)
	for _, placement := range a.Ads.Placements {
		provider, ok := placement.Providers["admob"]
		if !ok {
			continue
		}
		for _, unit := range []string{provider.AndroidAdUnitID, provider.IOSAdUnitID} {
			if unit == "" {
				continue
			}
			if _, exists := seen[unit]; exists {
				continue
			}
			seen[unit] = struct{}{}
			units = append(units, unit)
		}
	}
	return units
}

// ValidateAppSet은 개별 앱 검증과 앱 사이의 전역 식별자 경계를 함께 확인한다.
// regsync는 부분 반영 전에 이 함수를 호출해 잘못된 callback 귀속을 막는다.
func ValidateAppSet(apps []App) error {
	owners := map[string]string{}
	for _, app := range apps {
		if err := app.Validate(); err != nil {
			return err
		}
		for _, unit := range app.AdMobUnits() {
			if owner, exists := owners[unit]; exists && owner != app.AppID {
				return fmt.Errorf("AdMob unit이 앱 사이에 중복됐다: %s, %s", owner, app.AppID)
			}
			owners[unit] = app.AppID
		}
	}
	return nil
}

func isPlaceholder(s string) bool {
	t := strings.TrimSpace(s)
	switch t {
	case "확정 필요", "TBD", "TODO", "FIXME", "XXX":
		return true
	}
	return false
}

// Issuer는 이 앱의 Firebase ID 토큰 issuer다.
//
// Firebase ID 토큰의 서명키는 전 프로젝트 공통이므로
// 프로젝트 구분은 서명이 아니라 aud와 iss claim으로 한다.
func (a App) Issuer() string {
	return "https://securetoken.google.com/" + a.FirebaseProjectID
}

// Audience는 이 앱의 Firebase ID 토큰 audience다.
func (a App) Audience() string { return a.FirebaseProjectID }

// FeatureEnabled는 기능 활성 여부를 돌려준다.
// 선언되지 않은 기능은 꺼진 것으로 본다. 명시적으로 켜야 동작한다.
func (a App) FeatureEnabled(name string) bool {
	if a.Features == nil {
		return false
	}
	return a.Features[name]
}

// EntitlementAllowed는 entitlement가 이 앱의 명시 allowlist에 있는지 판정한다.
// IAP 활성화만으로 전역 카탈로그 전체를 허용하지 않는 fail-closed 경계다.
func (a App) EntitlementAllowed(entitlementID string) bool {
	for _, allowed := range a.IAP.EntitlementIDs {
		if allowed == entitlementID {
			return true
		}
	}
	return false
}

// UpdateURL은 플랫폼에 맞는 스토어 주소를 돌려준다.
//
// web과 ait은 설치본이 없으므로 빈 문자열이다. 주소가 없으면 클라이언트가
// 업데이트 버튼을 숨긴다. 눌러도 아무 일 없는 버튼을 만들지 않는다.
func (a App) UpdateURL(platform string) string {
	switch platform {
	case "android":
		return a.Store.GooglePlayURL
	case "ios":
		return a.Store.AppStoreURL
	}
	return ""
}

// MarketEnabled는 앱 레지스트리의 IAP 마켓 allowlist를 확인한다.
func (a App) MarketEnabled(market string) bool {
	for _, enabled := range a.IAP.Markets {
		if enabled == market {
			return true
		}
	}
	return false
}

// EventAllowed는 이벤트를 플랫폼으로 보낼지 판정한다.
func (a App) EventAllowed(name string) bool {
	for _, allowed := range a.PlatformEventAllowlist {
		if allowed == name {
			return true
		}
	}
	return false
}

// StripEventPrefix는 플랫폼 저장용으로 접두사를 벗긴다.
func (a App) StripEventPrefix(name string) string {
	if a.GA4.EventPrefix == "" {
		return name
	}
	return strings.TrimPrefix(name, a.GA4.EventPrefix)
}

// EnsureUsable은 앱이 요청을 받을 수 있는 상태인지 확인한다.
func (a App) EnsureUsable() error {
	if a.Status == StatusPaused {
		return platformerr.Newf(platformerr.CodeAppPaused,
			"%s는 지금 점검 중이에요", a.DisplayName)
	}
	return nil
}
