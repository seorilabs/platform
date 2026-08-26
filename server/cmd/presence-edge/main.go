// Command presence-edge는 edge.vzyx.xyz의 RPI 수신 프로세스다.
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

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/presence"
	"github.com/seorilabs/platform/server/internal/presenceedge"
)

func main() {
	if err := run(); err != nil {
		slog.Error("presence edge 종료", "err", err)
		os.Exit(1)
	}
}

func run() error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	databaseURL := os.Getenv("PRESENCE_DATABASE_URL")
	publicKeyRaw := os.Getenv("PRESENCE_PUBLIC_KEY")
	if databaseURL == "" || publicKeyRaw == "" {
		return errors.New("presence edge: PRESENCE_DATABASE_URL과 PRESENCE_PUBLIC_KEY가 필요하다")
	}
	publicKey, err := presence.ParsePublicKey(publicKeyRaw)
	if err != nil {
		return err
	}
	verifier, err := presence.NewVerifier(publicKey)
	if err != nil {
		return err
	}
	repository, err := presenceedge.OpenMySQL(databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		if err := repository.Close(); err != nil {
			slog.Warn("presence edge MySQL 종료 실패", "err", err)
		}
	}()

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 3*time.Second)
	if err := repository.Ping(startupCtx); err != nil {
		cancelStartup()
		return fmt.Errorf("presence edge: MySQL 준비 확인 실패: %w", err)
	}
	cancelStartup()

	mux := http.NewServeMux()
	presenceedge.NewHandler(verifier, repository).Register(mux)
	handler := edgeCORS(httpx.Chain(mux,
		httpx.Recover(),
		httpx.RequestID(),
		httpx.AccessLog(),
		httpx.Timeout(time.Second),
	))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go cleanupExpired(ctx, repository)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("presence edge 시작", "port", port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func cleanupExpired(ctx context.Context, repo *presenceedge.MySQLRepository) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			cleanupCtx, cancel := context.WithTimeout(ctx, time.Second)
			_, err := repo.Cleanup(cleanupCtx, now.UTC().Add(-10*time.Minute), 1000)
			cancel()
			if err != nil {
				slog.Warn("만료 presence 정리 실패", "err", err)
			}
		}
	}
}

func edgeCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
