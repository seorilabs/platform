package iap

import (
	"context"
	"encoding/json"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"net/http"
	"testing"
)

type defenseHTTPFixture struct {
	fakeService
	snapshots, actions int
	request            ledger.DefenseRequest
}

func (f *defenseHTTPFixture) DefenseSnapshot(_ context.Context, app, puid string) (ledger.DefenseSnapshot, error) {
	f.gotAppID, f.gotPUID = app, puid
	f.snapshots++
	return ledger.DefenseSnapshot{State: ledger.DefenseState{Balance: 100, Enabled: true, Upgrades: map[string]int{}, Weapons: []string{}}}, f.err
}
func (f *defenseHTTPFixture) ActDefense(_ context.Context, app, puid string, r ledger.DefenseRequest) (ledger.DefenseReceipt, error) {
	f.gotAppID, f.gotPUID = app, puid
	f.actions++
	f.request = r
	return ledger.DefenseReceipt{RequestID: r.RequestID, ActionID: r.ActionID, CatalogVersion: r.CatalogVersion, RunID: r.RunID, Cost: 25}, f.err
}

const defenseBody = `{"requestId":"action_0001","actionId":"damage","catalogVersion":"defense-1"}`

func TestDefenseHTTPRequiresLinkedAuthenticatedAccount(t *testing.T) {
	for _, route := range []struct{ method, path, body string }{{http.MethodGet, "/v1/iap/defense", ""}, {http.MethodPost, "/v1/iap/defense/actions", defenseBody}} {
		for _, linked := range []bool{false, true} {
			sess := paidSession()
			sess.AppID = "mechanical-frontier"
			sess.IsLinkedAccount = linked
			f := &defenseHTTPFixture{}
			w := serve(t, NewHandler(f, &fakeSessions{sess: sess}), route.method, route.path, route.body)
			if !linked {
				if w.Code != 403 || errorCode(t, w) != string(platformerr.CodeAccountLinkRequired) || f.actions+f.snapshots != 0 {
					t.Fatalf("unlinked route %s: %d %s", route.path, w.Code, w.Body.String())
				}
				continue
			}
			if w.Code != 200 || f.actions+f.snapshots != 1 || f.gotAppID != "mechanical-frontier" || f.gotPUID != sess.PlatformUserID {
				t.Fatalf("linked route %s: %d %s fixture=%+v", route.path, w.Code, w.Body.String(), f)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("financial response was cacheable")
			}
		}
		sess := paidSession()
		sess.IsAnonymous = true
		sess.IsLinkedAccount = true
		f := &defenseHTTPFixture{}
		w := serve(t, NewHandler(f, &fakeSessions{sess: sess}), route.method, route.path, route.body)
		if w.Code != 403 || f.actions+f.snapshots != 0 {
			t.Fatalf("anonymous route %s: %s", route.path, w.Body.String())
		}
	}
}
func TestDefenseHTTPRejectsClientPricesAndOwnership(t *testing.T) {
	for _, field := range []string{`"cost":0`, `"balance":99999`, `"amount":100`, `"platformUserId":"another-owner"`, `"upgrades":{"damage":10000}`} {
		sess := paidSession()
		sess.IsLinkedAccount = true
		f := &defenseHTTPFixture{}
		body := defenseBody[:len(defenseBody)-1] + "," + field + "}"
		w := serve(t, NewHandler(f, &fakeSessions{sess: sess}), http.MethodPost, "/v1/iap/defense/actions", body)
		if w.Code != 400 || f.actions != 0 {
			t.Fatalf("authority injection %s accepted: %s", field, w.Body.String())
		}
	}
	sess := paidSession()
	sess.IsLinkedAccount = true
	f := &defenseHTTPFixture{}
	w := serve(t, NewHandler(f, &fakeSessions{sess: sess}), http.MethodPost, "/v1/iap/defense/actions", defenseBody)
	var env struct {
		OK     bool                  `json:"ok"`
		Result ledger.DefenseReceipt `json:"result"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &env); e != nil || !env.OK || env.Result.Cost != 25 || f.request.ActionID != "damage" {
		t.Fatalf("server receipt lost: %s err=%v", w.Body.String(), e)
	}
}

func TestDefenseHTTPInsufficientBalanceRemainsForbidden(t *testing.T) {
	sess := paidSession()
	sess.IsLinkedAccount = true
	fixture := &defenseHTTPFixture{fakeService: fakeService{err: platformerr.New(platformerr.CodeContentTicketEmpty, "insufficient funds")}}
	w := serve(t, NewHandler(fixture, &fakeSessions{sess: sess}), http.MethodPost, "/v1/iap/defense/actions", defenseBody)
	if w.Code != http.StatusForbidden || errorCode(t, w) != string(platformerr.CodeContentTicketEmpty) || fixture.actions != 1 {
		t.Fatalf("insufficient balance response %d %s", w.Code, w.Body.String())
	}
}
