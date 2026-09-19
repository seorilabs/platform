package blocklist

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// fakeSource는 저장소를 메모리로 흉내낸다.
//
// loads는 실제로 저장소를 몇 번 읽었는지 센다. 캐시가 동작하는지는
// 결과값이 아니라 읽기 횟수로만 확인할 수 있다.
type fakeSource struct {
	entries map[string][]Entry
	err     error
	loads   int
}

func newFakeSource() *fakeSource {
	return &fakeSource{entries: map[string][]Entry{}}
}

func (f *fakeSource) LoadUIDs(_ context.Context, appID string) (map[string]struct{}, error) {
	f.loads++
	if f.err != nil {
		return nil, f.err
	}
	uids := map[string]struct{}{}
	for _, e := range f.entries[appID] {
		uids[e.UID] = struct{}{}
	}
	return uids, nil
}

func (f *fakeSource) ListEntries(_ context.Context, appID string) ([]Entry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entries[appID], nil
}

func (f *fakeSource) Put(_ context.Context, appID string, e Entry) error {
	if f.err != nil {
		return f.err
	}
	for i, cur := range f.entries[appID] {
		if cur.UID == e.UID {
			f.entries[appID][i] = e
			return nil
		}
	}
	f.entries[appID] = append(f.entries[appID], e)
	return nil
}

