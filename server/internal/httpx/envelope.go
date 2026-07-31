// Package httpx는 HTTP 전송 계층의 공통 부분을 담는다.
//
// 응답 envelope, 요청 파싱, 미들웨어가 여기 있다.
// 라우팅은 표준 net/http의 ServeMux를 쓴다. Go 1.22+ 패턴 라우팅이
// "POST /v1/inbox/{id}/claim"을 지원하므로 외부 라우터를 도입하지 않는다.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// envelope는 모든 응답의 형태다. 불변식 12다.
//
//	성공  {"ok": true,  "result": {...}}
//	실패  {"ok": false, "error": {"code": "...", "message": "..."}}
//
// 클라이언트가 HTTP status와 ok의 정합성을 검사하므로 둘이 어긋나면 안 된다.
type envelope struct {
	OK     bool       `json:"ok"`
	Result any        `json:"result,omitempty"`
	Error  *errorBody `json:"error,omitempty"`
}

type errorBody struct {
	Code    platformerr.Code `json:"code"`
	Message string           `json:"message"`
}

// WriteOK는 성공 응답을 쓴다.
func WriteOK(w http.ResponseWriter, status int, result any) {
	writeJSON(w, status, envelope{OK: true, Result: result})
}

// WriteError는 실패 응답을 쓴다.
//
// 어떤 에러든 envelope으로 만든다. 플랫폼 에러가 아니면 internal 500이 된다.
// 원인은 로그에만 남기고 응답에는 넣지 않는다. 내부 구조가 새면 안 된다.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	pe, known := platformerr.As(err)

	if !known || pe.Status >= 500 {
		// 5xx와 정체불명 에러는 원인을 남긴다. 4xx는 클라이언트 실수라 시끄럽기만 하다.
		slog.ErrorContext(r.Context(), "요청 처리 실패",
			"code", pe.Code,
			"status", pe.Status,
			"method", r.Method,
			"path", r.URL.Path,
			"err", err.Error(),
		)
	}

	writeJSON(w, pe.Status, envelope{
		OK:    false,
		Error: &errorBody{Code: pe.Code, Message: pe.Message},
	})
}

func writeJSON(w http.ResponseWriter, status int, body envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// 불변식 12. 결제 응답이 중간 캐시에 남으면 안 된다.
	//
	// 기본은 no-store이고, 핸들러가 미리 설정했으면 그걸 존중한다.
	// RemoteConfig처럼 캐시가 이득인 경로만 명시적으로 바꾼다.
	// 기본값이 안전한 쪽이므로 깜빡해도 사고가 나지 않는다.
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 헤더를 이미 보냈으므로 상태를 바꿀 수 없다. 로그만 남긴다.
		slog.Error("응답 인코딩 실패", "err", err)
	}
}
