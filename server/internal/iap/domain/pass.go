package domain

// 기간 이용권(ADR 0036)
//
// 소모성 구매 한 건이 "즉시분 + N일 동안 하루 한 번 받는 몫"이 된다. 즉시분은 기존
// 소모성 검증 응답으로 앱이 지급하고, 하루 몫은 서버가 판정해 수령 응답으로 준다.
// 날짜는 서버 시각만 쓴다. 기기 시계와 시간대는 받지 않는다.

const (
	maxPassDays   = 366
	maxPassAmount = 1_000_000
)

// MissedDayPolicy는 받지 않고 지난 날의 몫을 어떻게 할지다.
//
// 기본은 소멸이다. 심사에서 소멸 규칙이 문제가 되면 카탈로그 값만 catch_up으로
// 바꿔 지난 몫을 몰아 받게 한다. 판정 분기는 한 곳(PlanPass)에 둔다.
type MissedDayPolicy string

const (
	// MissedDaysForfeit는 그날 안에 받지 않은 몫이 사라진다.
	MissedDaysForfeit MissedDayPolicy = "forfeit"
	// MissedDaysCatchUp은 지난 날 몫을 다음 수령 때 한 번에 받는다.
	MissedDaysCatchUp MissedDayPolicy = "catch_up"
)

// PassSchedule은 카탈로그가 정하는 지급 일정이다. 클라이언트 입력으로 받지 않는다.
type PassSchedule struct {
	// ImmediateAmount는 구매 검증 때 앱이 바로 지급하는 양이다. 서버는 안내만 한다.
	ImmediateAmount int             `json:"immediate_amount"`
	DailyAmount     int             `json:"daily_amount"`
	Days            int             `json:"days"`
	MissedDays      MissedDayPolicy `json:"missed_days"`
}

// Valid는 일정이 판정 가능한 범위인지 본다.
func (s PassSchedule) Valid() bool {
	if s.MissedDays != MissedDaysForfeit && s.MissedDays != MissedDaysCatchUp {
		return false
	}
	return s.Days >= 1 && s.Days <= maxPassDays &&
		s.DailyAmount >= 1 && s.DailyAmount <= maxPassAmount &&
		s.ImmediateAmount >= 0 && s.ImmediateAmount <= maxPassAmount
}
