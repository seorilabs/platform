package blocklist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"cloud.google.com/go/firestore"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/store"
)

// blocksCollection은 앱별 차단 목록의 루트 컬렉션이다.
//
// 경로는 blocks/{appID}/uids/{uid}다. uid 하나가 문서 하나인 이유는
// 배열 한 문서로 두면 두 운영자가 동시에 차단할 때 read-modify-write가
// 서로를 덮어쓰기 때문이다. 차단이 사라지는 방향의 사고는 조용하다.
const blocksCollection = "blocks"

// uidsCollection은 앱 문서 아래의 차단 uid 하위 컬렉션이다.
const uidsCollection = "uids"

// StoreSource는 Firestore에서 차단 기록을 읽고 쓴다. 런타임 기본 경로다.
type StoreSource struct {
	store *store.Client
}

// NewStoreSource는 Firestore 기반 소스를 만든다.
func NewStoreSource(s *store.Client) *StoreSource {
	return &StoreSource{store: s}
}

func collectionPath(appID string) (fspath.Path, error) {
	return fspath.Parse(blocksCollection + "/" + appID + "/" + uidsCollection)
}

func docPath(appID, uid string) (fspath.Path, error) {
	return fspath.Parse(blocksCollection + "/" + appID + "/" + uidsCollection + "/" + uid)
}

func (s *StoreSource) LoadUIDs(ctx context.Context, appID string) (map[string]struct{}, error) {
	p, err := collectionPath(appID)
	if err != nil {
		return nil, fmt.Errorf("blocklist: 경로 파싱 실패: %w", err)
	}

	it, err := s.store.Query(ctx, p, nil)
	if err != nil {
		return nil, fmt.Errorf("blocklist: 조회 실패: %w", err)
	}
	defer it.Stop()

	uids := map[string]struct{}{}
	for {
		snap, err := it.Next()
		if store.IsDone(err) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("blocklist: 순회 실패: %w", err)
		}
		uids[snap.Ref.ID] = struct{}{}
	}
	return uids, nil
}

func (s *StoreSource) ListEntries(ctx context.Context, appID string) ([]Entry, error) {
	p, err := collectionPath(appID)
	if err != nil {
		return nil, fmt.Errorf("blocklist: 경로 파싱 실패: %w", err)
	}

	it, err := s.store.Query(ctx, p, func(q firestore.Query) firestore.Query {
		return q.OrderBy("blocked_at", firestore.Desc)
	})
	if err != nil {
		return nil, fmt.Errorf("blocklist: 조회 실패: %w", err)
	}
	defer it.Stop()

	entries := []Entry{}
	for {
		snap, err := it.Next()
		if store.IsDone(err) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("blocklist: 순회 실패: %w", err)
		}
		var e Entry
		if err := snap.DataTo(&e); err != nil {
			// 한 건이 깨져도 나머지는 보여준다. 운영 화면이 통째로 비면
			// 차단이 하나도 없는 것과 구분되지 않는다.
			slog.ErrorContext(ctx, "차단 기록 변환 실패. 건너뛴다",
				"app_id", appID, "doc", snap.Ref.ID, "err", err)
			continue
		}
		if e.UID == "" {
			e.UID = snap.Ref.ID
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (s *StoreSource) Put(ctx context.Context, appID string, e Entry) error {
	p, err := docPath(appID, e.UID)
	if err != nil {
		return fmt.Errorf("blocklist: 경로 파싱 실패: %w", err)
	}
	return s.store.Set(ctx, p, e)
}

func (s *StoreSource) Remove(ctx context.Context, appID, uid string) (bool, error) {
	p, err := docPath(appID, uid)
	if err != nil {
		return false, fmt.Errorf("blocklist: 경로 파싱 실패: %w", err)
	}

	// 존재 확인을 먼저 한다. Firestore Delete는 없는 문서에도 성공하므로
	// 운영자에게 "해제했다"와 "원래 없었다"를 구분해 보여줄 수 없다.
	if _, err := s.store.Get(ctx, p); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("blocklist: 조회 실패: %w", err)
	}
	if err := s.store.Delete(ctx, p); err != nil {
		return false, fmt.Errorf("blocklist: 삭제 실패: %w", err)
	}
	return true, nil
}
