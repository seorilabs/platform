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
	// RecommendedVersion은 유도할 목표 버전이다. 권장과 강제 화면 양쪽에서
	// "최신 1.5.0" 같은 표시에 쓴다.
	//
	// 저장하지 않는다. 관측에서 매번 계산하므로 문서에 굳으면 낡은다.
	RecommendedVersion string `json:"recommendedVersion,omitempty" firestore:"-"`
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

// 강제 업데이트를 걸 수 있는 플랫폼과 클라이언트가 스스로 밝히는 플랫폼이다.
//
// ait과 web에는 정책을 걸지 않는다. 미니앱 번들은 토스가 전달하고 웹은 새로고침이라
// 유저가 "설치본을 업데이트"할 대상 자체가 없다.
const (
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
	PlatformWeb     = "web"
	PlatformAIT     = "ait"
)

// NormalizeClientPlatform은 아는 플랫폼이면 정규화하고 아니면 빈 문자열을 준다.
//
// 모르는 값을 추측해서 android로 넣지 않는다. 엉뚱한 런타임이 안드로이드
// 정책에 걸린다.
func NormalizeClientPlatform(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case PlatformAndroid:
		return PlatformAndroid
	case PlatformIOS:
		return PlatformIOS
	case PlatformWeb:
		return PlatformWeb
	case PlatformAIT:
		return PlatformAIT
	}
	return ""
}

// PlatformFromRuntime은 X-Seori-Runtime에서 플랫폼을 뽑는다.
//
// 토큰 단위로 본다. 부분 문자열로 보면 "godot-web-ait"의 web과 ait이 검사
// 순서에 따라 뒤바뀐다. AIT WebView 안에서 도는 Godot Web 빌드를 web으로
// 보면 안 되므로 ait을 먼저 본다.
func PlatformFromRuntime(runtime string) string {
	tokens := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(
		strings.ToLower(strings.TrimSpace(runtime)),
		func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == '/' },
	) {
		tokens[token] = struct{}{}
	}
	for _, candidate := range []struct{ token, platform string }{
		{"ait", PlatformAIT},
		{"toss", PlatformAIT},
		{"android", PlatformAndroid},
		{"ios", PlatformIOS},
		{"web", PlatformWeb},
	} {
		if _, ok := tokens[candidate.token]; ok {
			return candidate.platform
		}
	}
	return ""
}

// 정책이 이겼을 때 쓰는 문구다.
//
// 요청으로 받지 않는 이유는 setMaintenance와 같다. 조작 화면에서 자유 텍스트를
// 받으면 앱마다 말투가 갈리고 오타가 그대로 유저에게 나간다.
const (
	defaultBlockedMessage    = "업데이트가 필요해요. 스토어에서 최신 버전을 받아 주세요"
	defaultDeprecatedMessage = "새 버전이 나왔어요. 업데이트하면 더 편하게 쓸 수 있어요"
)

// BlockedVersion은 강제 업데이트 대상 한 건이다.
type BlockedVersion struct {
	Version string `json:"version" firestore:"version"`
	// BlockedAt은 목록에 넣은 시각이다. 감사와 롤백 판단에 쓴다.
	BlockedAt time.Time `json:"blockedAt,omitempty" firestore:"blocked_at,omitempty"`
}

// PlatformUpdatePolicy는 한 플랫폼의 업데이트 유도 정책이다.
//
// 권장과 강제가 비대칭인 게 의도다. 권장은 임계값이라 릴리스마다 손댈 필요가
// 없고, 강제는 열거라 명시하지 않은 버전을 실수로 막을 방법이 없다.
//
// 강제를 임계값("X 미만 차단")으로 두지 않은 이유가 하나 더 있다. Rule의 상한은
// MaxVersion "이하"라 "2.0.0 미만"을 정확히 쓸 수 없다. maxVersion을 1.9.9로
// 쓰면 1.9.10이 빠져나간다. 운영자가 경계를 손으로 계산하게 두면 언젠가 틀리고,
// 그 틀림의 대가가 유저 브릭이다.
type PlatformUpdatePolicy struct {
	// BlockedVersions에 적힌 버전만 강제한다. 비어 있으면 아무도 막히지 않는다.
	BlockedVersions []BlockedVersion `json:"blockedVersions,omitempty" firestore:"blocked_versions"`
	// RecommendOverride가 비어 있으면 관측된 최신 안정 버전을 자동 추종한다.
	// 자동 추종이 이상값을 잡았을 때의 탈출구이지 평상시 운영 수단이 아니다.
	RecommendOverride string `json:"recommendOverride,omitempty" firestore:"recommend_override"`
}

