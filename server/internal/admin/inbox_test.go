package admin

import (
	"context"
	"encoding/json"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/registry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type issueStub struct {
	calls int
	input ledger.InboxIssue
}

func (s *issueStub) IssueInbox(_ context.Context, in ledger.InboxIssue) (ledger.InboxMessage, error) {
	s.calls++
	s.input = in
	return ledger.InboxMessage{ID: in.RequestID}, nil
}
func (s *issueStub) CheckAdminMutationRate(context.Context, string) error { return nil }
func TestInboxIssueAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, principal, userApp, reward, confirm string
		enabled                                   bool
		status                                    int
	}{
		{"write", "write@example.test", "game", "starter", "ISSUE INBOX game " + testPUID, true, 200},
		{"read identity", "read@example.test", "game", "starter", "ISSUE INBOX game " + testPUID, true, 403},
		{"cross game", "write@example.test", "other", "starter", "ISSUE INBOX game " + testPUID, true, 403},
		{"other reward", "write@example.test", "game", "other", "ISSUE INBOX game " + testPUID, true, 422},
		{"wrong confirmation", "write@example.test", "game", "starter", "wrong", true, 400},
		{"disabled", "write@example.test", "game", "starter", "ISSUE INBOX game " + testPUID, false, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, e := NewAuthenticator(&fakeValidator{email: tc.principal}, []string{"read@example.test"}, []string{"write@example.test"})
			if e != nil {
				t.Fatal(e)
			}
			app := registry.App{AppID: "game", Status: registry.StatusActive, Features: map[string]bool{"inbox": tc.enabled}, IAP: registry.IAPConfig{EntitlementIDs: []string{"starter"}}}
			users := &fakeUsers{user: identity.SupportUser{PlatformUserID: testPUID, AppID: tc.userApp}}
			service := &issueStub{}
			mux := http.NewServeMux()
			RegisterInbox(mux, auth, &fakeApps{app: app}, users, nil, func(registry.App) InboxIssuer { return service })
			payload, _ := json.Marshal(map[string]any{"requestId": "welcome", "platformUserId": testPUID, "title": "Welcome", "body": "Reward", "rewards": []ledger.InboxReward{{Kind: "entitlement", EntitlementID: tc.reward, Quantity: 1}}, "reason": "internal_validation", "confirmation": tc.confirm})
			r := httptest.NewRequest("POST", "/v1/admin/apps/game/inbox", strings.NewReader(string(payload)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer fixture")
			r.Header.Set("X-Seori-Actor", "spoofed")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d expected=%d %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status == 200 {
				if service.calls != 1 || !strings.HasPrefix(service.input.Actor, "oidc_sha256:") {
					t.Fatal("actor not bound to OIDC", service.input.Actor)
				}
			} else if service.calls != 0 {
				t.Fatal("unauthorized issue executed")
			}
		})
	}
}
