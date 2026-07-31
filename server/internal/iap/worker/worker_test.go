package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// fakeOutbox는 대기열을 대신한다.
type fakeOutbox struct {
	// queue는 마켓별 대기 항목이다.
	queue map[domain.Platform][]ledger.OutboxItem

	completed []string
	failed    []failRecord
	claimErr  error
}

type failRecord struct {
	orderKey    string
	code        platformerr.Code
	maxAttempts int
}

func (f *fakeOutbox) ClaimNext(_ context.Context, p domain.Platform) (ledger.OutboxItem, bool, error) {
	if f.claimErr != nil {
		return ledger.OutboxItem{}, false, f.claimErr
	}
	items := f.queue[p]
	if len(items) == 0 {
		return ledger.OutboxItem{}, false, nil
	}
	item := items[0]
	f.queue[p] = items[1:]
	return item, true, nil
}

func (f *fakeOutbox) CompleteOutbox(_ context.Context, orderKey, _ string) error {
	f.completed = append(f.completed, orderKey)
	return nil
}

func (f *fakeOutbox) FailOutbox(
	_ context.Context, orderKey, _ string,
	code platformerr.Code, maxAttempts int, _ time.Duration,
) error {
	f.failed = append(f.failed, failRecord{orderKey, code, maxAttempts})
	return nil
}

// fakeCompleter는 마켓 완료 호출을 대신한다.
type fakeCompleter struct {
	err  error
	call int
	got  []domain.VerifiedPurchase
}

func (f *fakeCompleter) CompleteGrant(_ context.Context, p domain.VerifiedPurchase) error {
	f.call++
	f.got = append(f.got, p)
	return f.err
}

func outboxItem(orderKey string, platform domain.Platform, attempt int) ledger.OutboxItem {
	return ledger.OutboxItem{
		OrderKey:     orderKey,
		LeaseID:      "lease-" + orderKey,
		AttemptCount: attempt,
		Purchase: domain.VerifiedPurchase{
			Platform:        platform,
			ProductID:       "gecko_galaxy",
			CanonicalID:     "canonical-" + orderKey,
			ProviderOrderID: "provider-" + orderKey,
			Completion:      domain.CompletionGoogleAcknowledge,
		},
	}
}

func newWorker(t *testing.T, ob Outbox, completers map[domain.Platform]Completer) *Worker {
	t.Helper()

	w, err := New(Config{
		Outbox:      ob,
		Completers:  completers,
		MaxAttempts: 12,
		MaxAge:      48 * time.Hour,
	})
	if err != nil {
		t.Fatalf("워커 생성 실패: %v", err)
	}
	return w
}

func TestRunOnceCompletesQueue(t *testing.T) {
	ob := &fakeOutbox{queue: map[domain.Platform][]ledger.OutboxItem{
		domain.PlatformGooglePlay: {
			outboxItem("order-1", domain.PlatformGooglePlay, 1),
			outboxItem("order-2", domain.PlatformGooglePlay, 1),
		},
	}}
	c := &fakeCompleter{}
	w := newWorker(t, ob, map[domain.Platform]Completer{domain.PlatformGooglePlay: c})

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("실행 실패: %v", err)
	}

	if stats.Claimed != 2 || stats.Completed != 2 || stats.Failed != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if c.call != 2 {
		t.Errorf("마켓 완료 호출 %d회", c.call)
	}
	if len(ob.completed) != 2 {
		t.Errorf("완료 기록 = %v", ob.completed)
	}
}

// 실패하면 백오프를 걸어 다시 예약한다. 지급은 건드리지 않는다.
func TestFailureIsRecordedNotDropped(t *testing.T) {
	ob := &fakeOutbox{queue: map[domain.Platform][]ledger.OutboxItem{
		domain.PlatformGooglePlay: {outboxItem("order-fail", domain.PlatformGooglePlay, 3)},
	}}
	c := &fakeCompleter{err: platformerr.New(platformerr.CodeProviderUnavailable, "Play 장애")}
	w := newWorker(t, ob, map[domain.Platform]Completer{domain.PlatformGooglePlay: c})

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("실행 실패: %v", err)
	}

	if stats.Failed != 1 || stats.Completed != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if len(ob.failed) != 1 {
		t.Fatalf("실패 기록 = %v", ob.failed)
	}
	if ob.failed[0].code != platformerr.CodeProviderUnavailable {
		t.Errorf("기록된 코드 = %q", ob.failed[0].code)
	}
	// dead-letter 판정 기준이 함께 전달되어야 한다
	if ob.failed[0].maxAttempts != 12 {
		t.Errorf("maxAttempts = %d", ob.failed[0].maxAttempts)
	}
	// 완료로 지워지면 안 된다
	if len(ob.completed) != 0 {
		t.Errorf("실패했는데 완료 처리했다: %v", ob.completed)
	}
}

// 한 건이 막혔다고 나머지를 멈추면 대기열이 인질로 잡힌다.
func TestOneFailureDoesNotStopBatch(t *testing.T) {
	ob := &fakeOutbox{queue: map[domain.Platform][]ledger.OutboxItem{
		domain.PlatformGooglePlay: {
			outboxItem("order-1", domain.PlatformGooglePlay, 1),
			outboxItem("order-2", domain.PlatformGooglePlay, 1),
			outboxItem("order-3", domain.PlatformGooglePlay, 1),
		},
	}}
	// 항상 실패하는 완료 처리기
	c := &fakeCompleter{err: errors.New("계속 실패")}
	w := newWorker(t, ob, map[domain.Platform]Completer{domain.PlatformGooglePlay: c})

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("한 건 실패로 실행 전체가 멈췄다: %v", err)
	}
	if stats.Claimed != 3 || stats.Failed != 3 {
		t.Errorf("stats = %+v, 나머지를 건너뛰었다", stats)
	}
}

