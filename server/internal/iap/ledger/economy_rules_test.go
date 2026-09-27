package ledger

import (
	"errors"
	"testing"
	"time"
)

func economyFixture(t *testing.T) (EconomyCatalog, EconomyState, time.Time) {
	t.Helper()
	c, e := LizardEconomyCatalog()
	if e != nil {
		t.Fatal(e)
	}
	c.Enabled = true
	c.LaunchAt = time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).Unix()
	n := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	s := newEconomyState(n)
	s.Paid = 10000
	return c, s, n
}
func request(action string) EconomyRequest {
	return EconomyRequest{RequestID: "request_1234567890", Action: action, CollectionID: "tidal", Count: 1}
}
func TestEconomyPityAndSelectionSurviveCollections(t *testing.T) {
	c, s, n := economyFixture(t)
	low := func(int) (int, error) { return 0, nil }
	for i := 0; i < 60; i++ {
		r := request("draw")
		if i >= 30 {
			r.CollectionID = "orchid"
		}
		out, e := c.applyEconomy(s, r, n.Add(28*24*time.Hour), low)
		if e != nil {
			t.Fatal(e)
		}
		s = out.State
		if (i+1)%30 == 0 && out.Draws[0].Rarity != "legendary" {
			t.Fatalf("draw %d did not guarantee legendary", i+1)
		}
	}
	if s.Pity != 0 || s.Tokens != 60 {
		t.Fatalf("state %+v", s)
	}
	r := request("exchange")
	r.TargetID = "tidal_nebula"
	out, e := c.applyEconomy(s, r, n, low)
	if e != nil || !out.State.Unlocked[r.TargetID] || out.State.Tokens != 0 {
		t.Fatalf("choice %+v %v", out, e)
	}
}
func TestEconomyFreeFirstAndDuplicateResearch(t *testing.T) {
	c, s, n := economyFixture(t)
	s.Free = 80
	s.Paid = 120
	low := func(int) (int, error) { return 0, nil }
	out, e := c.applyEconomy(s, request("draw"), n, low)
	if e != nil {
		t.Fatal(e)
	}
	if out.State.Free != 0 || out.State.Paid != 100 {
		t.Fatalf("free first %+v", out.State)
	}
	out, e = c.applyEconomy(out.State, request("draw"), n, low)
	if e != nil || !out.Draws[0].Duplicate || out.State.Shards != 10 {
		t.Fatalf("duplicate %+v %v", out, e)
	}
}
func TestEconomyRejectDoesNotMutateAndRandomFailureDoesNotSpend(t *testing.T) {
	c, s, n := economyFixture(t)
	fail := func(int) (int, error) { return 0, errors.New("random unavailable") }
	if _, e := c.applyEconomy(s, request("draw"), n, fail); e == nil {
		t.Fatal("random failure accepted")
	}
	if s.Paid != 10000 || len(s.Unlocked) != 0 {
		t.Fatal("input was mutated")
	}
	for _, action := range []string{"draw", "shards"} {
		debt := s.clone()
		debt.Paid = -1
		debt.Free = 1000
		if _, e := c.applyEconomy(debt, request(action), n, secureEconomyRoll); e == nil {
			t.Fatal("debt spent")
		}
	}
	r := request("draw")
	r.CollectionID = "orchid"
	if _, e := c.applyEconomy(s, r, n, secureEconomyRoll); e == nil {
		t.Fatal("future collection accepted")
	}
}
func TestEconomyServerTimedExpeditionWeeklyAndResearch(t *testing.T) {
	c, s, n := economyFixture(t)
	r := request("expedition_start")
	out, e := c.applyEconomy(s, r, n, secureEconomyRoll)
	if e != nil {
		t.Fatal(e)
	}
	s = out.State
	if _, e := c.applyEconomy(s, request("expedition_claim"), n, secureEconomyRoll); e == nil {
		t.Fatal("early claim accepted")
	}
	for i := 0; i < 3; i++ {
		n = n.Add(time.Duration(c.ExpeditionSeconds) * time.Second)
		out, e = c.applyEconomy(s, request("expedition_claim"), n, secureEconomyRoll)
		if e != nil {
			t.Fatal(e)
		}
		s = out.State
		if i < 2 {
			out, e = c.applyEconomy(s, r, n, secureEconomyRoll)
			if e != nil {
				t.Fatal(e)
			}
			s = out.State
		}
	}
	out, e = c.applyEconomy(s, request("weekly"), n, secureEconomyRoll)
	if e != nil || out.State.Free != 300 || out.State.Shards != 160 {
		t.Fatalf("weekly %+v %v", out, e)
	}
	s = out.State
	if _, e := c.applyEconomy(s, request("weekly"), n, secureEconomyRoll); e == nil {
		t.Fatal("weekly duplicated")
	}
	research := request("research")
	research.TargetID = "expedition"
	out, e = c.applyEconomy(s, research, n, secureEconomyRoll)
	if e != nil || out.State.Research["tidal_expedition"] != 1 || out.State.Shards != 140 {
		t.Fatalf("research %+v %v", out, e)
	}
}
func TestEconomyStarterChoiceCannotDuplicateOwned(t *testing.T) {
	c, s, n := economyFixture(t)
	s.StarterChoices = 1
	r := request("starter_choice")
	r.TargetID = "tidal_dew"
	out, e := c.applyEconomy(s, r, n, secureEconomyRoll)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := c.applyEconomy(out.State, r, n, secureEconomyRoll); e == nil {
		t.Fatal("starter choice duplicated")
	}
}
func TestEconomyWeightBoundaries(t *testing.T) {
	for _, tc := range []struct {
		roll   int
		rarity string
	}{{0, "common"}, {7499, "common"}, {7500, "rare"}, {9699, "rare"}, {9700, "legendary"}, {9999, "legendary"}} {
		t.Run(tc.rarity+time.Duration(tc.roll).String(), func(t *testing.T) {
			c, s, n := economyFixture(t)
			call := 0
			roll := func(int) (int, error) {
				call++
				if call == 1 {
					return tc.roll, nil
				}
				return 0, nil
			}
			out, e := c.applyEconomy(s, request("draw"), n, roll)
			if e != nil || out.Draws[0].Rarity != tc.rarity {
				t.Fatalf("%+v %v", out, e)
			}
		})
	}
}

func TestEconomyGuestCannotSpendPaidBalance(t *testing.T) {
	c, s, n := economyFixture(t)
	r := request("draw")
	r.freeOnly = true
	if _, err := c.applyEconomy(s, r, n, func(int) (int, error) { return 0, nil }); err == nil {
		t.Fatal("guest consumed paid currency")
	}
	s.Free = 100
	out, err := c.applyEconomy(s, r, n, func(int) (int, error) { return 0, nil })
	if err != nil || out.State.Paid != s.Paid || out.State.Free != 0 {
		t.Fatalf("free spend: %+v %v", out, err)
	}
}
