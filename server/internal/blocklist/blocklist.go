// Package blocklist는 앱별 차단 계정을 관리한다.
//
// 차단 목록은 레지스트리가 아니라 여기 있다. 레지스트리의 원장은 git이고
// regsync가 Firestore 문서를 통째로 덮어쓴다. 차단을 레지스트리에 두면
// 운영자가 차단한 계정이 다음 regsync에 조용히 풀린다. 차단은 사고 대응
// 중에 즉시 넣고 빼는 런타임 상태이지 선언적 설정이 아니다.
//
// 두 번째 이유는 공개 범위다. registry/apps/*.json은 public 저장소에 있고
// 사용자 식별자는 거기 들어가면 안 된다. ADR 0005(no-PII-in-platform)와
// ADR 0026 참고.
package blocklist

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// DefaultTTL은 캐시 수명이다.
//
// 레지스트리·원격설정과 같은 60초다. 차단은 사고 대응 경로라 반영이
// 늦으면 안 되고, 인증 요청마다 Firestore를 읽을 수도 없다.
const DefaultTTL = 60 * time.Second

// MaxReasonLen은 사유 길이 상한이다. 운영자 메모지 자유 서술 로그가 아니다.
const MaxReasonLen = 200

// Entry는 차단 기록 한 건이다.
//
// uid는 문서 ID이면서 본문에도 둔다. 본문에만 있으면 목록 조회가
// 문서 ID와 어긋났을 때 어느 쪽이 맞는지 판정할 근거가 없다.
type Entry struct {
	UID       string    `json:"uid" firestore:"uid"`
	Reason    string    `json:"reason,omitempty" firestore:"reason"`
	BlockedBy string    `json:"blockedBy,omitempty" firestore:"blocked_by"`
	BlockedAt time.Time `json:"blockedAt" firestore:"blocked_at"`
}

// Source는 차단 기록의 저장소다.
//
// 인터페이스를 여기에 두는 이유는 Service가 소비자이기 때문이다.
// registry.Source와 같은 구조다.
type Source interface {
	// LoadUIDs는 판정용 집합을 돌려준다. 본문을 읽지 않는다.
	LoadUIDs(ctx context.Context, appID string) (map[string]struct{}, error)
	// ListEntries는 운영 화면용 상세 목록이다. 차단 시각 내림차순이다.
	ListEntries(ctx context.Context, appID string) ([]Entry, error)
	Put(ctx context.Context, appID string, e Entry) error
	// Remove는 실제로 지웠으면 true를 돌려준다.
	Remove(ctx context.Context, appID, uid string) (bool, error)
}

type cached struct {
	uids     map[string]struct{}
	loadedAt time.Time
}

// Service는 차단 목록을 읽고 쓴다.
type Service struct {
	source Source
	ttl    time.Duration
	now    func() time.Time

	mu    sync.RWMutex
	cache map[string]cached
}

// NewService는 서비스를 만든다. 첫 조회 시점에 로드한다.
func NewService(src Source) *Service {
	return &Service{
		source: src,
		ttl:    DefaultTTL,
		now:    time.Now,
		cache:  map[string]cached{},
	}
}

// WithTTL은 캐시 수명을 바꾼다.
func (s *Service) WithTTL(d time.Duration) *Service {
	s.ttl = d
	return s
}

// WithClock은 시계를 주입한다. 테스트에서 만료를 제어하려고 둔다.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Blocked는 계정이 차단됐는지 본다.
//
// 캐시가 만료됐고 저장소도 실패하면 낡은 캐시로 계속한다. 차단 목록을
// 못 읽었다는 이유로 정상 사용자 전체를 막는 편이 피해가 크다. 캐시가
// 아예 없으면 판정 근거가 없으므로 에러를 돌려준다. 호출자는 이 에러를
// 통과가 아니라 실패로 다뤄야 한다.
func (s *Service) Blocked(ctx context.Context, appID, uid string) (bool, error) {
	if appID == "" || uid == "" {
		return false, nil
	}

	s.mu.RLock()
	c, ok := s.cache[appID]
	fresh := ok && s.now().Sub(c.loadedAt) < s.ttl
	s.mu.RUnlock()

	if fresh {
		_, blocked := c.uids[uid]
		return blocked, nil
	}

	uids, err := s.load(ctx, appID)
	if err != nil {
		if ok {
			slog.WarnContext(ctx, "차단 목록 갱신 실패. 캐시로 계속한다",
				"app_id", appID, "err", err)
			_, blocked := c.uids[uid]
			return blocked, nil
		}
		return false, err
	}
	_, blocked := uids[uid]
	return blocked, nil
}

func (s *Service) load(ctx context.Context, appID string) (map[string]struct{}, error) {
	uids, err := s.source.LoadUIDs(ctx, appID)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeInternal, "차단 목록을 불러오지 못했어요")
	}

	s.mu.Lock()
	s.cache[appID] = cached{uids: uids, loadedAt: s.now()}
	s.mu.Unlock()

	return uids, nil
}

// List는 앱의 차단 기록을 돌려준다. Admin API 조회용이다.
//
// 캐시를 쓰지 않는다. 운영자가 방금 넣은 차단이 목록에 안 보이면
// 같은 조작을 두 번 한다.
func (s *Service) List(ctx context.Context, appID string) ([]Entry, error) {
	entries, err := s.source.ListEntries(ctx, appID)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeInternal, "차단 목록을 불러오지 못했어요")
	}
	return entries, nil
}

// Block은 계정을 차단한다. 이미 차단돼 있으면 사유와 시각을 갱신한다.
func (s *Service) Block(ctx context.Context, appID, uid, reason, actor string) (Entry, error) {
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > MaxReasonLen {
		return Entry{}, platformerr.New(platformerr.CodeRequestInvalid, "차단 사유가 너무 길어요")
	}

	entry := Entry{
		UID:       uid,
		Reason:    reason,
		BlockedBy: actor,
		BlockedAt: s.now().UTC(),
	}
	if err := s.source.Put(ctx, appID, entry); err != nil {
		return Entry{}, platformerr.Wrap(err, platformerr.CodeInternal, "계정을 차단하지 못했어요")
	}

	s.invalidate(appID)
	return entry, nil
}

// Unblock은 차단을 해제한다. 차단돼 있지 않았으면 false를 돌려준다.
func (s *Service) Unblock(ctx context.Context, appID, uid string) (bool, error) {
	removed, err := s.source.Remove(ctx, appID, uid)
	if err != nil {
		return false, platformerr.Wrap(err, platformerr.CodeInternal, "차단을 해제하지 못했어요")
	}
	if removed {
		s.invalidate(appID)
	}
	return removed, nil
}

func (s *Service) invalidate(appID string) {
	s.mu.Lock()
	delete(s.cache, appID)
	s.mu.Unlock()
}
