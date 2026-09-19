package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/blocklist"
)

// fakeBlocks는 차단 저장소를 메모리로 흉내낸다.
type fakeBlocks struct {
	entries map[string][]blocklist.Entry
	err     error
}

func newFakeBlocks() *fakeBlocks {
	return &fakeBlocks{entries: map[string][]blocklist.Entry{}}
}

func (f *fakeBlocks) List(_ context.Context, appID string) ([]blocklist.Entry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entries[appID], nil
}

func (f *fakeBlocks) Block(_ context.Context, appID, uid, reason, actor string) (blocklist.Entry, error) {
	if f.err != nil {
		return blocklist.Entry{}, f.err
	}
	e := blocklist.Entry{UID: uid, Reason: reason, BlockedBy: actor, BlockedAt: time.Unix(1700000000, 0).UTC()}
	f.entries[appID] = append(f.entries[appID], e)
	return e, nil
}

func (f *fakeBlocks) Unblock(_ context.Context, appID, uid string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	for i, e := range f.entries[appID] {
		if e.UID == uid {
			f.entries[appID] = append(f.entries[appID][:i], f.entries[appID][i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func serveBlocks(
	t *testing.T,
	blocks Blocks,
	apps Apps,
	auditor *fakeAuditor,
	method, path, body, email string,
) *httptest.ResponseRecorder {
	t.Helper()

	auth, err := NewAuthenticator(&fakeValidator{email: email},
		[]string{backofficeReadSA}, []string{backofficeSA})
	if err != nil {
		t.Fatalf("인증기 생성 실패: %v", err)
	}

	mux := http.NewServeMux()
	if err := RegisterBlocks(mux, auth, blocks, apps, auditor); err != nil {
		t.Fatalf("차단 API 등록 실패: %v", err)
	}

	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestBlockAndListAndUnblock(t *testing.T) {
	blocks := newFakeBlocks()
	auditor := &fakeAuditor{}

	w := serveBlocks(t, blocks, &fakeApps{}, auditor,
		http.MethodPost, "/v1/admin/apps/ungeul/blocks", `{"uid":"uid-1","reason":"어뷰징"}`, backofficeSA)
	if ok, result, code := decodeEnvelope(t, w); !ok {
		t.Fatalf("차단 실패: status=%d code=%s", w.Code, code)
	} else if result["uid"] != "uid-1" {
		t.Errorf("uid = %v, want uid-1", result["uid"])
	}

	w = serveBlocks(t, blocks, &fakeApps{}, auditor,
		http.MethodGet, "/v1/admin/apps/ungeul/blocks", "", backofficeReadSA)
	ok, result, code := decodeEnvelope(t, w)
	if !ok {
		t.Fatalf("목록 조회 실패: status=%d code=%s", w.Code, code)
	}
	if list, _ := result["blocks"].([]any); len(list) != 1 {
		t.Fatalf("blocks = %v, want 1건", result["blocks"])
	}

	w = serveBlocks(t, blocks, &fakeApps{}, auditor,
		http.MethodDelete, "/v1/admin/apps/ungeul/blocks/uid-1", "", backofficeSA)
	ok, result, code = decodeEnvelope(t, w)
	if !ok {
		t.Fatalf("해제 실패: status=%d code=%s", w.Code, code)
	}
	if result["removed"] != true {
		t.Errorf("removed = %v, want true", result["removed"])
	}

	if len(auditor.records) != 2 {
		t.Fatalf("감사 기록 = %d건, want 2", len(auditor.records))
	}
	for _, rec := range auditor.records {
		if rec.action != "identity.block" {
			t.Errorf("action = %q, want identity.block", rec.action)
		}
		if rec.detail["uid"] != "uid-1" {
			t.Errorf("detail uid = %v, want uid-1", rec.detail["uid"])
		}
	}
	if auditor.records[0].outcome != "blocked" || auditor.records[1].outcome != "unblocked" {
		t.Errorf("outcome = %q, %q", auditor.records[0].outcome, auditor.records[1].outcome)
	}
}

// 조회 자격증명으로는 차단을 넣을 수 없다. 조회 키가 유출돼도
// 원장을 바꿀 수 없어야 한다.
func TestBlockRequiresWriteAccess(t *testing.T) {
	w := serveBlocks(t, newFakeBlocks(), &fakeApps{}, &fakeAuditor{},
		http.MethodPost, "/v1/admin/apps/ungeul/blocks", `{"uid":"uid-1"}`, backofficeReadSA)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// 존재하지 않는 앱에는 차단을 쌓지 못한다. 오타로 만들어진 컬렉션은
// 아무 요청도 읽지 않으므로 차단한 줄 알고 넘어가게 된다.
func TestBlockRejectsUnknownApp(t *testing.T) {
	apps := &fakeApps{err: errors.New("없는 앱")}
	w := serveBlocks(t, newFakeBlocks(), apps, &fakeAuditor{},
		http.MethodPost, "/v1/admin/apps/ungeul/blocks", `{"uid":"uid-1"}`, backofficeSA)
	if ok, _, _ := decodeEnvelope(t, w); ok {
		t.Error("없는 앱에 차단을 넣었다")
	}
}

func TestBlockRejectsInvalidUID(t *testing.T) {
	for _, uid := range []string{"", "uid 1", "uid/1", strings.Repeat("a", 129)} {
		w := serveBlocks(t, newFakeBlocks(), &fakeApps{}, &fakeAuditor{},
			http.MethodPost, "/v1/admin/apps/ungeul/blocks", `{"uid":"`+uid+`"}`, backofficeSA)
		if ok, _, code := decodeEnvelope(t, w); ok {
			t.Errorf("uid=%q를 통과시켰다", uid)
		} else if code != "request_invalid" {
			t.Errorf("uid=%q code = %q, want request_invalid", uid, code)
		}
	}
}

// 차단돼 있지 않은 계정의 해제는 200이고 removed=false다. 404를 보면
// 운영자가 다른 앱을 잘못 본 것으로 오해해 조작을 반복한다.
func TestUnblockMissingReturnsFalse(t *testing.T) {
	auditor := &fakeAuditor{}
	w := serveBlocks(t, newFakeBlocks(), &fakeApps{}, auditor,
		http.MethodDelete, "/v1/admin/apps/ungeul/blocks/uid-missing", "", backofficeSA)

	ok, result, code := decodeEnvelope(t, w)
	if !ok {
		t.Fatalf("해제 실패: status=%d code=%s", w.Code, code)
	}
	if result["removed"] != false {
		t.Errorf("removed = %v, want false", result["removed"])
	}
	if len(auditor.records) != 1 || auditor.records[0].outcome != "not_blocked" {
		t.Errorf("감사 기록 = %+v", auditor.records)
	}
}
