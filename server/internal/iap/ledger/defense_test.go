package ledger

import (
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"reflect"
	"testing"
)

func TestDefenseCatalogPricesAndDeterministicActions(t *testing.T) {
	cat, err := DefenseCatalogCurrent()
	if err != nil {
		t.Fatal(err)
	}
	units, prices := []int{100, 550, 1150, 2400, 6200}, []int{99, 499, 999, 1999, 4999}
	for i, p := range cat.Packs {
		if p.Units != units[i] || p.USDCents != prices[i] {
			t.Fatalf("pack %d = %+v", i, p)
		}
	}
	state := defenseView(DefenseState{}, 10000, 0)
	for _, tc := range []struct {
		action, run string
		cost        int
		level       int
	}{
		{"damage", "", 25, 1}, {"damage", "", 35, 2}, {"soldiers", "", 25, 1}, {"core", "", 25, 1},
		{"siege", "", 300, 0}, {"storm", "", 900, 0}, {"revive", "run_0001", 75, 0},
		{"airstrike", "run_0001", 25, 0}, {"repair", "run_0001", 30, 0},
	} {
		before := state.Balance
		out, cost, e := defenseApply(cat, state, DefenseRequest{ActionID: tc.action, CatalogVersion: cat.Version, RunID: tc.run})
		if e != nil || cost != tc.cost || out.Balance != before-int64(tc.cost) || out.Version != state.Version+1 {
			t.Fatalf("%s out=%+v cost=%d err=%v", tc.action, out, cost, e)
		}
		if tc.level > 0 && out.Upgrades[tc.action] != tc.level {
			t.Fatalf("%s level=%d", tc.action, out.Upgrades[tc.action])
		}
		state = out
	}
	// No revive count enters pricing or authorization: each new request can revive again.
	for range 20 {
		var e error
		state, _, e = defenseApply(cat, state, DefenseRequest{ActionID: "revive", CatalogVersion: cat.Version, RunID: "run_0001"})
		if e != nil {
			t.Fatal(e)
		}
	}
}
func TestDefenseRejectsInvalidAuthorityWithoutMutatingInput(t *testing.T) {
	cat, err := DefenseCatalogCurrent()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		state DefenseState
		req   DefenseRequest
	}{
		{"catalog mismatch", defenseView(DefenseState{}, 1000, 0), DefenseRequest{ActionID: "damage", CatalogVersion: "future"}},
		{"unknown action", defenseView(DefenseState{}, 1000, 0), DefenseRequest{ActionID: "gift", CatalogVersion: cat.Version}},
		{"no free spending", defenseView(DefenseState{}, 0, 0), DefenseRequest{ActionID: "damage", CatalogVersion: cat.Version}},
		{"refund debt", defenseView(DefenseState{}, -1, 25), DefenseRequest{ActionID: "damage", CatalogVersion: cat.Version}},
		{"consumable needs run", defenseView(DefenseState{}, 1000, 0), DefenseRequest{ActionID: "revive", CatalogVersion: cat.Version}},
		{"upgrade rejects run", defenseView(DefenseState{}, 1000, 0), DefenseRequest{ActionID: "damage", CatalogVersion: cat.Version, RunID: "run_0001"}},
		{"duplicate weapon", defenseView(DefenseState{Weapons: []string{"siege"}}, 1000, 0), DefenseRequest{ActionID: "siege", CatalogVersion: cat.Version}},
		{"level limit", defenseView(DefenseState{Upgrades: map[string]int{"damage": 10000}}, 1000000, 0), DefenseRequest{ActionID: "damage", CatalogVersion: cat.Version}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.state
			before.Upgrades = map[string]int{}
			for k, v := range tc.state.Upgrades {
				before.Upgrades[k] = v
			}
			before.Weapons = append([]string{}, tc.state.Weapons...)
			_, _, e := defenseApply(cat, tc.state, tc.req)
			if e == nil {
				t.Fatal("rejected request accepted")
			}
			if !reflect.DeepEqual(tc.state, before) {
				t.Fatalf("rejection mutated input %+v -> %+v", before, tc.state)
			}
		})
	}
}
func TestDefenseBalanceUsesSourceStateAndRefundDebt(t *testing.T) {
	cat, err := DefenseCatalogCurrent()
	if err != nil {
		t.Fatal(err)
	}
	ents := map[string]entitlementDoc{"defense_crystals_100": {Sources: map[string]domain.Source{
		"active":   {State: domain.StateActive, ContentUnitsConsumed: 30},
		"pending":  {State: domain.StatePending},
		"refunded": {State: domain.StateRevoked, ContentUnitsConsumed: 40},
	}}}
	balance, debt, e := defenseUnits(cat, ents)
	if e != nil || balance != 30 || debt != 40 {
		t.Fatalf("balance=%d debt=%d err=%v", balance, debt, e)
	}
	ent := ents["defense_crystals_100"]
	ent.Sources["active"] = domain.Source{State: domain.StateActive, ContentUnitsConsumed: 101}
	ents["defense_crystals_100"] = ent
	if _, _, e = defenseUnits(cat, ents); e == nil {
		t.Fatal("corrupt consumed units accepted")
	}
}

func TestDefenseMalformedActionsUseRequestInvalid(t *testing.T) {
	cat, err := DefenseCatalogCurrent()
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []DefenseRequest{
		{RequestID: "action_0001", ActionID: "unknown", CatalogVersion: cat.Version},
		{RequestID: "action_0002", ActionID: "revive", CatalogVersion: cat.Version},
		{RequestID: "action_0003", ActionID: "revive", CatalogVersion: cat.Version, RunID: "short"},
		{RequestID: "action_0004", ActionID: "damage", CatalogVersion: cat.Version, RunID: "valid_run_0001"},
	} {
		if _, _, err := defenseApply(cat, defenseView(DefenseState{}, 1000, 0), req); platformerr.CodeOf(err) != platformerr.CodeRequestInvalid {
			t.Fatalf("malformed request %+v err=%v", req, err)
		}
	}
}