func (f *fakeSource) Remove(_ context.Context, appID, uid string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	for i, cur := range f.entries[appID] {
		if cur.UID == uid {
			f.entries[appID] = append(f.entries[appID][:i], f.entries[appID][i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func newTestService(src Source, now func() time.Time) *Service {
	return NewService(src).WithClock(now)
}

func TestBlockedReadsFromSource(t *testing.T) {
	ctx := context.Background()
	src := newFakeSource()
	svc := newTestService(src, time.Now)

	if _, err := svc.Block(ctx, "ungeul", "uid-1", "어뷰징", "운영자"); err != nil {
		t.Fatalf("차단 실패: %v", err)
	}

	blocked, err := svc.Blocked(ctx, "ungeul", "uid-1")
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if !blocked {
		t.Error("차단된 계정을 통과시켰다")
	}

	blocked, err = svc.Blocked(ctx, "ungeul", "uid-2")
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if blocked {
		t.Error("차단되지 않은 계정을 막았다")
	}
}

// 차단 목록은 앱 범위다. 같은 uid라도 다른 앱은 막히면 안 된다.
func TestBlockedIsScopedToApp(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(newFakeSource(), time.Now)

	if _, err := svc.Block(ctx, "ungeul", "uid-1", "", "운영자"); err != nil {
		t.Fatalf("차단 실패: %v", err)
	}

	blocked, err := svc.Blocked(ctx, "happy-farm", "uid-1")
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if blocked {
		t.Error("다른 앱의 차단이 전이됐다")
	}
}

// TTL 안에서는 저장소를 다시 읽지 않는다. 인증 요청마다 읽으면
// Firestore 비용과 지연이 사용자 수에 비례해 붙는다.
func TestBlockedCachesWithinTTL(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	src := newFakeSource()
	svc := newTestService(src, func() time.Time { return now }).WithTTL(60 * time.Second)

	for range 5 {
		if _, err := svc.Blocked(ctx, "ungeul", "uid-1"); err != nil {
			t.Fatalf("조회 실패: %v", err)
		}
	}
	if src.loads != 1 {
		t.Errorf("loads = %d, want 1", src.loads)
	}

	now = now.Add(61 * time.Second)
	if _, err := svc.Blocked(ctx, "ungeul", "uid-1"); err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if src.loads != 2 {
		t.Errorf("만료 후 loads = %d, want 2", src.loads)
	}
}

// 차단 직후에는 캐시를 버린다. TTL을 기다리면 방금 차단한 계정이
// 최대 60초 동안 계속 들어온다.
func TestBlockInvalidatesCache(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	src := newFakeSource()
	svc := newTestService(src, func() time.Time { return now }).WithTTL(time.Hour)

	if _, err := svc.Blocked(ctx, "ungeul", "uid-1"); err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if _, err := svc.Block(ctx, "ungeul", "uid-1", "", "운영자"); err != nil {
		t.Fatalf("차단 실패: %v", err)
	}

	blocked, err := svc.Blocked(ctx, "ungeul", "uid-1")
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if !blocked {
		t.Error("차단이 즉시 반영되지 않았다")
	}
}

// 해제도 즉시 반영된다. 오차단을 푼 사용자가 60초를 더 기다리면 안 된다.
func TestUnblockInvalidatesCache(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	svc := newTestService(newFakeSource(), func() time.Time { return now }).WithTTL(time.Hour)

	if _, err := svc.Block(ctx, "ungeul", "uid-1", "", "운영자"); err != nil {
		t.Fatalf("차단 실패: %v", err)
	}
	if _, err := svc.Blocked(ctx, "ungeul", "uid-1"); err != nil {
		t.Fatalf("조회 실패: %v", err)
	}

	removed, err := svc.Unblock(ctx, "ungeul", "uid-1")
	if err != nil {
		t.Fatalf("해제 실패: %v", err)
	}
	if !removed {
		t.Fatal("removed = false, want true")
	}

	blocked, err := svc.Blocked(ctx, "ungeul", "uid-1")
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if blocked {
		t.Error("해제가 즉시 반영되지 않았다")
	}
}

// 차단돼 있지 않은 계정의 해제는 실패가 아니다. false만 돌려준다.
func TestUnblockMissingIsNotError(t *testing.T) {
	removed, err := newTestService(newFakeSource(), time.Now).
		Unblock(context.Background(), "ungeul", "없는-uid")
	if err != nil {
		t.Fatalf("해제 실패: %v", err)
	}
	if removed {
		t.Error("removed = true, want false")
	}
}

// 저장소가 죽었고 캐시도 없으면 통과시키지 않고 에러를 올린다.
// 차단 여부를 모른 채 통과시키면 차단이 무의미해진다.
func TestBlockedFailsWithoutCache(t *testing.T) {
	src := newFakeSource()
	src.err = errors.New("firestore 장애")

	_, err := newTestService(src, time.Now).Blocked(context.Background(), "ungeul", "uid-1")
	if err == nil {
		t.Fatal("저장소 장애를 통과시켰다")
	}
	if code := platformerr.CodeOf(err); code != platformerr.CodeInternal {
		t.Errorf("code = %q, want internal", code)
	}
}

// 저장소가 죽어도 캐시가 있으면 그 값으로 계속 간다. 차단 목록을 못
// 읽었다는 이유로 정상 사용자 전체를 막는 편이 피해가 크다.
func TestBlockedFallsBackToStaleCache(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	src := newFakeSource()
	src.entries["ungeul"] = []Entry{{UID: "uid-1"}}
	svc := newTestService(src, func() time.Time { return now }).WithTTL(60 * time.Second)

	if _, err := svc.Blocked(ctx, "ungeul", "uid-1"); err != nil {
		t.Fatalf("조회 실패: %v", err)
	}

	src.err = errors.New("firestore 장애")
	now = now.Add(61 * time.Second)

	blocked, err := svc.Blocked(ctx, "ungeul", "uid-1")
	if err != nil {
		t.Fatalf("낡은 캐시로 이어가지 못했다: %v", err)
	}
	if !blocked {
		t.Error("낡은 캐시의 차단이 풀렸다")
	}
}

func TestBlockRejectsLongReason(t *testing.T) {
	reason := ""
	for range MaxReasonLen + 1 {
		reason += "가"
	}

	_, err := newTestService(newFakeSource(), time.Now).
		Block(context.Background(), "ungeul", "uid-1", reason, "운영자")
	if code := platformerr.CodeOf(err); code != platformerr.CodeRequestInvalid {
		t.Errorf("code = %q, want request_invalid", code)
	}
}

// 재차단은 실패가 아니라 갱신이다. 사고 대응 중에 같은 조작을
// 반복하는 편이 정상이다.
func TestBlockTwiceUpdatesReason(t *testing.T) {
	ctx := context.Background()
	src := newFakeSource()
	svc := newTestService(src, time.Now)

	if _, err := svc.Block(ctx, "ungeul", "uid-1", "1차", "운영자"); err != nil {
		t.Fatalf("차단 실패: %v", err)
	}
	if _, err := svc.Block(ctx, "ungeul", "uid-1", "2차", "운영자"); err != nil {
		t.Fatalf("재차단 실패: %v", err)
	}

	entries, err := svc.List(ctx, "ungeul")
	if err != nil {
		t.Fatalf("목록 조회 실패: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Reason != "2차" {
		t.Errorf("reason = %q, want 2차", entries[0].Reason)
	}
}
