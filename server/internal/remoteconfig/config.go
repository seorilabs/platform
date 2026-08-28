// Package remoteconfig는 원격 설정을 제공한다.
//
// 공지를 2단계로 미뤘으므로 kill switch와 강제 업데이트, 점검 안내를
// 이게 맡는다. 세션 응답에 얹으면 추가 왕복이 0이다.
//
// Firebase RC가 못 하는 걸 한다. AIT와 Godot 런타임에서 동작하고,
// 크로스앱 공통 설정이 가능하며, IAP와 같은 서버 권위 신뢰 경계를 갖는다.
// Obsidian 프로젝트/platform/03-architecture/remote-config.md 참고.
package remoteconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SDKStatus는 클라이언트 SDK의 상태다.
//
// blocked가 마켓에 배포된 구버전 SDK를 서버에서 끄는 유일한 수단이다.
// 이게 없으면 한번 배포된 SDK를 영원히 지원해야 한다.
type SDKStatus struct {
	Status              string `json:"status" firestore:"status"` // ok | deprecated | blocked
	Message             string `json:"message,omitempty" firestore:"message"`
	UpdateURL           string `json:"updateUrl,omitempty" firestore:"update_url"`
	MinSupportedVersion string `json:"minSupportedVersion,omitempty" firestore:"min_supported_version"`
}

const (
	SDKStatusOK         = "ok"
	SDKStatusDeprecated = "deprecated"
	SDKStatusBlocked    = "blocked"
)

// Maintenance는 점검 모드다.
//
// BREAK-GLASS 절차가 이 값을 켠다. 본문 텍스트를 받지 않고
// 앱과 시간만 받으며 문구는 서버 상수다. 장애 중에 자유 텍스트 입력이나
// 외부 LLM 호출에 의존하면 안 된다.
type Maintenance struct {
	Active  bool      `json:"active" firestore:"active"`
	Message string    `json:"message,omitempty" firestore:"message"`
	Until   time.Time `json:"until,omitempty" firestore:"until"`
}

// MarshalJSON은 종료 시각이 없으면 필드를 생략한다.
//
// time.Time은 구조체라 omitempty가 동작하지 않는다. 그냥 두면
// 응답에 "until":"0001-01-01T00:00:00Z"가 나가고, 클라이언트가 이걸
// 유효한 시각으로 읽으면 점검이 이미 끝난 것으로 오해한다.
func (m Maintenance) MarshalJSON() ([]byte, error) {
	if m.Until.IsZero() {
		return json.Marshal(struct {
			Active  bool   `json:"active"`
			Message string `json:"message,omitempty"`
		}{m.Active, m.Message})
	}
	// alias로 감싸지 않으면 이 메서드가 다시 불려 무한 재귀가 된다.
	type alias Maintenance
	return json.Marshal(alias(m))
}

// Rule은 조건부 오버라이드다.
//
// 조건이 맞는 규칙의 값이 기본값 위에 덮인다. 순서대로 적용하므로
// 뒤에 오는 규칙이 이긴다.
type Rule struct {
	Platforms  []string `json:"platforms,omitempty" firestore:"platforms"`
	MinVersion string   `json:"minVersion,omitempty" firestore:"min_version"`
	MaxVersion string   `json:"maxVersion,omitempty" firestore:"max_version"`
	Locales    []string `json:"locales,omitempty" firestore:"locales"`

	Values   map[string]any  `json:"values,omitempty" firestore:"values"`
	Features map[string]bool `json:"features,omitempty" firestore:"features"`
}

// matches는 클라이언트가 이 규칙에 해당하는지 본다.
//
// 비어 있는 조건은 "전체"를 뜻한다. 모든 조건이 맞아야 매칭이다.
func (r Rule) matches(t Target) bool {
	if len(r.Platforms) > 0 && !containsFold(r.Platforms, t.Platform) {
		return false
	}
	if len(r.Locales) > 0 && !matchesLocale(r.Locales, t.Locale) {
		return false
	}
	return versionInRange(t.AppVersion, r.MinVersion, r.MaxVersion)
}

// Document는 Firestore에 저장되는 앱별 설정이다.
type Document struct {
	AppID string `json:"appId" firestore:"app_id"`

	Values   map[string]any  `json:"values" firestore:"values"`
	Features map[string]bool `json:"features" firestore:"features"`
	SDK      SDKStatus       `json:"sdk" firestore:"sdk"`
	Maint    Maintenance     `json:"maintenance" firestore:"maintenance"`
	Rules    []Rule          `json:"rules,omitempty" firestore:"rules"`

	// Version은 변경마다 올린다. ETag 계산에 쓴다.
	Version   int64     `json:"version" firestore:"version"`
	UpdatedAt time.Time `json:"updatedAt" firestore:"updated_at"`
	UpdatedBy string    `json:"updatedBy,omitempty" firestore:"updated_by"`
}

// Target은 설정을 요청한 클라이언트다.
type Target struct {
	Platform   string
	AppVersion string
	Locale     string
}

// Resolved는 타겟팅이 적용된 최종 설정이다.
type Resolved struct {
	Values   map[string]any  `json:"values"`
	Features map[string]bool `json:"features"`
	SDK      SDKStatus       `json:"sdk"`
	Maint    Maintenance     `json:"maintenance"`
}

// Resolve는 타겟에 맞는 설정을 계산한다.
//
// 기본값 위에 매칭되는 규칙을 순서대로 덮는다.
func (d Document) Resolve(t Target) Resolved {
	out := Resolved{
		Values:   copyAnyMap(d.Values),
		Features: copyBoolMap(d.Features),
		SDK:      d.SDK,
		Maint:    d.Maint,
	}
	if out.SDK.Status == "" {
		out.SDK.Status = SDKStatusOK
	}

	for _, r := range d.Rules {
		if !r.matches(t) {
			continue
		}
		for k, v := range r.Values {
			out.Values[k] = v
		}
		for k, v := range r.Features {
			out.Features[k] = v
		}
	}

	// 점검 종료 시각이 지났으면 자동으로 해제한다.
	// 운영자가 끄는 걸 잊어도 서비스가 계속 막히지 않게 한다.
	if out.Maint.Active && !out.Maint.Until.IsZero() && time.Now().After(out.Maint.Until) {
		out.Maint.Active = false
	}

	return out
}

// ETag는 설정 버전과 타겟으로 캐시 태그를 만든다.
//
// 같은 버전이라도 타겟이 다르면 결과가 다르므로 함께 넣는다.
func (d Document) ETag(t Target) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%d|%s|%s|%s", d.AppID, d.Version, t.Platform, t.AppVersion, t.Locale)
	return `W/"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`
}

// MarshalValues는 값 맵을 JSON으로 만든다. 관리 화면 미리보기용이다.
func (r Resolved) MarshalValues() (string, error) {
	b, err := json.Marshal(r.Values)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func copyAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyBoolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// matchesLocale은 로케일을 비교한다.
//
// "ko"는 "ko-KR"에도 매칭한다. 클라이언트가 지역까지 붙여 보내도
// 언어 단위 규칙이 동작해야 한다.
func matchesLocale(list []string, v string) bool {
	if v == "" {
		return false
	}
	lang, _, _ := strings.Cut(strings.ToLower(v), "-")
	for _, s := range list {
		s = strings.ToLower(s)
		if s == strings.ToLower(v) || s == lang {
			return true
		}
	}
	return false
}