// 자격증명이 없는 마켓 항목은 집지 않는다.
//
// 집으면 완료할 방법도 없이 시도 횟수만 축나서 dead-letter로 밀린다.
func TestSkipsMarketsWithoutCompleter(t *testing.T) {
	ob := &fakeOutbox{queue: map[domain.Platform][]ledger.OutboxItem{
		domain.PlatformGooglePlay: {outboxItem("order-play", domain.PlatformGooglePlay, 1)},
		domain.PlatformAppStore:   {outboxItem("order-apple", domain.PlatformAppStore, 1)},
	}}
	c := &fakeCompleter{}
	// Play만 조립됐다
	w := newWorker(t, ob, map[domain.Platform]Completer{domain.PlatformGooglePlay: c})

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("실행 실패: %v", err)
	}

	if stats.Claimed != 1 {
		t.Errorf("claimed = %d, want 1", stats.Claimed)
	}
	// App Store 항목은 그대로 남아 있어야 한다
	if len(ob.queue[domain.PlatformAppStore]) != 1 {
		t.Error("검증기 없는 마켓 항목을 집었다")
	}
}

// 한 번 실행이 무한정 돌지 않는다. Cloud Run Job은 시간 상한이 있다.
func TestBatchIsBounded(t *testing.T) {
	items := make([]ledger.OutboxItem, 0, maxBatch*3)
	for i := range maxBatch * 3 {
		items = append(items, outboxItem(string(rune('a'+i%26))+"-order", domain.PlatformGooglePlay, 1))
	}

	ob := &fakeOutbox{queue: map[domain.Platform][]ledger.OutboxItem{
		domain.PlatformGooglePlay: items,
	}}
	c := &fakeCompleter{}
	w := newWorker(t, ob, map[domain.Platform]Completer{domain.PlatformGooglePlay: c})

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("실행 실패: %v", err)
	}
	if stats.Claimed != maxBatch {
		t.Errorf("claimed = %d, want %d", stats.Claimed, maxBatch)
	}
	// 남은 건 다음 실행이 집는다
	if len(ob.queue[domain.PlatformGooglePlay]) == 0 {
		t.Error("상한을 넘겨 전부 처리했다")
	}
}

// 컨텍스트가 끝나면 즉시 멈춘다. 남은 건 다음 실행이 집는다.
func TestStopsOnCanceledContext(t *testing.T) {
	ob := &fakeOutbox{queue: map[domain.Platform][]ledger.OutboxItem{
		domain.PlatformGooglePlay: {
			outboxItem("order-1", domain.PlatformGooglePlay, 1),
			outboxItem("order-2", domain.PlatformGooglePlay, 1),
		},
	}}
	c := &fakeCompleter{}
	w := newWorker(t, ob, map[domain.Platform]Completer{domain.PlatformGooglePlay: c})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stats, err := w.RunOnce(ctx)
	if err != nil {
		t.Fatalf("취소를 에러로 만들었다: %v", err)
	}
	if stats.Claimed != 0 {
		t.Errorf("취소됐는데 %d건 집었다", stats.Claimed)
	}
}

// 대기열 조회 자체가 실패하면 그건 올린다. 계속 돌아도 소용없다.
func TestClaimErrorPropagates(t *testing.T) {
	ob := &fakeOutbox{claimErr: errors.New("firestore down")}
	w := newWorker(t, ob, map[domain.Platform]Completer{
		domain.PlatformGooglePlay: &fakeCompleter{},
	})

	if _, err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("대기열 장애를 성공으로 처리했다")
	}
}

// 완료 호출에는 대기열에 보관한 구매 정보가 그대로 전달된다.
func TestCompleterReceivesStoredPurchase(t *testing.T) {
	ob := &fakeOutbox{queue: map[domain.Platform][]ledger.OutboxItem{
		domain.PlatformGooglePlay: {outboxItem("order-1", domain.PlatformGooglePlay, 1)},
	}}
	c := &fakeCompleter{}
	w := newWorker(t, ob, map[domain.Platform]Completer{domain.PlatformGooglePlay: c})

	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("실행 실패: %v", err)
	}

	if len(c.got) != 1 {
		t.Fatalf("완료 호출 %d회", len(c.got))
	}
	got := c.got[0]
	if got.CanonicalID != "canonical-order-1" {
		t.Errorf("canonicalId = %q", got.CanonicalID)
	}
	// Apple은 finishTransaction에 transactionId가 필요하다
	if got.ProviderOrderID != "provider-order-1" {
		t.Errorf("providerOrderId = %q", got.ProviderOrderID)
	}
	if got.Completion != domain.CompletionGoogleAcknowledge {
		t.Errorf("completion = %q", got.Completion)
	}
}

func TestNewValidation(t *testing.T) {
	valid := Config{
		Outbox:      &fakeOutbox{},
		Completers:  map[domain.Platform]Completer{domain.PlatformGooglePlay: &fakeCompleter{}},
		MaxAttempts: 12,
	}

	if _, err := New(valid); err != nil {
		t.Fatalf("정상 설정을 거부했다: %v", err)
	}

	tests := []struct {
		name  string
		mutID func(*Config)
	}{
		{"대기열 없음", func(c *Config) { c.Outbox = nil }},
		{"완료 처리기 없음", func(c *Config) { c.Completers = nil }},
		{"재시도 상한 0", func(c *Config) { c.MaxAttempts = 0 }},
		{"재시도 상한 음수", func(c *Config) { c.MaxAttempts = -1 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutID(&cfg)
			if _, err := New(cfg); err == nil {
				t.Error("잘못된 설정을 통과시켰다")
			}
		})
	}
}
