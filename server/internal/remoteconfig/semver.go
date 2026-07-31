package remoteconfig

import (
	"strconv"
	"strings"
)

// version은 파싱된 stable SemVer다.
type version struct {
	major, minor, patch int
	valid               bool
}

// parseVersion은 "v1.2.3" 또는 "1.2.3"을 파싱한다.
//
// 조직의 모든 릴리스가 stable SemVer 태그다. prerelease는 쓰지 않으므로
// 여기서도 다루지 않는다. "1.2.3-beta" 같은 값이 오면 하이픈 앞부분만 본다.
func parseVersion(s string) version {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	// prerelease와 빌드 메타데이터를 잘라낸다.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return version{}
	}

	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return version{}
	}

	v := version{valid: true}
	dst := []*int{&v.major, &v.minor, &v.patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}
		}
		*dst[i] = n
	}
	return v
}

// compare는 a와 b를 비교한다. a < b면 음수, 같으면 0, a > b면 양수다.
func (a version) compare(b version) int {
	switch {
	case a.major != b.major:
		return a.major - b.major
	case a.minor != b.minor:
		return a.minor - b.minor
	default:
		return a.patch - b.patch
	}
}

// versionInRange는 버전이 [min, max] 범위에 드는지 본다.
//
// min이나 max가 비어 있으면 그쪽 경계는 없다.
// 클라이언트 버전이 파싱 불가면 범위 조건이 있는 규칙에 매칭하지 않는다.
// 알 수 없는 버전에 조건부 설정을 적용하면 예측할 수 없는 동작이 된다.
func versionInRange(clientVer, min, max string) bool {
	if min == "" && max == "" {
		return true
	}

	cv := parseVersion(clientVer)
	if !cv.valid {
		return false
	}

	if min != "" {
		mv := parseVersion(min)
		if !mv.valid || cv.compare(mv) < 0 {
			return false
		}
	}
	if max != "" {
		xv := parseVersion(max)
		if !xv.valid || cv.compare(xv) > 0 {
			return false
		}
	}
	return true
}
