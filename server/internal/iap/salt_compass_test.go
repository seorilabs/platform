package iap

import (
	"context"
	"net/http"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/ledger"
)

type saltHTTPService struct {
	fakeService
	calls int
}

func (s *saltHTTPService) SaltSnapshot(_ context.Context, app, puid string) (ledger.SaltSnapshot, error) {
	s.gotAppID, s.gotPUID = app, puid
	s.calls++
	return ledger.SaltSnapshot{}, nil
}
func (s *saltHTTPService) TransactSalt(_ context.Context, app, puid string, _ ledger.SaltRequest) (ledger.SaltReceipt, error) {
	s.gotAppID, s.gotPUID = app, puid
	s.calls++
	return ledger.SaltReceipt{}, nil
}
func TestSaltHTTPRejectsInjectedAuthorityAndUsesAuthenticatedOwner(t *testing.T) {
	s := &saltHTTPService{}
	sess := paidSession()
	sess.AppID = "salt-compass"
	h := NewHandler(s, &fakeSessions{sess: sess})
	for _, field := range []string{`"platformUserId":"other"`, `"credits":280`, `"rewardVerified":true`, `"sealBalance":999`} {
		w := serve(t, h, http.MethodPost, "/v1/iap/salt-compass/transactions", `{"requestId":"x","action":"bootstrap",`+field+`}`)
		if w.Code != http.StatusBadRequest || s.calls != 0 {
			t.Fatalf("injected field reached service: %s status=%d calls=%d", field, w.Code, s.calls)
		}
	}
	w := serve(t, h, http.MethodGet, "/v1/iap/salt-compass", "")
	if w.Code != http.StatusOK || s.gotAppID != sess.AppID || s.gotPUID != sess.PlatformUserID {
		t.Fatalf("authenticated owner mismatch status=%d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("financial snapshot may be cached")
	}
	sess.IsAnonymous = true
	h = NewHandler(s, &fakeSessions{sess: sess})
	w = serve(t, h, http.MethodGet, "/v1/iap/salt-compass", "")
	if w.Code == http.StatusOK || s.calls != 1 {
		t.Fatal("anonymous session reached financial state")
	}
}
