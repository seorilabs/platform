package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyAppID
)

// Middleware는 핸들러를 감싸는 함수다.
type Middleware func(http.Handler) http.Handler

// Chain은 미들웨어를 순서대로 적용한다.
//
// Chain(h, a, b, c)는 a(b(c(h)))가 된다.
// 즉 앞에 쓴 것이 바깥이고 요청을 먼저 본다.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Recover는 패닉을 500으로 바꾼다.
//
// 이게 없으면 패닉 하나가 프로세스를 죽이고 Cloud Run이 인스턴스를 교체한다.
// 그동안 진행 중이던 다른 요청도 함께 끊긴다.
func Recover() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// 스택을 남긴다. 패닉은 드물고 원인 파악이 어렵다.
				slog.ErrorContext(r.Context(), "패닉 복구",
					"panic", rec,
					"method", r.Method,
					"path", requestLogPath(r),
					"stack", string(debug.Stack()),
				)
				// 헤더를 이미 보냈으면 상태를 바꿀 수 없다.
				// 그래도 연결은 끊어야 클라이언트가 재시도한다.
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"internal","message":"처리 중 문제가 생겼어요"}}`))
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestID는 요청마다 식별자를 붙인다.
//
// Cloud Run이 X-Cloud-Trace-Context를 주므로 그걸 우선 쓴다.
// 로그를 요청 단위로 묶어 보려면 이 값이 필요하다.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Cloud-Trace-Context")
			if id == "" {
				id = r.Header.Get("X-Request-Id")
			}
			if id != "" {
				ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequestIDOf는 컨텍스트에서 요청 식별자를 꺼낸다.
func RequestIDOf(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

// WithAppID는 컨텍스트에 앱 식별자를 넣는다.
func WithAppID(ctx context.Context, appID string) context.Context {
	return context.WithValue(ctx, ctxKeyAppID, appID)
}

// AppIDOf는 컨텍스트에서 앱 식별자를 꺼낸다.
func AppIDOf(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyAppID).(string)
	return v
}

// statusRecorder는 응답 상태를 기록한다.
// http.ResponseWriter는 쓴 상태를 되읽을 방법을 주지 않는다.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// AccessLog는 요청을 구조화 JSON으로 남긴다.
//
// 토큰과 본문은 절대 남기지 않는다. AGENTS.md의 금지 항목이다.
// 경로와 상태, 지연만 남긴다.
func AccessLog() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}

			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}

			attrs := []any{
				"method", r.Method,
				"path", requestLogPath(r),
				"status", rec.status,
				"latency_ms", time.Since(start).Milliseconds(),
			}
			if id := RequestIDOf(r.Context()); id != "" {
				attrs = append(attrs, "trace", id)
			}
			if app := AppIDOf(r.Context()); app != "" {
				attrs = append(attrs, "app_id", app)
			}

			// 헬스체크는 warm-up ping이 5분마다 때리므로 로그가 시끄러워진다.
			if r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
				return
			}

			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			}
			slog.Log(r.Context(), level, "요청", attrs...)
		})
	}
}

// requestLogPath는 URL path parameter 원문 대신 ServeMux route pattern을
// 남긴다. 사용자 조회 reference에 이메일 같은 PII를 잘못 붙여 넣어도 access,
// error, panic 로그에 원문이 남아서는 안 된다.
func requestLogPath(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "<unmatched>"
}

// Timeout은 요청 컨텍스트에 상한을 건다.
//
// 외부 마켓 API가 응답하지 않을 때 인스턴스가 묶이는 걸 막는다.
// Cloud Run 자체 타임아웃보다 짧아야 우리가 제어권을 갖는다.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
