package iap

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type fakeBoxService struct {
	fakeService
	opened []string
}

func (f *fakeBoxService) BoxSnapshot(context.Context, string, string) (verify.BoxSnapshot, error) {
	return verify.BoxSnapshot{State: ledger.BoxState{Available: 2}}, nil
}

func (f *fakeBoxService) OpenBox(_ context.Context, _, puid, requestID string) (ledger.BoxReceipt, error) {
	f.opened = append(f.opened, puid+"/"+requestID)
	return ledger.BoxReceipt{RequestID: requestID, Applied: true, FriendID: "B19"}, nil
}

// 확률은 구매 전에도 보여야 하므로 연결 전 게스트도 조회할 수 있고 linked가 응답에 실린다.
func TestBoxSnapshotIsReadableBeforeLinking(t *testing.T) {
	svc := &fakeBoxService{}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})
	w := serve(t, h, http.MethodGet, "/v1/iap/boxes", "")
	var body struct {
		Result verify.BoxSnapshot `json:"result"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Result.State.Available != 2 || body.Result.Linked {
		t.Fatalf("response=%s", w.Body.String())
	}
	anon := paidSession()
	anon.IsAnonymous = true
	h = NewHandler(svc, &fakeSessions{sess: anon})
	if code := errorCode(t, serve(t, h, http.MethodGet, "/v1/iap/boxes", "")); code != string(platformerr.CodeAnonymousNotAllowed) {
		t.Fatalf("anonymous code=%s", code)
	}
}

// 개봉은 결제 세션 규칙(연결 계정 요구)을 따르고, 친구·수량 같은 권한 필드를 받지 않는다.
func TestBoxOpenRequiresLinkedSessionAndRejectsInjectedFields(t *testing.T) {
	svc := &fakeBoxService{}
	app := registry.App{AppID: "bloomhand", Features: map[string]bool{"iap": true}}
	app.IAP.RequireLinkedAccount = true
	h := NewHandler(svc, &fakeSessions{sess: paidSession()}).WithApps(&fakeApps{app: app})
	if code := errorCode(t, serve(t, h, http.MethodPost, "/v1/iap/boxes/open", `{"requestId":"open-req-1"}`)); code != string(platformerr.CodeAccountLinkRequired) {
		t.Fatalf("unlinked code=%s", code)
	}
	linked := paidSession()
	linked.IsLinkedAccount = true
	h = NewHandler(svc, &fakeSessions{sess: linked}).WithApps(&fakeApps{app: app})
	if w := serve(t, h, http.MethodPost, "/v1/iap/boxes/open", `{"requestId":"open-req-1","friendId":"B20","count":5}`); w.Code != 400 {
		t.Fatalf("injected fields accepted: %s", w.Body.String())
	}
	if w := serve(t, h, http.MethodPost, "/v1/iap/boxes/open", `{"requestId":"open-req-1"}`); w.Code != 200 || len(svc.opened) != 1 {
		t.Fatalf("response=%s opened=%v", w.Body.String(), svc.opened)
	}
}

// Firebase bridge 게스트는 등록·공급자 연결 없이 구매 검증과 개봉에 도달한다.
// 단순 익명 헤더 세션(IsAnonymous)은 기존 규칙대로 거부한다.
func TestBloomhandGuestCanPurchaseAndOpenBoxes(t *testing.T) {
	apps, err := registry.NewFSSource(os.DirFS("../../../registry"), "apps").LoadApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var app registry.App
	for _, candidate := range apps {
		if candidate.AppID == "bloomhand" {
			app = candidate
		}
	}
	if app.AppID == "" {
		t.Fatal("missing Bloomhand registry")
	}
	guest := paidSession()
	guest.AppID = "bloomhand"
	guest.IsLinkedAccount = false
	svc := &fakeBoxService{}
	h := NewHandler(svc, &fakeSessions{sess: guest}).WithApps(&fakeApps{app: app})
	for _, req := range []struct{ path, body string }{
		{"/v1/iap/verify", `{"platform":"app_store","productId":"bloomhand_friend_box_1","token":"fixture-transaction"}`},
		{"/v1/iap/boxes/open", `{"requestId":"guest-open-1"}`},
	} {
		if w := serve(t, h, http.MethodPost, req.path, req.body); w.Code != http.StatusOK {
			t.Fatalf("guest denied: %s %s", req.path, w.Body.String())
		}
	}
	if svc.gotPUID != guest.PlatformUserID || len(svc.opened) != 1 {
		t.Fatal("guest identity not passed to verification/open")
	}
}
