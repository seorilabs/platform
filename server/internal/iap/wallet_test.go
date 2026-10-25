package iap

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type fakeWalletService struct {
	fakeService
	appID string
	puid  string
}

func (f *fakeWalletService) WalletSnapshot(_ context.Context, appID, puid string) (map[string]int, error) {
	f.appID, f.puid = appID, puid
	return map[string]int{"starter_test_ticket_1": 2}, nil
}

func TestWalletUsesOnlyAuthenticatedIdentity(t *testing.T) {
	sess := paidSession()
	sess.AppID = "starter-game"
	svc := &fakeWalletService{}
	h := NewHandler(svc, &fakeSessions{sess: sess})
	w := serve(t, h, http.MethodGet, "/v1/iap/wallet", "")
	if w.Code != http.StatusOK || svc.appID != sess.AppID || svc.puid != sess.PlatformUserID || !strings.Contains(w.Body.String(), `"starter_test_ticket_1":2`) {
		t.Fatalf("wallet did not use session identity: status=%d response=%s", w.Code, w.Body.String())
	}
}

func TestWalletRejectsAnonymousAndIdentityInjection(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		anonymous   bool
	}{
		{name: "anonymous", anonymous: true},
		{name: "another user", query: "?puid=another-user"},
		{name: "client amount", query: "?amount=10000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := paidSession()
			sess.AppID = "starter-game"
			sess.IsAnonymous = tc.anonymous
			svc := &fakeWalletService{}
			w := serve(t, NewHandler(svc, &fakeSessions{sess: sess}), http.MethodGet, "/v1/iap/wallet"+tc.query, "")
			if (w.Code != http.StatusBadRequest && w.Code != http.StatusForbidden) || svc.puid != "" {
				t.Fatalf("untrusted wallet request reached service: status=%d response=%s", w.Code, w.Body.String())
			}
		})
	}
}
