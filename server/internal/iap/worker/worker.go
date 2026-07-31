// Package worker는 마켓 완료 처리를 재시도한다.
//
// 지급은 끝났는데 마켓에 "완료했다"고 알리지 못한 주문을 다시 처리한다.
// 불변식 7 때문에 이 대기열이 생긴다 — 완료 실패로 지급을 롤백하지 않으므로
// 누군가는 나중에 마켓에 알려야 한다.
//
// Play는 3일 안에 acknowledge하지 않으면 자동 환불한다.
// 이 워커가 멈추면 유저는 산 물건을 잃고 우리는 매출을 잃는다.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// Outbox는 완료 재시도 대기열이다.
//
// 소비자인 이 패키지가 인터페이스를 정의한다. ledger.Ledger가 구현한다.
type Outbox interface {
	ClaimNext(ctx context.Context, platform domain.Platform) (ledger.OutboxItem, bool, error)
	CompleteOutbox(ctx context.Context, orderKey, leaseID string) error
	FailOutbox(ctx context.Context, orderKey, leaseID string,
		errCode platformerr.Code, maxAttempts int, maxAge time.Duration) error
}

// Completer는 마켓에 완료를 알린다.
//
// providers의 검증기가 그대로 구현한다.
type Completer interface {
	CompleteGrant(ctx context.Context, p domain.VerifiedPurchase) error
}

// Auditor는 감사 원장에 기록한다.
type Auditor interface {
	Record(ctx context.Context, action, appID, puid, outcome string, detail map[string]any)
}

// maxBatch는 한 번 실행에서 처리할 최대 건수다.
//
// Cloud Run Job은 시간 상한이 있다. 무한정 돌지 않게 끊는다.
// 남은 것은 다음 실행이 집는다.
const maxBatch = 20

// Config는 워커 설정이다.
type Config struct {
	Outbox Outbox
	// Completers는 마켓별 완료 처리기다.
	//
	// 자격증명이 없어 조립하지 못한 마켓은 여기 없다.
	// 그 마켓 항목은 집지 않는다 — 집으면 시도 횟수만 축낸다.
	Completers map[domain.Platform]Completer
	Auditor    Auditor

	MaxAttempts int
	MaxAge      time.Duration
}

// Worker는 완료 재시도 워커다.
type Worker struct {
	outbox     Outbox
	completers map[domain.Platform]Completer
	auditor    Auditor

	maxAttempts int
	maxAge      time.Duration
}

func New(cfg Config) (*Worker, error) {
	if cfg.Outbox == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"완료 대기열이 필요해요")
	}
	if len(cfg.Completers) == 0 {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"완료 처리기가 하나도 없어요")
	}
	if cfg.MaxAttempts <= 0 {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"재시도 횟수 상한이 올바르지 않아요")
	}

	return &Worker{
		outbox:      cfg.Outbox,
		completers:  cfg.Completers,
		auditor:     cfg.Auditor,
		maxAttempts: cfg.MaxAttempts,
		maxAge:      cfg.MaxAge,
	}, nil
}

// Stats는 한 번 실행의 결과다.
type Stats struct {
	Claimed   int
	Completed int
	Failed    int
}

// RunOnce는 대기열을 한 바퀴 처리한다.
//
// Cloud Run Job이 주기적으로 부른다. 여러 인스턴스가 동시에 돌아도
// lease가 중복 처리를 막는다 — Firebase의 maxInstances:1 보장이
// Cloud Run Job에는 없어서 이게 유일한 방어선이다.
func (w *Worker) RunOnce(ctx context.Context) (Stats, error) {
	var stats Stats

	// 자격증명이 있는 마켓만 돈다.
	for platform := range w.completers {
		for range maxBatch {
			// 컨텍스트가 끝나면 남은 건 다음 실행이 집는다.
			if err := ctx.Err(); err != nil {
				return stats, nil
			}

			// 한 번에 하나만 집는다. 여러 건을 한꺼번에 점유하면
			// 앞 건이 느릴 때 뒤 건의 lease가 처리도 못 해보고 만료된다.
			item, found, err := w.outbox.ClaimNext(ctx, platform)
			if err != nil {
				return stats, err
			}
			if !found {
				break
			}

			stats.Claimed++
			if w.completeOne(ctx, platform, item) {
				stats.Completed++
			} else {
				stats.Failed++
			}
		}
	}

	return stats, nil
}

// completeOne은 항목 하나를 마켓에 완료 처리한다.
//
// 실패해도 에러를 올리지 않는다. 한 건이 막혔다고 나머지를 멈추면
// 대기열 전체가 그 한 건에 인질로 잡힌다.
func (w *Worker) completeOne(ctx context.Context, platform domain.Platform, item ledger.OutboxItem) bool {
	completer := w.completers[platform]

	err := completer.CompleteGrant(ctx, item.Purchase)
	if err == nil {
		if cerr := w.outbox.CompleteOutbox(ctx, item.OrderKey, item.LeaseID); cerr != nil {
			slog.ErrorContext(ctx, "완료 기록 실패",
				"platform", string(platform), "err", cerr)
			return false
		}
		w.audit(ctx, item, "ok", "")
		return true
	}

	code := platformerr.CodeOf(err)

	slog.WarnContext(ctx, "마켓 완료 처리 실패",
		"platform", string(platform),
		"attempt", item.AttemptCount,
		"code", string(code),
	)

	if ferr := w.outbox.FailOutbox(
		ctx, item.OrderKey, item.LeaseID, code, w.maxAttempts, w.maxAge,
	); ferr != nil {
		slog.ErrorContext(ctx, "실패 기록 실패",
			"platform", string(platform), "err", ferr)
	}

	w.audit(ctx, item, string(code), string(code))
	return false
}

func (w *Worker) audit(ctx context.Context, item ledger.OutboxItem, outcome, errCode string) {
	if w.auditor == nil {
		return
	}
	// 소유자는 대기열에 없다. 필요하면 orderKey로 주문을 찾는다.
	w.auditor.Record(ctx, "iap.completion_retry", "", "", outcome, map[string]any{
		"platform":   string(item.Purchase.Platform),
		"attempt":    item.AttemptCount,
		"error_code": errCode,
	})
}
