package inbox

import (
	"context"
	"errors"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type sessionStub struct {
	app, puid string
	err       error
}

func (s sessionStub) Authenticate(*http.Request) (identity.Session, error) {
	return identity.Session{AppID: s.app, PlatformUserID: s.puid}, s.err
}

type appsStub struct{ enabled bool }

func (a appsStub) GetUsable(context.Context, string) (registry.App, error) {
	return registry.App{Features: map[string]bool{"inbox": a.enabled}}, nil
}

type mailStub struct {
	puid  string
	calls int
}

func (m *mailStub) ListInbox(_ context.Context, puid, _ string) (ledger.InboxPage, error) {
	m.puid = puid
	return ledger.InboxPage{Messages: []ledger.InboxMessage{}}, nil
}
func (m *mailStub) ReadInbox(_ context.Context, puid, id string) (ledger.InboxMessage, error) {
	m.puid = puid
	m.calls++
	return ledger.InboxMessage{ID: id, ReadAt: 1}, nil
}
func (m *mailStub) ClaimInbox(_ context.Context, puid, id string) (ledger.InboxMessage, error) {
	m.puid = puid
	m.calls++
	if id == "bad" {
		return ledger.InboxMessage{}, errors.New("fixture failure")
	}
	return ledger.InboxMessage{ID: id, ClaimedAt: 1}, nil
}
func TestSessionScopeAndStrictClaims(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		enabled    bool
		auth       error
		status     int
		calls      int
	}{
		{"valid", "{}", true, nil, 200, 1},
		{"reward injection", `{"quantity":100}`, true, nil, 400, 0},
		{"user injection", `{"platformUserId":"other"}`, true, nil, 400, 0},
		{"not adopted", "{}", false, nil, 403, 0},
		{"expired", "{}", true, platformerr.New(platformerr.CodeSessionExpired, "expired"), 401, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &mailStub{}
			mux := http.NewServeMux()
			NewHandler(sessionStub{"game", "owner", tc.auth}, appsStub{tc.enabled}, func(registry.App) Mailbox { return m }).Register(mux)
			r := httptest.NewRequest("POST", "/v1/inbox/welcome/claim", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.status || m.calls != tc.calls {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, m.calls, w.Body.String())
			}
			if m.calls > 0 && m.puid != "owner" {
				t.Fatal("session owner lost")
			}
		})
	}
}
func TestBatchPartialFailure(t *testing.T) {
	m := &mailStub{}
	mux := http.NewServeMux()
	NewHandler(sessionStub{"game", "owner", nil}, appsStub{true}, func(registry.App) Mailbox { return m }).Register(mux)
	r := httptest.NewRequest("POST", "/v1/inbox/claim-batch", strings.NewReader(`{"ids":["good","bad"]}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 || m.calls != 2 || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatal(w.Body.String())
	}
}
