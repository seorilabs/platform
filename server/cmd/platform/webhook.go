package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/config"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/providers/apple"
	"github.com/seorilabs/platform/server/internal/iap/refundreview"
	"github.com/seorilabs/platform/server/internal/iap/verify"
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

		switch audience {
		case "":
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
				Apps:          d.registry,
				RefundReviews: d.iap.ledger,
				RefundSealer:  d.iap.refundKeys,
			})
			if err != nil {
				return err
			}
			h.Register(mux)
			slog.Info("Play 알림 수신 준비 완료")
		}
	}

	// 신규 앱 webhook은 요청 시점에 임의 appId로 구현체를 선택하지 않는다.
	// composition root가 registry의 appId/package/bundle과 verifier/ledger를
	// 한 handler에 묶어 고정 경로에 등록한다. payload가 다른 앱이면 provider
	// handler가 다시 검증해 거부한다. 기존 단일 앱 URL은 위에서 계속 유지한다.
	for appID, app := range d.iap.apps {
		appLedger := d.iap.appLedgers[appID]
		appVerifiers := d.iap.appVerifiers[appID]
		if appLedger == nil {
			continue
		}
		if verifier := appVerifiers[domain.PlatformAppStore]; verifier != nil {
			client, err := apple.NewClient(apple.Config{
				KeyContent: ic.Apple.KeyContent, KeyID: ic.Apple.KeyID, Issuer: ic.Apple.Issuer,
				BundleID:    app.IAP.AppStoreBundleID,
				Sandbox:     app.IAP.LedgerEnvironment == "sandbox",
				RequireOCSP: app.IAP.LedgerEnvironment != "sandbox",
			})
			if err != nil {
				return err
			}
			h, err := webhook.NewAppleHandler(webhook.AppleConfig{
				Parser: client, Verifier: verifier, Events: appLedger, Reconciler: appLedger,
				Auditor: audit, BundleID: app.IAP.AppStoreBundleID, AppID: appID,
			})
			if err != nil {
				return err
			}
			h.RegisterAt(mux, "/v1/iap/webhooks/apple/"+appID)
		}
		if verifier := appVerifiers[domain.PlatformGooglePlay]; verifier != nil {
			audience := os.Getenv("IAP_PLAY_RTDN_AUDIENCE")
			if audience == "" {
				continue
			}
			validator, err := webhook.NewGoogleTokenValidator(audience)
			if err != nil {
				return err
			}
			h, err := webhook.NewPlayHandler(webhook.PlayConfig{
				Validator: validator, Verifier: verifier, Events: appLedger, Reconciler: appLedger,
				Auditor: audit, PackageName: app.IAP.GooglePlayPackageName,
				AllowedEmails: splitList(os.Getenv("IAP_PLAY_RTDN_SENDERS")), Apps: d.registry,
				RefundReviews: appLedger, RefundSealer: d.iap.refundKeys,
			})
			if err != nil {
				return err
			}
			h.RegisterAt(mux, "/v1/iap/webhooks/play/"+appID)
		}
	}

	return nil
}

// newWorker는 완료 재시도 워커를 조립한다.
func newWorker(parts *iapParts, cfg config.Config, audit worker.Auditor) (*worker.Worker, error) {
	return newWorkerFor("", parts.ledger, parts.verifiers, parts.refundKeys, cfg, audit)
}

func newWorkerFor(
	appID string,
	appLedger *ledger.Ledger,
	verifiers map[domain.Platform]verify.Verifier,
	refundKeys *refundreview.Keyring,
	cfg config.Config,
	audit worker.Auditor,
) (*worker.Worker, error) {
	completers := make(map[domain.Platform]worker.Completer, len(verifiers))
	for platform, v := range verifiers {
		// AIT는 클라이언트가 완료 처리를 한다. 서버가 할 일이 없다.
		if platform == domain.PlatformAppsInToss {
			continue
		}
		completers[platform] = v
	}

	workerConfig := worker.Config{
		Outbox:      appLedger,
		AppID:       appID,
		Completers:  completers,
		Auditor:     audit,
		MaxAttempts: cfg.IAP.CompletionMaxAttempts,
		MaxAge:      cfg.IAP.CompletionMaxAge,
	}
	if playVerifier, ok := verifiers[domain.PlatformGooglePlay]; ok {
		responder, ok := playVerifier.(worker.RefundResponder)
		if !ok || refundKeys == nil {
			return nil, errors.New("worker role에 Play 환불 검토 설정이 필요하다")
		}
		workerConfig.RefundReviews = appLedger
		workerConfig.RefundOpener = refundKeys
		workerConfig.RefundResponder = responder
	}
	return worker.New(workerConfig)
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

	// Job 실행 시간에 상한을 둔다. 남은 항목은 다음 실행이 집는다.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	total := worker.Stats{}
	appIDs := make([]string, 0, len(deps.iap.appLedgers))
	for appID := range deps.iap.appLedgers {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	for _, appID := range appIDs {
		w, err := newWorkerFor(
			appID, deps.iap.appLedgers[appID], deps.iap.appVerifiers[appID],
			deps.iap.refundKeys, cfg, auditAdapter{col: deps.events},
		)
		if err != nil {
			return err
		}
		stats, err := w.RunOnce(ctx)
		if err != nil {
			return err
		}
		total.Claimed += stats.Claimed
		total.Completed += stats.Completed
		total.Failed += stats.Failed
		total.RefundClaimed += stats.RefundClaimed
		total.RefundResponded += stats.RefundResponded
		total.RefundFailed += stats.RefundFailed
		total.RefundExpired += stats.RefundExpired
	}
	stats := total
	slog.Info("완료 재시도 종료",
		"claimed", stats.Claimed,
		"completed", stats.Completed,
		"failed", stats.Failed,
		"refund_claimed", stats.RefundClaimed,
		"refund_responded", stats.RefundResponded,
		"refund_failed", stats.RefundFailed,
		"refund_expired", stats.RefundExpired,
	)
	// dead-letter가 있으면 사람이 봐야 한다.
	// 마켓에 완료를 알리지 못한 주문이 영구히 남았다는 뜻이다.
	for _, appID := range appIDs {
		if n, err := deps.iap.appLedgers[appID].CountDeadLetters(ctx); err != nil {
			slog.WarnContext(ctx, "dead-letter 집계 실패", "app_id", appID, "err", err)
		} else if n > 0 {
			slog.ErrorContext(ctx, "완료 처리를 포기한 주문이 있다", "app_id", appID, "count", n)
		}
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
