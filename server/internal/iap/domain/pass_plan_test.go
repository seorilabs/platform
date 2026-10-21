package domain

import (
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

var passT0 = time.Date(2026, 10, 21, 13, 30, 0, 0, time.UTC)

func passDay(n int) time.Time { return passT0.Add(time.Duration(n) * PassDay) }

func activePassSource(activated time.Time, claimed int, last time.Time) Source {
	return Source{
		Platform: PlatformGooglePlay, ProductID: "pass", State: StateActive,
		PurchasedAt: activated.Add(-time.Minute), ActivatedAt: activated,
		ContentUnitsConsumed: claimed, LastClaimedAt: last,
	}
}

// 놓친 날 소멸: 하루(첫 활성 시각부터 24시간)마다 한 번, 그날 안에만 받는다.
func TestPlanPassForfeitOnePerDay(t *testing.T) {
	s := amberPass(MissedDaysForfeit)
	cases := []struct {
		name          string
		src           Source
		now           time.Time
		active        bool
		claimable     int
		remaining     int
		nextClaimAt   time.Time
		claimedDays   int
		wantEndsAt    time.Time
		wantClaimsKey bool
	}{
		{"구매 직후 첫날", activePassSource(passT0, 0, time.Time{}), passT0.Add(time.Second),
			true, 1, 30, passT0.Add(time.Second), 0, passDay(30), true},
		{"첫날 받은 뒤", activePassSource(passT0, 1, passT0.Add(time.Minute)), passT0.Add(time.Hour),
			true, 0, 29, passDay(1), 1, passDay(30), false},
		{"다음 날 경계 직전", activePassSource(passT0, 1, passT0.Add(time.Minute)), passDay(1).Add(-time.Nanosecond),
			true, 0, 29, passDay(1), 1, passDay(30), false},
		{"다음 날 경계", activePassSource(passT0, 1, passT0.Add(time.Minute)), passDay(1),
			true, 1, 29, passDay(1), 1, passDay(30), true},
		{"닷새 놓친 뒤에도 하루분만", activePassSource(passT0, 1, passT0.Add(time.Minute)), passDay(6).Add(time.Hour),
			true, 1, 24, passDay(6).Add(time.Hour), 1, passDay(30), true},
		{"마지막 날", activePassSource(passT0, 10, passDay(20)), passDay(29).Add(23 * time.Hour),
			true, 1, 1, passDay(29).Add(23 * time.Hour), 10, passDay(30), true},
		{"마지막 날 받은 뒤", activePassSource(passT0, 11, passDay(29).Add(time.Hour)), passDay(29).Add(2 * time.Hour),
			true, 0, 0, time.Time{}, 11, passDay(30), false},
		{"기간 끝", activePassSource(passT0, 11, passDay(29).Add(time.Hour)), passDay(30),
			false, 0, 0, time.Time{}, 11, passDay(30), false},
		// 받은 날 수를 다 채운 source는 같은 날 표식이 사라져도 더 받지 못한다.
		{"받은 날 수 상한", activePassSource(passT0, 30, time.Time{}), passDay(29),
			true, 0, 0, time.Time{}, 30, passDay(30), false},
	}
	for _, c := range cases {
		p, err := PlanPass(s, map[string]Source{"a": c.src}, c.now)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if p.Active != c.active || p.ClaimableDays != c.claimable || p.RemainingDays != c.remaining ||
			!p.NextClaimAt.Equal(c.nextClaimAt) || p.ClaimedDays != c.claimedDays || p.TotalDays != 30 ||
			!p.EndsAt.Equal(c.wantEndsAt) || (p.Claims["a"] == 1) != c.wantClaimsKey {
			t.Errorf("%s: %+v", c.name, p)
		}
	}
}

// 이용 중 재구매는 끝나는 날을 30일 뒤로 미루고 하루 지급량은 그대로다.
// 환불된 source는 자리를 지켜 다른 source의 날짜를 당기지 않는다.
func TestPlanPassRepurchaseExtendsAndRefundStopsOnlyItsDays(t *testing.T) {
	s := amberPass(MissedDaysForfeit)
	first := activePassSource(passT0, 10, passDay(9))
	second := activePassSource(passDay(10).Add(5*time.Hour), 0, time.Time{})
	sources := map[string]Source{"first": first, "second": second}

	p, err := PlanPass(s, sources, passDay(10).Add(6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Active || p.ClaimableDays != 1 || p.Claims["first"] != 1 || p.TotalDays != 60 ||
		!p.EndsAt.Equal(passDay(60)) || p.RemainingDays != 50 {
		t.Fatalf("연장: %+v", p)
	}
	// 31일째 경계는 첫 구매의 위상을 그대로 잇는다.
	p, err = PlanPass(s, sources, passDay(30))
	if err != nil {
		t.Fatal(err)
	}
	if p.Claims["second"] != 1 || p.ClaimableDays != 1 {
		t.Fatalf("이어 받기: %+v", p)
	}

	refunded := first
	refunded.State = StateRevoked
	sources["first"] = refunded
	p, err = PlanPass(s, sources, passDay(20))
	if err != nil {
		t.Fatal(err)
	}
	if p.Active || p.ClaimableDays != 0 || !p.NextClaimAt.Equal(passDay(30)) || p.TotalDays != 30 ||
		!p.EndsAt.Equal(passDay(60)) || p.RemainingDays != 30 {
		t.Fatalf("환불 뒤 남은 날: %+v", p)
	}
}

// 기간이 끝난 뒤 다시 사면 그 시각부터 새로 센다.
func TestPlanPassRepurchaseAfterEndStartsNewPhase(t *testing.T) {
	s := amberPass(MissedDaysForfeit)
	restart := passDay(40).Add(7 * time.Hour)
	sources := map[string]Source{
		"old": activePassSource(passT0, 30, passDay(29)),
		"new": activePassSource(restart, 0, time.Time{}),
	}
	p, err := PlanPass(s, sources, restart.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Active || p.Claims["new"] != 1 || !p.EndsAt.Equal(restart.Add(30*PassDay)) || p.RemainingDays != 30 {
		t.Fatalf("%+v", p)
	}
}

// 몰아 받기 정책: 지난 날 몫을 한 번에 받고, 기간이 끝난 뒤에도 남은 몫을 받는다.
func TestPlanPassCatchUp(t *testing.T) {
	s := amberPass(MissedDaysCatchUp)
	p, err := PlanPass(s, map[string]Source{"a": activePassSource(passT0, 1, passT0)}, passDay(4).Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if p.ClaimableDays != 4 || p.Claims["a"] != 4 || p.RemainingDays != 29 || !p.NextClaimAt.Equal(passDay(4).Add(time.Hour)) {
		t.Fatalf("밀린 날: %+v", p)
	}
	p, err = PlanPass(s, map[string]Source{"a": activePassSource(passT0, 5, passDay(4))}, passDay(4).Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if p.ClaimableDays != 0 || !p.NextClaimAt.Equal(passDay(5)) {
		t.Fatalf("받은 뒤: %+v", p)
	}
	p, err = PlanPass(s, map[string]Source{"a": activePassSource(passT0, 5, passDay(4))}, passDay(45))
	if err != nil {
		t.Fatal(err)
	}
	if p.Active || p.ClaimableDays != 25 || p.RemainingDays != 25 {
		t.Fatalf("끝난 뒤: %+v", p)
	}
}

// 다른 기기로 옮겨 온 source의 마지막 수령 시각이 같은 날 두 번 받기를 막는다.
func TestPlanPassAnyRecentClaimBlocksSameDay(t *testing.T) {
	s := amberPass(MissedDaysForfeit)
	sources := map[string]Source{
		"a": activePassSource(passT0, 3, time.Time{}),
		// 지금 날짜를 맡은 source가 아니어도, 같은 날 안의 수령 기록이면 받은 것으로 본다.
		"b": activePassSource(passDay(1), 0, passDay(3).Add(time.Hour)),
	}
	p, err := PlanPass(s, sources, passDay(3).Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if p.ClaimableDays != 0 || !p.NextClaimAt.Equal(passDay(4)) {
		t.Fatalf("%+v", p)
	}
}

// 이 기능 이전에 만든 활성 source는 마켓 구매 시각을 기준으로 삼는다. 활성으로
// 확정된 적을 알 수 없는 회수 source와 보류 source는 날짜를 차지하지 않는다.
func TestPlanPassLegacyAndNeverActiveSources(t *testing.T) {
	s := amberPass(MissedDaysForfeit)
	legacy := Source{State: StateActive, PurchasedAt: passT0}
	neverActive := Source{State: StateRevoked, PurchasedAt: passT0.Add(-40 * PassDay)}
	pending := Source{State: StatePending, PurchasedAt: passT0.Add(-time.Hour)}
	p, err := PlanPass(s, map[string]Source{"legacy": legacy, "revoked": neverActive, "pending": pending}, passT0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Active || p.Claims["legacy"] != 1 || !p.EndsAt.Equal(passDay(30)) || p.TotalDays != 30 {
		t.Fatalf("%+v", p)
	}
}

func TestPlanPassRejectsBrokenLedger(t *testing.T) {
	s := amberPass(MissedDaysForfeit)
	for name, src := range map[string]Source{
		"받은 날이 일수보다 많음": activePassSource(passT0, 31, time.Time{}),
		"음수":            activePassSource(passT0, -1, time.Time{}),
		"시작 시각 없음":      {State: StateActive},
	} {
		if _, err := PlanPass(s, map[string]Source{"a": src}, passT0); platformerr.CodeOf(err) != platformerr.CodeLedgerStateInvalid {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if _, err := PlanPass(PassSchedule{}, nil, passT0); platformerr.CodeOf(err) != platformerr.CodeCatalogInvalid {
		t.Errorf("잘못된 일정: err=%v", err)
	}
}
