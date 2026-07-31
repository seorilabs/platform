package ledger

import (
	"testing"
	"time"
)

// 백오프는 순수 계산이라 Firestore 없이 검증한다.
//
// 여기가 틀리면 두 방향으로 잘못된다. 너무 짧으면 마켓을 두드려
// rate limit에 걸리고, 너무 길면 Play의 3일 자동 환불 안에
// 완료를 못 해서 매출이 사라진다.
func TestBackoffFor(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		// 방어. 0이나 음수가 와도 첫 간격으로 취급한다
		{-1, 60 * time.Second},
		{0, 60 * time.Second},
		{1, 60 * time.Second},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 32 * time.Minute},
		{7, 64 * time.Minute},
		{8, 128 * time.Minute},
		{9, 256 * time.Minute}, // 4h16m. 아직 상한 아래다
		// 2^9 * 60s = 8h32m이라 여기서 상한에 걸린다
		{10, 6 * time.Hour},
		{11, 6 * time.Hour},
		// 시프트로 계산했다면 오버플로로 음수가 됐을 값들
		{64, 6 * time.Hour},
		{1000, 6 * time.Hour},
	}

	for _, tt := range tests {
		got := backoffFor(tt.attempt)
		if got != tt.want {
			t.Errorf("backoffFor(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
		if got <= 0 {
			t.Errorf("backoffFor(%d)가 양수가 아니다: %v", tt.attempt, got)
		}
		if got > backoffMax {
			t.Errorf("backoffFor(%d)가 상한을 넘었다: %v", tt.attempt, got)
		}
	}
}

// 기본 재시도 12회 안에 Play의 3일 자동 환불 시한을 넘기지 않아야 한다.
//
// 넘기면 완료를 못 한 채 환불되고 유저는 산 물건을 잃는다.
func TestBackoffFitsPlayAcknowledgeWindow(t *testing.T) {
	const defaultMaxAttempts = 12

	var total time.Duration
	for i := 1; i <= defaultMaxAttempts; i++ {
		total += backoffFor(i)
	}

	// 3일(72시간) 안에 12회를 다 쓸 수 있어야 한다.
	// 다 쓰기도 전에 시한이 지나면 dead-letter 판정이 무의미해진다.
	const playAcknowledgeWindow = 72 * time.Hour
	if total > playAcknowledgeWindow {
		t.Errorf("12회 누적 %v가 Play 시한 %v를 넘는다", total, playAcknowledgeWindow)
	}

	// 초반은 촘촘해야 한다. 일시적 장애는 대개 몇 분 안에 풀린다.
	var firstSix time.Duration
	for i := 1; i <= 6; i++ {
		firstSix += backoffFor(i)
	}
	if firstSix > 2*time.Hour {
		t.Errorf("첫 6회 누적이 %v로 너무 성기다", firstSix)
	}

	t.Logf("첫 6회 누적 %v, 12회 누적 %v", firstSix, total)
}
