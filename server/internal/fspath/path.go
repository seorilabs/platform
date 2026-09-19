// Package fspath는 Firestore 경로를 파싱하고 환경 prefix를 적용한다.
//
// Firestore 경로는 세그먼트가 홀수면 컬렉션, 짝수면 문서다.
//
//	users                              → 컬렉션 (1)
//	users/pu_01JXYZ                    → 문서   (2)
//	users/pu_01JXYZ/entitlements       → 컬렉션 (3)
//
// 환경 prefix는 staging이 production과 같은 (default) 데이터베이스를
// 공유하기 때문에 필요하다. ADR 0003 참고.
package fspath

import (
	"errors"
	"strings"
)

// Kind는 경로가 가리키는 대상이다.
type Kind int

const (
	KindInvalid Kind = iota
	KindCollection
	KindDocument
)

func (k Kind) String() string {
	switch k {
	case KindCollection:
		return "collection"
	case KindDocument:
		return "document"
	default:
		return "invalid"
	}
}

// 에러는 sentinel로 정의한다. 호출자가 errors.Is로 판정할 수 있어야 한다.
// 문자열 비교로 에러를 판정하는 코드를 만들지 않기 위한 Go 관용구다.
var (
	ErrEmptyPath        = errors.New("fspath: 경로가 비어 있다")
	ErrEmptySegment     = errors.New("fspath: 빈 세그먼트가 있다")
	ErrInvalidSegment   = errors.New("fspath: 세그먼트에 허용되지 않는 문자가 있다")
	ErrReservedSegment  = errors.New("fspath: 예약된 세그먼트 이름이다")
	ErrPrefixNotAllowed = errors.New("fspath: 이미 prefix가 적용된 경로다")
)

// Path는 검증을 통과한 Firestore 경로다.
//
// 이 타입을 통해서만 경로를 만들면 prefix 누락을 구조적으로 줄일 수 있다.
// store 패키지의 Doc과 Collection이 string이 아니라 Path를 받는 이유가 이것이다.
// Obsidian 프로젝트/platform/03-architecture/server-layout.md 참고.
type Path struct {
	segments []string
	kind     Kind
}

// Parse는 슬래시로 구분된 경로를 검증해 Path로 만든다.
func Parse(raw string) (Path, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Path{}, ErrEmptyPath
	}

	segments := strings.Split(trimmed, "/")
	for _, seg := range segments {
		if seg == "" {
			// "/users", "users/", "users//pu_1" 이 전부 여기로 온다.
			return Path{}, ErrEmptySegment
		}
		// reserved 검사가 문자 검사보다 먼저다.
		// "." 과 ".." 은 허용 문자로만 이루어져 있어 순서가 바뀌면 통과해버린다.
		if seg == "." || seg == ".." {
			return Path{}, ErrReservedSegment
		}
		if !isValidSegment(seg) {
			return Path{}, ErrInvalidSegment
		}
	}

	kind := KindDocument
	if len(segments)%2 == 1 {
		kind = KindCollection
	}

	return Path{segments: segments, kind: kind}, nil
}

// String은 경로를 슬래시로 결합해 돌려준다.
func (p Path) String() string {
	return strings.Join(p.segments, "/")
}

// Kind는 컬렉션인지 문서인지 돌려준다.
func (p Path) Kind() Kind {
	return p.kind
}

// WithEnvPrefix는 staging 환경용 prefix를 첫 세그먼트에 적용한다.
//
//	users/pu_1  +  "stg_"  →  stg_users/pu_1
//
// prefix가 빈 문자열이면 원본을 그대로 돌려준다. production이 그 경우다.
// 이미 같은 prefix가 붙어 있으면 ErrPrefixNotAllowed를 돌려준다.
// 이중 적용은 조용히 잘못된 경로를 만들기 때문에 에러로 잡는다.
func (p Path) WithEnvPrefix(prefix string) (Path, error) {
	if prefix == "" {
		return p, nil
	}
	if len(p.segments) == 0 {
		return Path{}, ErrEmptyPath
	}
	if strings.HasPrefix(p.segments[0], prefix) {
		return Path{}, ErrPrefixNotAllowed
	}

	// 슬라이스를 복사한다. Go 슬라이스는 참조 의미론이라
	// p.segments를 직접 수정하면 호출자가 들고 있는 원본 Path까지 바뀐다.
	// Path가 값 타입이라 안전해 보이지만 내부 슬라이스는 공유된다.
	segments := make([]string, len(p.segments))
	copy(segments, p.segments)
	segments[0] = prefix + segments[0]

	// 세그먼트 수가 그대로이므로 kind도 그대로다.
	return Path{segments: segments, kind: p.kind}, nil
}

// isValidSegment는 영숫자와 . _ - 만 허용한다.
//
// range로 문자열을 순회하면 룬 단위로 읽는다.
// 한글 같은 멀티바이트 문자는 자연스럽게 default로 걸린다.
func isValidSegment(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}
