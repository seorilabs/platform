// Command platform은 Seorilabs 공통 플랫폼 서버다.
//
// 단일 바이너리이고 PLATFORM_ROLE 환경변수로 역할을 나눈다.
// 코드베이스는 하나이고 네트워크 경계가 없다. 마이크로서비스가 아니다.
//
// 역할을 나누는 이유는 셋이며 전부 실질적이다.
//   - IAM 폭발 반경: platform-iap 만 마켓 자격증명을 갖는다
//   - 비용 격벽: ingest 폭주가 max-instances 를 다 먹어 결제를 죽이면 안 된다
//   - 동시성 튜닝이 정반대: ingest 는 I/O 바운드 write-only, api 는 캐시 + 읽기
//
// docs/03-architecture/overview.md 참고.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Role은 이 프로세스가 담당하는 역할이다.
type Role string

const (
	RoleAPI    Role = "api"    // session, RemoteConfig, entitlement 조회
	RoleIAP    Role = "iap"    // 마켓 검증과 웹훅. 자격증명 보유
	RoleIngest Role = "ingest" // 이벤트 수집. 고QPS
	RoleAdmin  Role = "admin"  // 백오피스 전용. private
	RoleWorker Role = "worker" // 완료 outbox 재시도. Cloud Run Job
)

func parseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleAPI, RoleIAP, RoleIngest, RoleAdmin, RoleWorker:
		return Role(s), nil
	case "":
		return "", errors.New("PLATFORM_ROLE 환경변수가 필요하다")
	default:
		return "", fmt.Errorf("알 수 없는 PLATFORM_ROLE: %s", s)
	}
}

func main() {
	// main은 얇게 두고 로직은 run에 둔다.
	// os.Exit는 defer를 실행하지 않으므로 한 곳에서만 부른다.
	if err := run(); err != nil {
		slog.Error("종료", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// 구조화 JSON 로깅. Cloud Logging이 필드를 그대로 인덱싱한다.
	// 토큰·영수증·purchaseToken 원문은 절대 로그에 남기지 않는다. AGENTS.md 참고.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	role, err := parseRole(os.Getenv("PLATFORM_ROLE"))
	if err != nil {
		return err
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Cloud Run이 주입하지만 로컬 실행을 위한 기본값
	}

	mux := http.NewServeMux()

	// 헬스체크는 계약이 아니라 인프라 관심사다. spec/openapi.yaml에 두지 않는다.
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		// TODO(P1): Firestore 연결과 레지스트리 로드를 확인한다
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ready")
	})

	// TODO(P1): role별 라우트를 등록한다
	// 표준 net/http의 ServeMux가 "POST /v1/inbox/{id}/claim" 패턴을 지원하므로
	// 외부 라우터를 도입하지 않는다.

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second, // Apple 검증은 외부 API 왕복이 붙는다
		IdleTimeout:       120 * time.Second,
	}

	// graceful shutdown. Cloud Run이 SIGTERM을 보내면 진행 중 요청을 마치고 내려간다.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("서버 시작", "role", role, "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("서버 실행 실패: %w", err)
	case <-ctx.Done():
		slog.Info("종료 신호 수신")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown 실패: %w", err)
	}
	slog.Info("정상 종료")
	return nil
}
