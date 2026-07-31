package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/config"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/providers/apple"
	"github.com/seorilabs/platform/server/internal/iap/webhook"
	"github.com/seorilabs/platform/server/internal/iap/worker"
)

// registerWebhooks는 마켓 알림 라우트를 연다.
//
// 자격증명이 없는 마켓의 엔드포인트는 열지 않는다.
// 열어두면 알림을 받고도 재검증을 못 해 계속 실패만 쌓인다.
func registerWebhooks(mux *http.ServeMux, cfg config.Config, d *deps) error {
	ic := cfg.IAP
	audit := auditAdapter{col: d.events}

	if ic.Apple.Enabled() {
		// 알림 JWS 검증에 쓰는 클라이언트는 검증기와 같은 설정이어야 한다.
		// 다르면 한쪽은 통과하고 다른 쪽은 막히는 상태가 된다.
		client, err := apple.NewClient(apple.Config{
			KeyContent:  ic.Apple.KeyContent,
			KeyID:       ic.Apple.KeyID,
			Issuer:      ic.Apple.Issuer,
			BundleID:    ic.Apple.BundleID,
			Sandbox:     ic.Apple.Sandbox,
			RequireOCSP: !ic.Apple.Sandbox,
		})
		if err != nil {
			return err
		}

		h, err := webhook.NewAppleHandler(webhook.AppleConfig{
			Parser:     client,
			Verifier:   d.iap.verifiers[domain.PlatformAppStore],
			Events:     d.iap.ledger,
			Reconciler: d.iap.ledger,
			Auditor:    audit,
			BundleID:   ic.Apple.BundleID,
		})
		if err != nil {
			return err
		}
		h.Register(mux)
		slog.Info("App Store 알림 수신 준비 완료")
	}

	if ic.Play.Enabled() {
		// Pub/Sub push는 OIDC 토큰으로 인증한다.
		// audience는 push subscription에 설정한 값과 같아야 한다.
		audience := os.Getenv("IAP_PLAY_RTDN_AUDIENCE")

		switch {
		case audience == "":
			// 검증할 수 없으면 열지 않는다. 인증 없는 알림
			// 엔드포인트는 아무나 환불을 흉내 낼 수 있는 통로다.
			slog.Warn("Play 알림 audience가 없어 웹훅을 열지 않는다")

		default:
			validator, err := webhook.NewGoogleTokenValidator(audience)
			if err != nil {
				return err
			}

			h, err := webhook.NewPlayHandler(webhook.PlayConfig{
				Validator:     validator,
				Verifier:      d.iap.verifiers[domain.PlatformGooglePlay],
				Events:        d.iap.ledger,
				Reconciler:    d.iap.ledger,
				Auditor:       audit,
				PackageName:   ic.Play.PackageName,
				AllowedEmails: splitList(os.Getenv("IAP_PLAY_RTDN_SENDERS")),
			})
			if err != nil {
				return err
			}
			h.Register(mux)
			slog.Info("Play 알림 수신 준비 완료")
		}
	}

	return nil
}

// newWorker는 완료 재시도 워커를 조립한다.
func newWorker(parts *iapParts, cfg config.Config, audit worker.Auditor) (*worker.Worker, error) {
	completers := make(map[domain.Platform]worker.Completer, len(parts.verifiers))
	for platform, v := range parts.verifiers {
		// AIT는 클라이언트가 완료 처리를 한다. 서버가 할 일이 없다.
		if platform == domain.PlatformAppsInToss {
			continue
		}
		completers[platform] = v
	}

	return worker.New(worker.Config{
		Outbox:      parts.ledger,
		Completers:  completers,
		Auditor:     audit,
		MaxAttempts: cfg.IAP.CompletionMaxAttempts,
		MaxAge:      cfg.IAP.CompletionMaxAge,
	})
}

// runWorker는 완료 재시도 워커를 한 번 돌린다.
//
// Cloud Run Job으로 주기 실행한다. 여러 인스턴스가 겹쳐 돌아도
// lease가 중복 완료를 막는다 — Firebase의 maxInstances:1 보장이
// Cloud Run Job에는 없어서 이게 유일한 방어선이다.
func runWorker(ctx context.Context, cfg config.Config) error {
	deps, err := newDeps(ctx, cfg)
	if err != nil {
		return err
	}
	defer deps.Close()

	if deps.iap == nil {
		// 결제 설정 없이 워커를 띄우면 아무 일도 하지 않으면서
		// 성공한 것처럼 보인다. 완료 대기열이 조용히 쌓인다.
		return errors.New("worker role에 결제 설정이 필요하다")
	}

	w, err := newWorker(deps.iap, cfg, auditAdapter{col: deps.events})
	if err != nil {
		return err
	}

	// Job 실행 시간에 상한을 둔다. 남은 항목은 다음 실행이 집는다.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	stats, err := w.RunOnce(ctx)
	slog.Info("완료 재시도 종료",
		"claimed", stats.Claimed,
		"completed", stats.Completed,
		"failed", stats.Failed,
	)
	if err != nil {
		return err
	}

	// dead-letter가 있으면 사람이 봐야 한다.
	// 마켓에 완료를 알리지 못한 주문이 영구히 남았다는 뜻이다.
	if n, err := deps.iap.ledger.CountDeadLetters(ctx); err != nil {
		slog.WarnContext(ctx, "dead-letter 집계 실패", "err", err)
	} else if n > 0 {
		slog.ErrorContext(ctx, "완료 처리를 포기한 주문이 있다", "count", n)
	}

	return nil
}

// splitList는 쉼표로 나눈 목록을 읽는다.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
