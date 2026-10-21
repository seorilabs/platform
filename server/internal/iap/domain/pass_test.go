package domain

import "testing"

func amberPass(policy MissedDayPolicy) PassSchedule {
	return PassSchedule{ImmediateAmount: 300, DailyAmount: 50, Days: 30, MissedDays: policy}
}

func TestPassScheduleValid(t *testing.T) {
	cases := []struct {
		name string
		s    PassSchedule
		want bool
	}{
		{"승인값", amberPass(MissedDaysForfeit), true},
		{"몰아 받기", amberPass(MissedDaysCatchUp), true},
		{"즉시분 없음", PassSchedule{DailyAmount: 10, Days: 7, MissedDays: MissedDaysForfeit}, true},
		{"정책 누락", PassSchedule{DailyAmount: 50, Days: 30}, false},
		{"모르는 정책", PassSchedule{DailyAmount: 50, Days: 30, MissedDays: "stack"}, false},
		{"하루 지급 0", PassSchedule{Days: 30, MissedDays: MissedDaysForfeit}, false},
		{"일수 0", PassSchedule{DailyAmount: 50, MissedDays: MissedDaysForfeit}, false},
		{"일수 상한 초과", PassSchedule{DailyAmount: 50, Days: 367, MissedDays: MissedDaysForfeit}, false},
		{"음수 즉시분", PassSchedule{ImmediateAmount: -1, DailyAmount: 50, Days: 30, MissedDays: MissedDaysForfeit}, false},
		{"지급량 상한 초과", PassSchedule{DailyAmount: 1_000_001, Days: 30, MissedDays: MissedDaysForfeit}, false},
	}
	for _, c := range cases {
		if got := c.s.Valid(); got != c.want {
			t.Errorf("%s: Valid()=%v, want %v", c.name, got, c.want)
		}
	}
}
