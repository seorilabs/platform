// Package registry는 앱 레지스트리를 로드하고 캐시한다.
//
// 레지스트리의 source of truth는 repo의 registry/apps/*.json이다.
// CI는 스키마만 검증한다. Firestore로 올리는 것은 cmd/regsync를 사람이
// 돌리는 별도 단계다. 런타임은 여기서 캐시해 읽는다.
// 콘솔이나 Firestore를 직접 수정하지 않는다.
package registry

import (
	"fmt"
	"regexp"
	"strings"

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

	// RequireAppCheck는 아직 아무도 읽지 않는다. 검증기가 없다.
	// true로 두어도 막히지 않으므로 보안 통제로 믿으면 안 된다.
	// 사정은 platformerr의 App Check 코드 주석에 적었다.
	RequireAppCheck bool `json:"require_app_check" firestore:"require_app_check"`

	GA4 GA4Config `json:"ga4" firestore:"ga4"`
	IAP IAPConfig `json:"iap" firestore:"iap"`

	// PlatformEventAllowlist에 없는 이벤트는 플랫폼으로 보내지 않는다.
	// 비용과 QPS를 규모와 무관한 상수로 묶는 장치다.
	PlatformEventAllowlist []string `json:"platform_event_allowlist" firestore:"platform_event_allowlist"`

	// CORSOrigins는 웹과 AIT 빌드용이다. 비어 있으면 CORS를 허용하지 않는다.
	CORSOrigins []string `json:"cors_origins" firestore:"cors_origins"`

	// BlockedUIDs는 남용 계정 차단용이다. 앱 전체를 멈추지 않고 개별 차단한다.
	BlockedUIDs []string `json:"blocked_uids" firestore:"blocked_uids"`
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
	Markets           []string          `json:"markets" firestore:"markets"`
	// GooglePlayPackageName은 RTDN packageName을 appId에 묶는 원장이다.
	// 환경변수나 알림 payload만으로 앱을 추측하지 않는다. ADR 0014.
	GooglePlayPackageName string `json:"google_play_package_name,omitempty" firestore:"google_play_package_name,omitempty"`
	// EntitlementIDs는 이 앱에 지급할 수 있는 entitlement allowlist다.
	// 전역 SKU 카탈로그는 상품 매핑의 원장이고, 이 목록은 앱 경계의 원장이다.
	EntitlementIDs []string `json:"entitlement_ids" firestore:"entitlement_ids"`
}

var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var entitlementIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var serviceAccountPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{5,29}@[a-z0-9][a-z0-9-]{5,29}\.iam\.gserviceaccount\.com$`)
var androidPackagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$`)

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
	if a.FeatureEnabled("iap") && a.MarketEnabled("google_play") {
		if !androidPackagePattern.MatchString(a.IAP.GooglePlayPackageName) ||
			isPlaceholder(a.IAP.GooglePlayPackageName) {
			return fmt.Errorf("%s: Google Play IAP에는 유효한 iap.google_play_package_name이 필요하다", a.AppID)
		}
	} else if a.IAP.GooglePlayPackageName != "" {
		return fmt.Errorf("%s: Google Play IAP가 비활성인데 package name이 설정됐다", a.AppID)
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
	// placeholder가 남은 채 배포되면 런타임에 이상하게 동작한다.
	// 부팅 시점에 잡는 편이 낫다.
	for _, v := range []string{a.AppID, a.DisplayName, a.FirebaseProjectID} {
		if isPlaceholder(v) {
			return fmt.Errorf("%s: placeholder가 남아 있다: %q", a.AppID, v)
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

// UIDBlocked는 계정이 차단됐는지 본다.
func (a App) UIDBlocked(uid string) bool {
	for _, b := range a.BlockedUIDs {
		if b == uid {
			return true
		}
	}
	return false
}

// EnsureUsable은 앱이 요청을 받을 수 있는 상태인지 확인한다.
func (a App) EnsureUsable() error {
	if a.Status == StatusPaused {
		return platformerr.Newf(platformerr.CodeAppPaused,
			"%s는 지금 점검 중이에요", a.DisplayName)
	}
	return nil
}
