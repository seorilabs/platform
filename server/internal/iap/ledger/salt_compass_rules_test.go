package ledger

import (
	"testing"
	"time"
)

func TestSaltRefundDebtAndLaterIncome(t *testing.T) {
	s := newSaltState(time.Unix(1000, 0))
	s.Initialized, s.SealBalance = true, 5
	// 무료 5 + 구매 40 - 소비 40 - 환불 40 = 부족분 35.
	s.SealBalance += 40
	if err := saltDebit(&s, 40); err != nil {
		t.Fatal(err)
	}
	s.SealBalance -= 40
	got := s.snapshot(time.Unix(1000, 0))
	if got.Available != 0 || got.Debt != 35 {
		t.Fatalf("refund=%+v", got)
	}
	if err := saltDebit(&s, 1); err == nil {
		t.Fatal("debt allowed spending")
	}
	s.SealBalance += 3
	if s.snapshot(time.Unix(1000, 0)).Debt != 32 {
		t.Fatal("free income did not repay debt")
	}
	s.SealBalance += 60
	if got := s.snapshot(time.Unix(1000, 0)); got.Available != 28 || got.Debt != 0 {
		t.Fatalf("paid income=%+v", got)
	}
}

func TestSaltStarterUsesServerClockAndNeverReopens(t *testing.T) {
	start := time.Unix(1000, 0)
	s := newSaltState(start)
	s.Initialized = true
	if err := saltApply(&s, SaltRequest{Action: "expedition_dispatch", TargetID: "X1", DurationSeconds: 10800}, start); err != nil {
		t.Fatal(err)
	}
	if err := saltApply(&s, SaltRequest{Action: "expedition_collect", TargetID: "X1"}, start.Add(10799*time.Second)); err == nil {
		t.Fatal("collected before return")
	}
	if err := saltApply(&s, SaltRequest{Action: "expedition_collect", TargetID: "X1"}, start.Add(10800*time.Second)); err != nil {
		t.Fatal(err)
	}
	if s.StarterStartsAt != 0 {
		t.Fatal("opened before first day")
	}
	later := start.Add(24 * time.Hour)
	if err := saltApply(&s, SaltRequest{Action: "expedition_dispatch", TargetID: "X2", DurationSeconds: 10800}, later); err != nil {
		t.Fatal(err)
	}
	settled := later.Add(3 * time.Hour)
	if err := saltApply(&s, SaltRequest{Action: "expedition_collect", TargetID: "X2"}, settled); err != nil {
		t.Fatal(err)
	}
	if !s.snapshot(settled).Starter.Eligible {
		t.Fatal("eligible settlement did not open offer")
	}
	if !s.snapshot(settled.Add(72*time.Hour - time.Second)).Starter.Eligible {
		t.Fatal("closed early")
	}
	if s.snapshot(settled.Add(72 * time.Hour)).Starter.Eligible {
		t.Fatal("expiry is exclusive")
	}
	if err := saltApply(&s, SaltRequest{Action: "prepare_starter", RequestID: "prepare1"}, settled); err != nil {
		t.Fatal(err)
	}
	if err := saltApply(&s, SaltRequest{Action: "prepare_starter", RequestID: "prepare2"}, settled); err == nil {
		t.Fatal("two concurrent purchase intents")
	}
	s.StarterPurchased, s.StarterActive, s.StarterIntent = true, false, ""
	if s.snapshot(settled).Starter.Eligible {
		t.Fatal("refund reopened account once offer")
	}
}

func TestSaltBootstrapAndRequestValidation(t *testing.T) {
	s := newSaltState(time.Unix(1000, 0))
	if err := saltApply(&s, SaltRequest{Action: "bootstrap"}, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := saltApply(&s, SaltRequest{Action: "bootstrap", LegacySeals: 17}, time.Unix(1001, 0)); err == nil {
		t.Fatal("imported legacy balance twice")
	}
	if s.SealBalance != 0 {
		t.Fatal("zero bootstrap minted money")
	}
	for _, r := range []SaltRequest{
		{RequestID: "a", Action: "spend_expedition", TargetID: "X1", LegacySeals: 3},
		{RequestID: "a", Action: "spend_build", TargetID: "B1", ShipClass: "S99"},
		{RequestID: "a", Action: "expedition_dispatch", TargetID: "X1", DurationSeconds: 1},
		{RequestID: "a", Action: "ad_reward"},
		{RequestID: "a/b", Action: "prepare_starter"},
	} {
		if err := r.validate(); err == nil {
			t.Fatalf("accepted invalid request: %+v", r)
		}
	}
}
