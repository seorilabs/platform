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

// ValidStableVersion은 안정 SemVer로 해석되는지 본다.
//
// 정책에 넣을 수 있는 버전을 admin이 검사할 때 쓴다. 정규식을 admin에
// 복제하면 두 곳의 해석이 갈린다.
func ValidStableVersion(s string) bool { return parseVersion(s).valid }

// SameVersion은 두 문자열이 같은 빌드를 가리키는지 본다.
//
// 원문을 비교하지 않는다. "1.4"와 "1.4.0"과 "v1.4.0"은 같은 빌드이고,
// 앱마다 접두사 관례가 달라 원문으로 비교하면 차단이 조용히 빗나간다.
func SameVersion(a, b string) bool {
	av, bv := parseVersion(a), parseVersion(b)
	return av.valid && bv.valid && av.compare(bv) == 0
}

// VersionLess는 a가 b보다 낮은지 본다.
//
// 어느 쪽이든 해석 불가면 false다. 모르는 버전을 낮다고 보면 그 클라이언트가
// 전부 안내 대상이 된다.
func VersionLess(a, b string) bool {
	av, bv := parseVersion(a), parseVersion(b)
	return av.valid && bv.valid && av.compare(bv) < 0
}

// HighestVersion은 안정 SemVer 중 가장 높은 원문을 돌려준다.
//
// 해석 불가한 값은 후보에서 빠진다. 없으면 빈 문자열이고, 그때는 아무
// 판정도 하지 않는다.
func HighestVersion(versions []string) string {
	best := ""
	bestParsed := version{}
	for _, candidate := range versions {
		parsed := parseVersion(candidate)
		if !parsed.valid {
			continue
		}
		if best == "" || parsed.compare(bestParsed) > 0 {
			best, bestParsed = candidate, parsed
		}
	}
	return best
}
