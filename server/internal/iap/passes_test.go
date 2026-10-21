package iap

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type fakePassService struct {
	fakeService
	gotEntitlement, gotRequest string
	claimErr                   error
}

func (f *fakePassService) PassSnapshot(_ context.Context, appID, puid string) (verify.PassSnapshot, error) {
	f.gotAppID, f.gotPUID = appID, puid
	return verify.PassSnapshot{ServerTime: 100, Passes: []ledger.PassStatus{{EntitlementID: "amber_pass_30d", Claimable: true}}}, nil
}

func (f *fakePassService) ClaimPass(_ context.Context, appID, puid, ent, requestID string) (ledger.PassReceipt, error) {
	f.gotAppID, f.gotPUID, f.gotEntitlement, f.gotRequest = appID, puid, ent, requestID
	if f.claimErr != nil {
		return ledger.PassReceipt{}, f.claimErr
	}
	return ledger.PassReceipt{RequestID: requestID, Applied: true, EntitlementID: ent, Days: 1, Amount: 50}, nil
}

func TestPassSnapshotAndClaim(t *testing.T) {
	svc := &fakePassService{}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})

	var snap struct {
		Result verify.PassSnapshot `json:"result"`
	}
	w := serve(t, h, http.MethodGet, "/v1/iap/passes", "")
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil || w.Code != http.StatusOK ||
		svc.gotPUID != paidSession().PlatformUserID || len(snap.Result.Passes) != 1 {
		t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
	var receipt struct {
		Result ledger.PassReceipt `json:"result"`
	}
	w = serve(t, h, http.MethodPost, "/v1/iap/passes/claim", `{"entitlementId":"amber_pass_30d","requestId":"claim-req-1"}`)
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || w.Code != http.StatusOK ||
		svc.gotEntitlement != "amber_pass_30d" || svc.gotRequest != "claim-req-1" || receipt.Result.Amount != 50 {
		t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}

	svc.claimErr = platformerr.New(platformerr.CodePassAlreadyClaimed, "x")
	w = serve(t, h, http.MethodPost, "/v1/iap/passes/claim", `{"entitlementId":"amber_pass_30d","requestId":"claim-req-2"}`)
	if w.Code != http.StatusConflict || errorCode(t, w) != string(platformerr.CodePassAlreadyClaimed) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

// 지급량, 날짜, 사용자를 요청으로 주입할 수 없다. 불변식 8.
func TestPassClaimRejectsInjectedFields(t *testing.T) {
	svc := &fakePassService{}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})
	for _, body := range []string{
		`{"entitlementId":"amber_pass_30d","requestId":"claim-req-1","amount":500}`,
		`{"entitlementId":"amber_pass_30d","requestId":"claim-req-1","platformUserId":"pu_other"}`,
		`{"entitlementId":"amber_pass_30d","requestId":"claim-req-1","day":3}`,
	} {
		w := serve(t, h, http.MethodPost, "/v1/iap/passes/claim", body)
		if w.Code != http.StatusBadRequest || svc.gotRequest != "" {
			t.Fatalf("body=%s status=%d", body, w.Code)
		}
	}
}

// 익명 세션은 조회도 수령도 못 하고, 연결 계정을 요구하는 앱은 게스트를 막는다.
func TestPassRequiresPayingSession(t *testing.T) {
	anon := paidSession()
	anon.IsAnonymous = true
	h := NewHandler(&fakePassService{}, &fakeSessions{sess: anon})
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/iap/passes", ""},
		{http.MethodPost, "/v1/iap/passes/claim", `{"entitlementId":"amber_pass_30d","requestId":"claim-req-1"}`},
	} {
		w := serve(t, h, req.method, req.path, req.body)
		if w.Code != http.StatusForbidden || errorCode(t, w) != string(platformerr.CodeAnonymousNotAllowed) {
			t.Fatalf("%s %s: %d %s", req.method, req.path, w.Code, w.Body.String())
		}
	}
	app := registry.App{AppID: "lizard-tycoon"}
	app.IAP.RequireLinkedAccount = true
	h = NewHandler(&fakePassService{}, &fakeSessions{sess: paidSession()}).WithApps(&fakeApps{app: app})
	w := serve(t, h, http.MethodGet, "/v1/iap/passes", "")
	if errorCode(t, w) != string(platformerr.CodeAccountLinkRequired) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestPassUnsupportedService(t *testing.T) {
	h := NewHandler(&fakeService{}, &fakeSessions{sess: paidSession()})
	w := serve(t, h, http.MethodGet, "/v1/iap/passes", "")
	if errorCode(t, w) != string(platformerr.CodeProductNotAllowed) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
