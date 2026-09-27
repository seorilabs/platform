package iap

import (
	"context"
	"net/http"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

type fakeEconomyService struct {
	fakeService
	freeCalled, paidCalled bool
	linkedPurchase         bool
}

func (f *fakeEconomyService) EconomySnapshot(context.Context, string, string) (ledger.EconomySnapshot, error) {
	return ledger.EconomySnapshot{}, nil
}
func (f *fakeEconomyService) TransactEconomy(context.Context, string, string, ledger.EconomyRequest) (ledger.EconomyReceipt, error) {
	f.paidCalled = true
	return ledger.EconomyReceipt{}, nil
}
func (f *fakeEconomyService) TransactFreeEconomy(context.Context, string, string, ledger.EconomyRequest) (ledger.EconomyReceipt, error) {
	f.freeCalled = true
	return ledger.EconomyReceipt{}, nil
}
func (f *fakeEconomyService) RequiresLinkedAccount(string, domain.Platform, string) (bool, error) {
	return f.linkedPurchase, nil
}
func TestEconomyHTTPUsesAuthenticatedSpendingPolicy(t *testing.T) {
	for _, linked := range []bool{false, true} {
		sess := paidSession()
		sess.IsLinkedAccount = linked
		svc := &fakeEconomyService{}
		h := NewHandler(svc, &fakeSessions{sess: sess})
		w := serve(t, h, http.MethodPost, "/v1/iap/economy/transactions", `{"requestId":"request_1234567890","action":"draw","collectionId":"tidal"}`)
		if w.Code != 200 || svc.paidCalled != linked || svc.freeCalled == linked {
			t.Fatalf("linked=%v response=%s service=%+v", linked, w.Body.String(), svc)
		}
	}
}
func TestEconomyHTTPRejectsInjectedPaidAuthority(t *testing.T) {
	svc := &fakeEconomyService{}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})
	w := serve(t, h, http.MethodPost, "/v1/iap/economy/transactions", `{"requestId":"request_1234567890","action":"draw","freeOnly":false,"linked":true}`)
	if w.Code != 400 || svc.paidCalled || svc.freeCalled {
		t.Fatalf("response=%s", w.Body.String())
	}
}
func TestNewEconomyPurchaseRequiresLinkedSessionBeforeVerification(t *testing.T) {
	svc := &fakeEconomyService{linkedPurchase: true}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})
	w := serve(t, h, http.MethodPost, "/v1/iap/verify", validBody)
	if errorCode(t, w) != string(platformerr.CodeAccountLinkRequired) || svc.gotPUID != "" {
		t.Fatalf("response=%s", w.Body.String())
	}
}
