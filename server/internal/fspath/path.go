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

import "errors"

// Kind는 경로가 가리키는 대상이다.
type Kind int

const (
	KindInvalid Kind = iota
	KindCollection
	KindDocument
)

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
// 이 타입을 통해서만 경로를 만들면 prefix 누락을 컴파일 단계에서 줄일 수 있다.
// docs/03-architecture/iap.md의 "repository 레이어에서 타입으로 강제한다"가 이것이다.
type Path struct {
	segments []string
	kind     Kind
}

// Parse는 슬래시로 구분된 경로를 검증해 Path로 만든다.
//
// TODO(직접 구현): 아래를 만족해야 한다. path_test.go의 테이블이 정본이다.
//   - 빈 경로는 ErrEmptyPath
//   - 빈 세그먼트("a//b", "/a", "a/")는 ErrEmptySegment
//   - 세그먼트는 영숫자와 . _ - 만 허용. 그 외는 ErrInvalidSegment
//   - "." 과 ".." 은 ErrReservedSegment
//   - 세그먼트 수가 홀수면 KindCollection, 짝수면 KindDocument
func Parse(raw string) (Path, error) {
	return Path{}, errors.New("fspath: Parse가 아직 구현되지 않았다")
}

// String은 경로를 슬래시로 결합해 돌려준다.
//
// TODO(직접 구현)
func (p Path) String() string {
	return ""
}

// Kind는 컬렉션인지 문서인지 돌려준다.
//
// TODO(직접 구현)
func (p Path) Kind() Kind {
	return KindInvalid
}

// WithEnvPrefix는 staging 환경용 prefix를 첫 세그먼트에 적용한다.
//
//	users/pu_1  +  "stg_"  →  stg_users/pu_1
//
// prefix가 빈 문자열이면 원본을 그대로 돌려준다. production이 그 경우다.
// 이미 같은 prefix가 붙어 있으면 ErrPrefixNotAllowed를 돌려준다.
// 이중 적용은 조용히 잘못된 경로를 만들기 때문에 에러로 잡는다.
//
// TODO(직접 구현)
func (p Path) WithEnvPrefix(prefix string) (Path, error) {
	return Path{}, errors.New("fspath: WithEnvPrefix가 아직 구현되지 않았다")
}
