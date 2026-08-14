package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

type corsOriginAuthorizerStub struct {
	allowed bool
	err     error
	calls   int
}

func (s *corsOriginAuthorizerStub) AllowsCORSOrigin(context.Context, string) (bool, error) {
	s.calls++
	return s.allowed, s.err
}

func TestCORSAllowsAITPreflight(t *testing.T) {
	origins := &corsOriginAuthorizerStub{allowed: true}
	called := false
	handler := CORS(origins)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	r := httptest.NewRequest(http.MethodOptions, "/v1/auth/session", nil)
	r.Header.Set("Origin", "https://lizard-tycoon.private-apps.tossmini.com")
	r.Header.Set("Access-Control-Request-Method", http.MethodPost)
	r.Header.Set("Access-Control-Request-Headers", "content-type, x-seori-app")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if called {
		t.Fatal("preflight가 실제 핸들러까지 도달했다")
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != r.Header.Get("Origin") {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != corsAllowMethods {
		t.Errorf("Access-Control-Allow-Methods = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != corsAllowHeaders {
		t.Errorf("Access-Control-Allow-Headers = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want empty", got)
	}
}

func TestCORSAllowsActualRequestAndExposesETag(t *testing.T) {
	origins := &corsOriginAuthorizerStub{allowed: true}
	handler := CORS(origins)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	r.Header.Set("Origin", "https://lizard-tycoon.apps.tossmini.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != r.Header.Get("Origin") {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); got != "ETag" {
		t.Errorf("Access-Control-Expose-Headers = %q", got)
	}
}

func TestCORSRejectsUnregisteredOrigin(t *testing.T) {
	origins := &corsOriginAuthorizerStub{}
	called := false
	handler := CORS(origins)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	r := httptest.NewRequest(http.MethodOptions, "/v1/auth/session", nil)
	r.Header.Set("Origin", "https://attacker.example")
	r.Header.Set("Access-Control-Request-Method", http.MethodPost)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if called {
		t.Fatal("거부된 origin이 실제 핸들러까지 도달했다")
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

func TestCORSRejectsUnknownPreflightHeaders(t *testing.T) {
	origins := &corsOriginAuthorizerStub{allowed: true}
	handler := CORS(origins)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r := httptest.NewRequest(http.MethodOptions, "/v1/auth/session", nil)
	r.Header.Set("Origin", "https://lizard-tycoon.private-apps.tossmini.com")
	r.Header.Set("Access-Control-Request-Method", http.MethodPost)
	r.Header.Set("Access-Control-Request-Headers", "x-unexpected-header")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestCORSBypassesRequestsWithoutOrigin(t *testing.T) {
	origins := &corsOriginAuthorizerStub{err: errors.New("호출되면 안 됨")}
	handler := CORS(origins)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/auth/session", nil))

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	if origins.calls != 0 {
		t.Fatalf("origin authorizer calls = %d, want 0", origins.calls)
	}
}

func TestCORSFailsClosedWhenRegistryIsUnavailable(t *testing.T) {
	origins := &corsOriginAuthorizerStub{err: platformerr.New(
		platformerr.CodeConfigUnavailable, "앱 설정을 불러오지 못했어요")}
	handler := CORS(origins)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	r.Header.Set("Origin", "https://lizard-tycoon.apps.tossmini.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
