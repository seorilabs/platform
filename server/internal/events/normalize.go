// Package events는 이벤트를 수집해 BigQuery에 적재한다.
//
// GA4를 대체하지 않는다. SDK가 단일 진입점이 되어 GA4와 플랫폼 양쪽으로
// 팬아웃하고, 플랫폼은 allowlist에 있는 것만 받는다.
// Obsidian 프로젝트/platform/03-architecture/events.md 참고.
package events

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// 정규화 규약. spec/conformance/param-normalization.json이 정본이다.
//
// 이 값들이 바뀌면 TS SDK와 GDScript SDK도 함께 바뀌어야 한다.
// 서버가 재정규화하는 이유는 클라이언트를 신뢰할 수 없기 때문이다.
// SDK가 이미 했으면 no-op이고, 안 했거나 조작됐으면 여기서 정리된다.
const (
	MaxParamCount    = 25
	MaxParamNameLen  = 40
	MaxParamValueLen = 100
	MaxEventNameLen  = 40
)

// piiKeys는 저장하면 안 되는 파라미터 이름이다.
//
// 개발자가 무심코 log_event("login", {email}) 하는 게 실제로 가장 흔한 사고다.
// SDK가 먼저 걸러내지만 서버도 막는다. ADR 0005 참고.
var piiKeys = map[string]bool{
	"email": true, "e_mail": true, "mail": true,
	"phone": true, "phone_number": true, "tel": true, "mobile": true,
	"name": true, "full_name": true, "first_name": true, "last_name": true, "real_name": true,
	"address": true, "addr": true, "zipcode": true, "postal_code": true,
	"birth": true, "birthday": true, "birthdate": true,
	"ssn": true, "passport": true,
	"card_number": true, "credit_card": true,
	"ip": true, "ip_address": true,
}

// IsPIIKey는 파라미터 이름이 PII 목록에 있는지 본다. 대소문자를 구분하지 않는다.
func IsPIIKey(name string) bool {
	return piiKeys[strings.ToLower(strings.TrimSpace(name))]
}

// NormalizeParams는 이벤트 파라미터를 정규화한다.
//
// 규칙은 spec/conformance/param-normalization.json과 같다.
//   - boolean은 1 또는 0
//   - 유한한 숫자는 그대로, NaN과 Infinity는 0
//   - null은 파라미터 자체를 제거
//   - 문자열은 100자로 자름
//   - 이름이 40자를 넘으면 제거
//   - 객체와 배열은 제거. 조용히 문자열로 바꾸지 않는다
//   - PII 키는 제거
//   - 25개를 넘으면 키 오름차순 앞 25개만 남김
//
// 제거 대상은 25개 상한 계산 전에 먼저 빠진다.
func NormalizeParams(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}

	kept := make(map[string]any, len(in))

	for k, v := range in {
		if len(k) > MaxParamNameLen {
			continue
		}
		if IsPIIKey(k) {
			continue
		}
		nv, ok := normalizeValue(v)
		if !ok {
			continue
		}
		kept[k] = nv
	}

	if len(kept) <= MaxParamCount {
		return kept
	}

	// 상한을 넘으면 키 오름차순으로 자른다.
	// 무작위로 자르면 같은 입력이 실행마다 다른 결과를 내 재현이 안 된다.
	keys := make([]string, 0, len(kept))
	for k := range kept {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]any, MaxParamCount)
	for _, k := range keys[:MaxParamCount] {
		out[k] = kept[k]
	}
	return out
}

// normalizeValue는 값 하나를 정규화한다. 두 번째 반환값이 false면 제거 대상이다.
func normalizeValue(v any) (any, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false

	case bool:
		// GA4에서 숫자는 SUM과 AVG가 되지만 문자열은 안 된다.
		// happy-farm의 toScalar가 이미 이 형식으로 쌓고 있어 맞춘다.
		if t {
			return int64(1), true
		}
		return int64(0), true

	case string:
		return truncateRunes(t, MaxParamValueLen), true

	case float64:
		// JSON 숫자는 전부 float64로 들어온다.
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return int64(0), true
		}
		// 정수로 떨어지면 정수로 저장한다. BigQuery에서 다루기 편하다.
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t), true
		}
		return t, true

	case float32:
		return normalizeValue(float64(t))

	case int:
		return int64(t), true
	case int32:
		return int64(t), true
	case int64:
		return t, true

	default:
		// 객체와 배열은 제거한다.
		// 조용히 String()으로 바꾸면 나중에 파싱 불가능한 값이 쌓인다.
		// 이 조직에서 실제로 문제를 냈던 방식이라 명시적으로 버린다.
		return nil, false
	}
}

// truncateRunes는 문자열을 룬 단위로 자른다.
//
// 바이트로 자르면 멀티바이트 문자 중간이 잘려 깨진 문자가 남는다.
// 이모지는 서로게이트 쌍이라 특히 위험하다.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	count := 0
	for i := range s {
		if count == max {
			return s[:i]
		}
		count++
	}
	return s
}

// NormalizeEventName은 이벤트 이름을 정규화한다.
//
// 빈 문자열이나 40자 초과는 거부한다.
func NormalizeEventName(name string) (string, bool) {
	n := strings.TrimSpace(name)
	if n == "" || len(n) > MaxEventNameLen {
		return "", false
	}
	return n, true
}