// blocks는 이 버전이 강제 대상인지 본다.
func (p PlatformUpdatePolicy) blocks(version string) bool {
	for _, blocked := range p.BlockedVersions {
		if SameVersion(version, blocked.Version) {
			return true
		}
	}
	return false
}

// UpdatePolicy는 플랫폼별 업데이트 유도 정책이다.
type UpdatePolicy struct {
	Platforms map[string]PlatformUpdatePolicy `json:"platforms,omitempty" firestore:"platforms"`
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
	// Update는 업데이트 유도 정책이다. SDK와 별도인 이유는 SDK가 break-glass
	// 수동 kill switch고 이쪽이 평상시 운영 경로이기 때문이다.
	Update UpdatePolicy `json:"update,omitempty" firestore:"update"`

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

// sdkSeverity는 상태의 엄격함이다. 알 수 없는 값은 0이다.
//
// 클라이언트도 모르는 status를 ok로 읽으므로 서버가 같은 눈금을 쓴다.
func sdkSeverity(status string) int {
	switch status {
	case SDKStatusBlocked:
		return 2
	case SDKStatusDeprecated:
		return 1
	}
	return 0
}

// resolveSDK는 서버가 클라이언트 대신 버전을 판정한다.
//
// 클라이언트가 semver를 비교하지 않는 이유는 둘이다. SDK 없이 raw HTTP로
// 붙는 앱도 그냥 동작해야 하고, 같은 비교 구현을 TS와 GDScript에 두 벌 두면
// 언젠가 갈라진다.
//
// 더 엄격한 쪽이 이긴다. 수동 kill switch를 정책이 완화하지 못하고, 낡은
// 수동 ok가 정책의 차단을 풀지 못한다.
func (d Document) resolveSDK(t Target, recommend string) SDKStatus {
	out := d.SDK
	if out.Status == "" {
		out.Status = SDKStatusOK
	}

	policy, ok := d.Update.Platforms[NormalizeClientPlatform(t.Platform)]
	if !ok {
		return out
	}
	if policy.RecommendOverride != "" {
		recommend = policy.RecommendOverride
	}
	// 판정과 무관하게 목표 버전은 알려 준다. CS와 로그에 필요하다.
	out.RecommendedVersion = recommend

	// 버전을 모르면 절대 판정하지 않는다.
	//
	// X-Seori-AppVer를 보내지 않는 구버전과 raw HTTP 앱이 전부 여기로 온다.
	// 이 한 줄이 없으면 헤더를 안 보내는 클라이언트가 전부 차단된다.
	if !ValidStableVersion(t.AppVersion) {
		return out
	}

	status := SDKStatusOK
	switch {
	case policy.blocks(t.AppVersion):
		status = SDKStatusBlocked
	case recommend != "" && VersionLess(t.AppVersion, recommend):
		status = SDKStatusDeprecated
	}
	if sdkSeverity(status) <= sdkSeverity(out.Status) {
		return out
	}

	out.Status = status
	// 수동 문구는 수동 status를 설명하는 말이다. 정책이 더 엄격해서 이겼다면
	// 그 문구는 지금 상태와 다른 이야기이므로 덮는다.
	if status == SDKStatusBlocked {
		out.Message = defaultBlockedMessage
	} else {
		out.Message = defaultDeprecatedMessage
	}
	return out
}

// Resolve는 타겟에 맞는 설정을 계산한다.
//
// 기본값 위에 매칭되는 규칙을 순서대로 덮는다.
//
// recommend는 자동 추종으로 구한 권장 기준 버전이다. 문서 안에 없는 이유는
// app_versions 관측에서 오기 때문이다. 빈 값이면 정책의 override만 쓴다.
func (d Document) Resolve(t Target, recommend string) Resolved {
	out := Resolved{
		Values:   copyAnyMap(d.Values),
		Features: copyBoolMap(d.Features),
		SDK:      d.resolveSDK(t, recommend),
		Maint:    d.Maint,
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
//
// salt는 문서 밖에서 응답을 바꾸는 입력이다. 스토어 주소는 레지스트리에 있어
// 문서 version이 안 올라가는데, 그것만 바뀌면 304를 받은 클라이언트가 영영
// 옛 주소를 쥔다. 가변 인자라 기존 호출부가 그대로 컴파일된다.
func (d Document) ETag(t Target, salt ...string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%d|%s|%s|%s", d.AppID, d.Version, t.Platform, t.AppVersion, t.Locale)
	for _, s := range salt {
		_, _ = fmt.Fprintf(h, "|%s", s)
	}
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
