package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// MaxBodyBytes는 요청 본문 상한이다.
//
// 원본 lizard-tycoon의 MAX_REQUEST_BYTES와 같은 64KiB다.
// 이벤트 배치 25건이 들어가도 충분하고, 그 이상은 남용이다.
const MaxBodyBytes = 64 * 1024

// DecodeStrict는 요청 본문을 파싱한다.
//
// 허용되지 않은 필드가 하나라도 있으면 거부한다. 불변식 8이다.
// 요청에 uid나 entitlementId 같은 권한 결정 필드를 주입하는 걸 막는 게 목적이다.
// 서버가 그 값을 읽지 않더라도 거부한다 — 클라이언트가 그런 필드를 보낸다는 것
// 자체가 계약을 잘못 이해했다는 신호다.
//
// 미지 필드를 무시하는 건 응답 쪽 정책이다. 요청과 응답의 방향이 다르다.
// 서버는 구버전 클라이언트의 요청을 받아야 하지만, 그건 필드 추가가 아니라
// 필드 부재로 나타난다.
func DecodeStrict(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := requireJSONContentType(r); err != nil {
		return err
	}

	// MaxBytesReader가 상한을 넘는 순간 읽기를 끊는다.
	// io.ReadAll로 다 읽고 나서 길이를 재면 이미 메모리를 다 쓴 뒤다.
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}

	// 본문에 JSON이 둘 이상 들어 있으면 거부한다.
	// {"a":1}{"b":2} 같은 요청이 앞의 것만 읽히고 통과하면 안 된다.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return platformerr.New(platformerr.CodeRequestInvalid, "요청 본문이 하나가 아니에요")
	}
	return nil
}

func requireJSONContentType(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return platformerr.New(platformerr.CodeContentTypeInvalid, "Content-Type이 필요해요")
	}
	// "application/json; charset=utf-8" 형태를 허용한다.
	base, _, _ := strings.Cut(ct, ";")
	if strings.TrimSpace(base) != "application/json" {
		return platformerr.Newf(platformerr.CodeContentTypeInvalid,
			"application/json이 필요한데 %s가 왔어요", base)
	}
	return nil
}

// decodeError는 json 디코더 에러를 플랫폼 에러로 번역한다.
//
// 원인 문자열을 그대로 노출하지 않는다. 내부 구조체 필드명이 새면
// 공격자에게 스키마를 알려주는 셈이다. 다만 미지 필드는 예외로,
// 어느 필드가 문제인지 알려주는 편이 클라이언트 개발에 도움이 된다.
func decodeError(err error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return platformerr.Newf(platformerr.CodeRequestTooLarge,
			"요청이 너무 커요. %dKiB까지 보낼 수 있어요", MaxBodyBytes/1024)
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return platformerr.Newf(platformerr.CodeRequestInvalid,
			"JSON 형식이 올바르지 않아요 (%d번째 글자)", syntaxErr.Offset)
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return platformerr.Newf(platformerr.CodeRequestInvalid,
			"%s 필드의 타입이 올바르지 않아요", typeErr.Field)
	}

	if errors.Is(err, io.EOF) {
		return platformerr.New(platformerr.CodeRequestInvalid, "요청 본문이 비어 있어요")
	}

	// DisallowUnknownFields는 타입 에러가 아니라 문자열 에러로 온다.
	// 표준 라이브러리가 별도 타입을 주지 않아 문자열로 판정할 수밖에 없다.
	if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field ") {
		field := strings.TrimPrefix(msg, "json: unknown field ")
		return platformerr.Newf(platformerr.CodeRequestInvalid,
			"허용되지 않은 필드예요: %s", field)
	}

	return platformerr.Wrap(err, platformerr.CodeRequestInvalid, "요청을 해석할 수 없어요")
}

// Header는 필수 헤더를 읽는다. 없으면 에러다.
func Header(r *http.Request, name string, code platformerr.Code) (string, error) {
	v := strings.TrimSpace(r.Header.Get(name))
	if v == "" {
		return "", platformerr.Newf(code, "%s 헤더가 필요해요", name)
	}
	return v, nil
}

// BearerToken은 Authorization 헤더에서 Bearer 토큰을 꺼낸다.
func BearerToken(r *http.Request) (string, error) {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	if v == "" {
		return "", platformerr.New(platformerr.CodeAuthRequired, "인증이 필요해요")
	}

	scheme, token, found := strings.Cut(v, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", platformerr.New(platformerr.CodeAuthInvalid, "Bearer 토큰 형식이 아니에요")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", platformerr.New(platformerr.CodeAuthInvalid, "토큰이 비어 있어요")
	}
	return token, nil
}

// Handler는 에러를 돌려주는 핸들러다.
//
// 표준 http.HandlerFunc은 에러를 돌려주지 않아 핸들러마다 에러 처리를
// 반복하게 된다. 이 타입으로 감싸면 WriteError를 한 곳에서 부른다.
type Handler func(http.ResponseWriter, *http.Request) error

// Wrap은 Handler를 표준 핸들러로 만든다.
func Wrap(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, err)
		}
	}
}
