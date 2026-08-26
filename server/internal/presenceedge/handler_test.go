package presenceedge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/presence"
)

type fakeVerifier struct {
	token presence.Token
	err   error
}

func (f fakeVerifier) Verify(string) (presence.Token, error) { return f.token, f.err }

type fakeRepo struct {
	upserts []Session
	err     error
}

func (f *fakeRepo) Upsert(_ context.Context, session Session) error {
	f.upserts = append(f.upserts, session)
	return f.err
}
func (f *fakeRepo) Ping(context.Context) error { return f.err }

func TestHeartbeatUsesServerTimeAndCoalescesFastDuplicates(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{}
	h := NewHandler(fakeVerifier{token: presence.Token{AppID: "happy-farm", SessionHash: strings.Repeat("a", 64)}}, repo).
		WithClock(func() time.Time { return now })

	for sequence := 1; sequence <= 2; sequence++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/presence/heartbeat",
			strings.NewReader(`{"version":1,"sequence":1,"platform":"ait","appVersion":"1.2.0"}`))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if err := h.heartbeat(rec, req); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	if len(repo.upserts) != 1 {
		t.Fatalf("빠른 중복이 DB에 %d회 기록됐다", len(repo.upserts))
	}
	got := repo.upserts[0]
	if !got.LastSeenAt.Equal(now) || !got.ExpiresAt.Equal(now.Add(presence.ActiveTTL)) {
		t.Fatalf("server time TTL mismatch: %+v", got)
	}
}

func TestHeartbeatReturnsServiceUnavailableWhenDatabaseFails(t *testing.T) {
	repo := &fakeRepo{err: errors.New("db down")}
	h := NewHandler(fakeVerifier{token: presence.Token{AppID: "happy-farm", SessionHash: strings.Repeat("a", 64)}}, repo)
	req := httptest.NewRequest(http.MethodPost, "/v1/presence/heartbeat",
		strings.NewReader(`{"version":1,"sequence":1,"platform":"android"}`))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	err := h.heartbeat(rec, req)
	if err == nil {
		t.Fatal("DB 장애가 성공으로 처리됐다")
	}
	if rec.Header().Get("Retry-After") != "300" {
		t.Fatalf("Retry-After=%q", rec.Header().Get("Retry-After"))
	}
}
