package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
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

func defenseRegistryFixture() registry.App {
	app := registry.App{AppID: "mechanical-frontier", Features: map[string]bool{"iap": true}}
	for _, units := range []int{100, 550, 1150, 2400, 6200} {
		app.IAP.EntitlementIDs = append(app.IAP.EntitlementIDs, fmt.Sprintf("defense_crystals_%d", units))
	}
	return app
}
func defenseSKUFixture(t *testing.T, appID, omit string) *catalog.Catalog {
	t.Helper()
	entitlements := map[string]any{}
	for _, units := range []int{100, 550, 1150, 2400, 6200} {
		id := fmt.Sprintf("defense_crystals_%d", units)
		if id == omit {
			continue
		}
		sku := fmt.Sprintf("crystals_%d", units)
		entitlements[id] = map[string]any{"type": "consumable", "google_play": sku, "app_store": sku}
	}
	raw, e := json.Marshal(map[string]any{"version": 2, "apps": map[string]any{appID: map[string]any{"entitlements": entitlements}}})
	if e != nil {
		t.Fatal(e)
	}
	result, e := catalog.Parse(raw, nil)
	if e != nil {
		t.Fatal(e)
	}
	return result
}
func TestDefenseServiceKeepsAppScopeAndNoLegacyFallback(t *testing.T) {
	enabled := defenseRegistryFixture()
	for _, tc := range []struct {
		name, app string
		apps      interface {
			GetUsable(context.Context, string) (registry.App, error)
		}
		scoped bool
		code   platformerr.Code
	}{
		{"foreign app", "other-app", boxApps{app: enabled}, true, platformerr.CodeProductNotAllowed},
		{"registry unavailable", "mechanical-frontier", nil, true, platformerr.CodeProductNotAllowed},
		{"iap disabled", "mechanical-frontier", boxApps{app: registry.App{AppID: "mechanical-frontier"}}, true, platformerr.CodeProductNotAllowed},
		{"scoped ledger missing", "mechanical-frontier", boxApps{app: enabled}, false, platformerr.CodeRuntimeConfigInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &defenseVerifyFixture{}
			s := &Service{apps: tc.apps, ledger: f, catalog: defenseSKUFixture(t, "mechanical-frontier", "")}
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
	s := &Service{apps: boxApps{app: enabled}, appLedgers: map[string]Ledger{"mechanical-frontier": f}, catalog: defenseSKUFixture(t, "mechanical-frontier", "")}
	req := ledger.DefenseRequest{RequestID: "action_0001", ActionID: "damage", CatalogVersion: "defense-1"}
	if _, e := s.DefenseSnapshot(context.Background(), "mechanical-frontier", "session-owner"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ActDefense(context.Background(), "mechanical-frontier", "session-owner", req); e != nil || f.puid != "session-owner" || f.request != req || f.actions != 1 || f.snapshots != 1 {
		t.Fatalf("routing %+v err=%v", f, e)
	}
}
func TestDefenseServiceRequiresAllPackMappingsBeforeReadingOrSpending(t *testing.T) {
	cases := []struct{ name, omitRegistry, omitCatalog, catalogApp string }{
		{"SKU mapped to foreign app", "", "", "other-app"},
	}
	for _, units := range []int{100, 550, 1150, 2400, 6200} {
		id := fmt.Sprintf("defense_crystals_%d", units)
		cases = append(cases, struct{ name, omitRegistry, omitCatalog, catalogApp string }{name: "registry missing " + id, omitRegistry: id, catalogApp: "mechanical-frontier"})
		cases = append(cases, struct{ name, omitRegistry, omitCatalog, catalogApp string }{name: "SKU missing " + id, omitCatalog: id, catalogApp: "mechanical-frontier"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := defenseRegistryFixture()
			allowed := []string{}
			for _, id := range app.IAP.EntitlementIDs {
				if id != tc.omitRegistry {
					allowed = append(allowed, id)
				}
			}
			app.IAP.EntitlementIDs = allowed
			f := &defenseVerifyFixture{}
			s := &Service{apps: boxApps{app: app}, appLedgers: map[string]Ledger{"mechanical-frontier": f}, catalog: defenseSKUFixture(t, tc.catalogApp, tc.omitCatalog)}
			if _, e := s.DefenseSnapshot(context.Background(), "mechanical-frontier", "owner"); platformerr.CodeOf(e) != platformerr.CodeRuntimeConfigInvalid {
				t.Fatalf("snapshot mapping accepted err=%v", e)
			}
			if _, e := s.ActDefense(context.Background(), "mechanical-frontier", "owner", ledger.DefenseRequest{RequestID: "action_0001", ActionID: "damage", CatalogVersion: "defense-1"}); platformerr.CodeOf(e) != platformerr.CodeRuntimeConfigInvalid {
				t.Fatalf("action mapping accepted err=%v", e)
			}
			if f.actions+f.snapshots != 0 {
				t.Fatal("incomplete catalog reached financial ledger")
			}
		})
	}
}
