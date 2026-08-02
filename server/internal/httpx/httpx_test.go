package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// TestMain은 로그를 버린다.
//
// 이 패키지는 에러 경로를 많이 테스트해서 로그가 출력을 뒤덮는다.
// 특히 패닉 복구 테스트의 스택 트레이스가 길다.
// 실패 원인이 로그에 묻히면 리뷰가 어려워진다.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func TestWriteOK(t *testing.T) {
	w := httptest.NewRecorder()
	WriteOK(w, http.StatusOK, map[string]string{"id": "pu_1"})

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	// 불변식 12. 결제 응답이 중간 캐시에 남으면 안 된다.
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("응답 파싱 실패: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("ok = %v, want true", body["ok"])
	}
	if _, has := body["error"]; has {
		t.Error("성공 응답에 error가 있다")
	}
}

func TestRequestLogPathNeverUsesRawPathParameters(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/admin/users/person@example.com", nil)
	r.Pattern = "GET /v1/admin/users/{reference}"
	if got := requestLogPath(r); got != r.Pattern || strings.Contains(got, "person@example.com") {
		t.Errorf("log path = %q", got)
	}

	unmatched := httptest.NewRequest(http.MethodGet, "/person@example.com", nil)
	if got := requestLogPath(unmatched); got != "<unmatched>" || strings.Contains(got, "person@example.com") {
		t.Errorf("unmatched log path = %q", got)
	}
}

func TestWriteError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			"플랫폼 에러는 코드와 상태를 유지한다",
			platformerr.New(platformerr.CodePurchaseOwnedByAnotherUser, "다른 계정의 구매예요"),
			http.StatusConflict,
			"purchase_owned_by_another_user",
		},
		{
			"일반 에러는 internal 500",
			errors.New("바닥 원인"),
			http.StatusInternalServerError,
			"internal",
		},
		{
			"감싼 플랫폼 에러도 찾는다",
			platformerr.Wrap(errors.New("원인"), platformerr.CodeAppPaused, "점검 중이에요"),
			http.StatusForbidden,
			"app_paused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/x", nil)

			WriteError(w, r, tt.err)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}

			var body struct {
				OK    bool `json:"ok"`
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("응답 파싱 실패: %v", err)
			}
			if body.OK {
				t.Error("ok = true, want false")
			}
			if body.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", body.Error.Code, tt.wantCode)
			}
			if body.Error.Message == "" {
				t.Error("message가 비어 있다")
			}
		})
	}
}

// 내부 원인이 응답에 새면 안 된다. 로그에만 남긴다.
func TestWriteErrorHidesCause(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/x", nil)

	secret := "내부구조체필드명_노출되면안됨"
	WriteError(w, r, platformerr.Wrap(errors.New(secret), platformerr.CodeInternal, "처리 중 문제가 생겼어요"))

	if strings.Contains(w.Body.String(), secret) {
		t.Errorf("원인이 응답에 노출됐다: %s", w.Body.String())
	}
}

func TestDecodeStrict(t *testing.T) {
	type payload struct {
		AppID string `json:"appId"`
	}

	newReq := func(body, contentType string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		return r
	}

	tests := []struct {
		name     string
		body     string
		ct       string
		wantCode platformerr.Code
	}{
		{"정상", `{"appId":"lizard-tycoon"}`, "application/json", ""},
		{"charset 포함 Content-Type 허용", `{"appId":"x"}`, "application/json; charset=utf-8", ""},
		{
			"미지 필드 거부 — 불변식 8",
			`{"appId":"x","uid":"주입시도"}`,
			"application/json",
			platformerr.CodeRequestInvalid,
		},
		{"Content-Type 없음", `{"appId":"x"}`, "", platformerr.CodeContentTypeInvalid},
		{"잘못된 Content-Type", `{"appId":"x"}`, "text/plain", platformerr.CodeContentTypeInvalid},
		{"깨진 JSON", `{"appId":`, "application/json", platformerr.CodeRequestInvalid},
		{"타입 불일치", `{"appId":123}`, "application/json", platformerr.CodeRequestInvalid},
		{"빈 본문", ``, "application/json", platformerr.CodeRequestInvalid},
		{
			"본문이 둘",
			`{"appId":"a"}{"appId":"b"}`,
			"application/json",
			platformerr.CodeRequestInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			var dst payload

			err := DecodeStrict(w, newReq(tt.body, tt.ct), &dst)

			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("예상치 못한 에러: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("에러를 기대했는데 nil이다")
			}
			if got := platformerr.CodeOf(err); got != tt.wantCode {
				t.Errorf("code = %q, want %q (%v)", got, tt.wantCode, err)
			}
		})
	}
}

func TestDecodeStrictRejectsOversizedBody(t *testing.T) {
	type payload struct {
		Note string `json:"note"`
	}

	big := strings.Repeat("a", MaxBodyBytes+1024)
	body := `{"note":"` + big + `"}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	var dst payload
	err := DecodeStrict(w, r, &dst)

	if err == nil {
		t.Fatal("크기 초과를 거부하지 않았다")
	}
	if got := platformerr.CodeOf(err); got != platformerr.CodeRequestTooLarge {
		t.Errorf("code = %q, want request_too_large", got)
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		want     string
		wantCode platformerr.Code
	}{
		{"정상", "Bearer abc.def.ghi", "abc.def.ghi", ""},
		{"소문자 스킴도 허용", "bearer abc", "abc", ""},
		{"헤더 없음", "", "", platformerr.CodeAuthRequired},
		{"스킴 없음", "abc.def", "", platformerr.CodeAuthInvalid},
		{"다른 스킴", "Basic abc", "", platformerr.CodeAuthInvalid},
		// 헤더가 존재하므로 required가 아니라 invalid다.
		// 클라이언트가 인증을 시도했지만 형식이 틀린 경우다.
		{"토큰 없음", "Bearer ", "", platformerr.CodeAuthInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
			if tt.header != "" {
				r.Header.Set("Authorization", tt.header)
			}

			got, err := BearerToken(r)

			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("예상치 못한 에러: %v", err)
				}
				if got != tt.want {
					t.Errorf("token = %q, want %q", got, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatal("에러를 기대했는데 nil이다")
			}
			if c := platformerr.CodeOf(err); c != tt.wantCode {
				t.Errorf("code = %q, want %q", c, tt.wantCode)
			}
		})
	}
}

func TestRecover(t *testing.T) {
	h := Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("의도된 패닉")
		}),
		Recover(),
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/x", nil)

	// 패닉이 여기까지 올라오면 테스트가 죽는다. 그게 실패 신호다.
	h.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Errorf("envelope 형식이 아니다: %s", w.Body.String())
	}
}

func TestWrapHandler(t *testing.T) {
	h := Wrap(func(http.ResponseWriter, *http.Request) error {
		return platformerr.New(platformerr.CodeRateLimited, "너무 잦아요")
	})

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/v1/x", nil))

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

// Chain은 앞에 쓴 미들웨어가 바깥이어야 한다.
func TestChainOrder(t *testing.T) {
	var order []string

	mw := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	h := Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			order = append(order, "handler")
		}),
		mw("first"), mw("second"),
	)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	want := []string{"first", "second", "handler"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}
