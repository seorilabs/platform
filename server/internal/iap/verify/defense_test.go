package verify

import (
	"context"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
	"testing"
)

type defenseVerifyFixture struct {
	fakeLedger
	snapshots, actions int
	puid               string
	request            ledger.DefenseRequest
}

func (f *defenseVerifyFixture) DefenseSnapshot(_ context.Context, puid string) (ledger.DefenseSnapshot, error) {
	f.snapshots++
	f.puid = puid
	return ledger.DefenseSnapshot{}, nil
}
func (f *defenseVerifyFixture) ActDefense(_ context.Context, puid string, r ledger.DefenseRequest) (ledger.DefenseReceipt, error) {
	f.actions++
	f.puid = puid
	f.request = r
	return ledger.DefenseReceipt{RequestID: r.RequestID, Cost: 25}, nil
}
func TestDefenseServiceKeepsAppScopeAndNoLegacyFallback(t *testing.T) {
	for _, tc := range []struct {
		name, app string
		apps      interface {
			GetUsable(context.Context, string) (registry.App, error)
		}
		scoped bool
		code   platformerr.Code
	}{
		{"foreign app", "other-app", boxApps{app: registry.App{Features: map[string]bool{"iap": true}}}, true, platformerr.CodeProductNotAllowed},
		{"registry unavailable", "mechanical-frontier", nil, true, platformerr.CodeProductNotAllowed},
		{"iap disabled", "mechanical-frontier", boxApps{app: registry.App{AppID: "mechanical-frontier"}}, true, platformerr.CodeProductNotAllowed},
		{"scoped ledger missing", "mechanical-frontier", boxApps{app: registry.App{AppID: "mechanical-frontier", Features: map[string]bool{"iap": true}}}, false, platformerr.CodeRuntimeConfigInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &defenseVerifyFixture{}
			s := &Service{apps: tc.apps, ledger: f}
			if tc.scoped {
				s.appLedgers = map[string]Ledger{"mechanical-frontier": f}
			}
			if _, e := s.DefenseSnapshot(context.Background(), tc.app, "owner"); platformerr.CodeOf(e) != tc.code {
				t.Fatalf("snapshot err=%v", e)
			}
			if _, e := s.ActDefense(context.Background(), tc.app, "owner", ledger.DefenseRequest{}); platformerr.CodeOf(e) != tc.code {
				t.Fatalf("action err=%v", e)
			}
			if f.snapshots+f.actions != 0 {
				t.Fatal("rejected scope reached ledger")
			}
		})
	}
	f := &defenseVerifyFixture{}
	s := &Service{apps: boxApps{app: registry.App{AppID: "mechanical-frontier", Features: map[string]bool{"iap": true}}}, appLedgers: map[string]Ledger{"mechanical-frontier": f}}
	req := ledger.DefenseRequest{RequestID: "action_0001", ActionID: "damage", CatalogVersion: "defense-1"}
	if _, e := s.DefenseSnapshot(context.Background(), "mechanical-frontier", "session-owner"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ActDefense(context.Background(), "mechanical-frontier", "session-owner", req); e != nil || f.puid != "session-owner" || f.request != req || f.actions != 1 || f.snapshots != 1 {
		t.Fatalf("routing %+v err=%v", f, e)
	}
}
