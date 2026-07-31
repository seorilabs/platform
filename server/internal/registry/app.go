// Package registry는 앱 레지스트리를 로드하고 캐시한다.
//
// 레지스트리의 source of truth는 repo의 registry/apps/*.json이다.
// CI가 스키마를 검증해 Firestore로 upsert하고, 런타임은 여기서 캐시해 읽는다.
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
	Status            Status `json:"status" firestore:"status"`

	Features        map[string]bool `json:"features" firestore:"features"`
	RequireAppCheck bool            `json:"require_app_check" firestore:"require_app_check"`

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
}

var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Validate는 레지스트리 항목을 검증한다.
//
// CI가 Firestore로 올리기 전에 부르고, 런타임 로드에서도 부른다.
// 잘못된 항목이 하나 있어도 나머지는 살아야 하므로 항목 단위로 판정한다.
func (a App) Validate() error {
	if !appIDPattern.MatchString(a.AppID) {
		return fmt.Errorf("app_id가 형식에 맞지 않는다: %q", a.AppID)
	}
	if a.FirebaseProjectID == "" {
		return fmt.Errorf("%s: firebase_project_id가 필요하다", a.AppID)
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
