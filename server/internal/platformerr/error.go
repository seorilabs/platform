// Package platformerr는 플랫폼 전역 에러 타입을 제공한다.
//
// Error가 code와 HTTP status를 함께 들고 다닌다. 원본 lizard-tycoon의
// IapError와 같은 모양이다. 도메인이 HTTP를 안다는 점이 순수하지 않지만,
// 코드가 60개 넘어 대응표를 한 곳에 모으는 편이 누락을 잡기 쉽다.
// ADR 0009 이전 결정이며 Obsidian 프로젝트/platform/03-architecture/server-layout.md 참고.
//
// 판정은 errors.Is가 아니라 CodeOf를 쓴다. 코드 비교가 실제 의도이기 때문이다.
//
//	if platformerr.CodeOf(err) == platformerr.CodePurchaseNotFound { ... }
package platformerr

import (
	"errors"
	"fmt"
)

// Error는 플랫폼 에러다.
//
// Message는 사용자에게 보이는 한국어 문구다.
// 클라이언트가 이 문자열로 분기하면 안 된다. Code로 분기한다.
type Error struct {
	Code    Code
	Message string
	Status  int

	// err은 원인이다. errors.Is와 errors.As가 체인을 따라갈 수 있게 한다.
	err error
}

func (e *Error) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap은 원인을 돌려준다. errors.Is와 errors.As가 이걸 쓴다.
func (e *Error) Unwrap() error { return e.err }

// Is는 같은 코드를 가진 Error끼리 같다고 본다.
//
// errors.Is(err, platformerr.New(CodeX, "")) 형태를 지원하려고 두었다.
// 다만 일반적인 판정은 CodeOf를 쓰는 편이 읽기 좋다.
func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return e.Code == t.Code
}

// New는 새 에러를 만든다. 상태는 코드에서 자동으로 정해진다.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message, Status: statusOf(code)}
}

// Newf는 포맷 문자열로 메시지를 만든다.
func Newf(code Code, format string, args ...any) *Error {
	return New(code, fmt.Sprintf(format, args...))
}

// Wrap은 원인을 감싼 에러를 만든다.
//
// 이미 *Error인 원인을 감싸면 바깥 코드가 우선한다.
// 내부 세부를 상위 계층 언어로 번역하는 게 목적이기 때문이다.
func Wrap(err error, code Code, message string) *Error {
	return &Error{Code: code, Message: message, Status: statusOf(code), err: err}
}

// Wrapf는 포맷 문자열로 메시지를 만들어 감싼다.
func Wrapf(err error, code Code, format string, args ...any) *Error {
	return Wrap(err, code, fmt.Sprintf(format, args...))
}

// WithStatus는 상태를 덮어쓴 사본을 돌려준다.
//
// 같은 코드가 상황에 따라 다른 상태를 갖는 경우에만 쓴다.
// platform_mismatch가 그 예로, 클라이언트 실수면 400이지만
// 서버 조립 실수면 500이다.
//
// 원본을 수정하지 않고 사본을 돌려준다. sentinel 스타일로 공유된
// 에러 값을 호출자가 오염시키는 걸 막는다.
func (e *Error) WithStatus(status int) *Error {
	c := *e
	c.Status = status
	return &c
}

// CodeOf는 에러 체인에서 플랫폼 에러 코드를 뽑는다.
//
// 체인에 *Error가 없으면 CodeInternal을 돌려준다.
// nil이면 빈 코드를 돌려준다. 성공을 실패로 오해하지 않게 하기 위해서다.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return CodeInternal
}

// StatusOf는 에러 체인에서 HTTP 상태를 뽑는다.
// 체인에 *Error가 없으면 500이다.
func StatusOf(err error) int {
	if err == nil {
		return 0
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Status
	}
	return statusOf(CodeInternal)
}

// As는 에러 체인에서 *Error를 꺼낸다.
//
// 체인에 없으면 CodeInternal 에러를 새로 만들어 돌려준다.
// httpx가 어떤 에러든 envelope으로 만들 수 있어야 하기 때문이다.
// 두 번째 반환값은 원래 플랫폼 에러였는지를 알려준다.
func As(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return Wrap(err, CodeInternal, "처리 중 문제가 생겼어요"), false
}

// IsRetryableErr는 에러가 재시도 가능한지 판정한다.
func IsRetryableErr(err error) bool {
	return IsRetryable(CodeOf(err))
}
