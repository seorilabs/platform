package domain

import (
	"sort"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// PassDay는 이용권 하루의 길이다. 하루는 source가 처음 활성으로 확정된 시각부터
// 24시간 단위로 센다. 시간대가 없으므로 모든 사용자가 정확히 N번의 기회를 가진다.
const PassDay = 24 * time.Hour

// PassPlan은 한 시각의 이용권 상태와, 지금 받으면 source마다 늘어날 날 수다.
type PassPlan struct {
	// Active는 지금 시각을 활성 source의 기간이 덮는지다.
	Active        bool
	ClaimableDays int
	// Claims는 지금 받을 때 source key별로 더할 날 수다.
	Claims      map[string]int
	ClaimedDays int
	TotalDays   int
	// RemainingDays는 앞으로 받을 수 있는 날 수다. 오늘 아직 받지 않았으면 오늘을 포함한다.
	RemainingDays int
	// NextClaimAt은 받을 수 있는 가장 이른 시각이다. 지금 받을 수 있으면 지금이고, 없으면 zero다.
	NextClaimAt time.Time
	// EndsAt은 활성 source 기간의 끝이다. 활성 source가 없으면 zero다.
	EndsAt time.Time
}

type passSegment struct {
	key        string
	active     bool
	start, end time.Time
	claimed    int
}

// PlanPass는 entitlement 한 개의 source로 이용권 상태를 계산한다.
//
// 기간 배치: 처음 활성 시각 순으로 source를 놓는다. 앞 기간이 끝나기 전에 산 source는
// 앞 기간의 끝에서 시작해 끝나는 날만 미룬다(하루 지급량은 그대로). 앞 기간이 끝난 뒤에
// 산 source는 자기 활성 시각부터 새로 센다. 회수된 source도 자리는 지켜서 환불이 다른
// source의 날짜를 당기지 않게 하고, 자기 남은 날만 멈춘다.
//
// 같은 날 두 번 받기는 source들의 LastClaimedAt으로 막는다. 이 값이 source와 함께
// 소유권 이전되므로 기기를 바꿔 같은 구매를 복원해도 그날 다시 받지 못한다.
// 받은 날 수는 source마다 Days를 넘지 못하게 한 번 더 막는다.
func PlanPass(s PassSchedule, sources map[string]Source, now time.Time) (PassPlan, error) {
	if !s.Valid() {
		return PassPlan{}, platformerr.New(platformerr.CodeCatalogInvalid, "이용권 지급 일정이 올바르지 않아요")
	}
	length := time.Duration(s.Days) * PassDay
	var lastClaim time.Time
	segs := make([]passSegment, 0, len(sources))
	for key, src := range sources {
		if src.LastClaimedAt.After(lastClaim) {
			lastClaim = src.LastClaimedAt
		}
		anchor, ok := passAnchor(src)
		if !ok {
			continue
		}
		if anchor.IsZero() {
			return PassPlan{}, platformerr.New(platformerr.CodeLedgerStateInvalid, "이용권 시작 시각을 알 수 없어요")
		}
		if src.ContentUnitsConsumed < 0 || src.ContentUnitsConsumed > s.Days {
			return PassPlan{}, platformerr.New(platformerr.CodeLedgerStateInvalid, "이용권 수령 원장이 올바르지 않아요")
		}
		segs = append(segs, passSegment{
			key: key, active: src.State == StateActive, start: anchor, claimed: src.ContentUnitsConsumed,
		})
	}
	sort.Slice(segs, func(i, j int) bool {
		if !segs[i].start.Equal(segs[j].start) {
			return segs[i].start.Before(segs[j].start)
		}
		return segs[i].key < segs[j].key
	})
	var prevEnd time.Time
	for i := range segs {
		if segs[i].start.Before(prevEnd) {
			segs[i].start = prevEnd
		}
		segs[i].end = segs[i].start.Add(length)
		prevEnd = segs[i].end
	}

	plan := PassPlan{Claims: map[string]int{}}
	var current *passSegment
	for i := range segs {
		seg := &segs[i]
		if !seg.active {
			continue
		}
		plan.TotalDays += s.Days
		plan.ClaimedDays += seg.claimed
		if seg.end.After(plan.EndsAt) {
			plan.EndsAt = seg.end
		}
		if !now.Before(seg.start) && now.Before(seg.end) {
			current = seg
		}
	}
	plan.Active = current != nil

	// 지금 날의 시작. 다음 날 경계를 찾는 기준이다.
	after := now
	if current != nil {
		after = current.start.Add(now.Sub(current.start) / PassDay * PassDay)
	}

	switch s.MissedDays {
	case MissedDaysForfeit:
		if current != nil {
			claimedToday := !lastClaim.Before(after)
			left := int(current.end.Sub(after) / PassDay)
			if claimedToday {
				left--
			}
			left = min(left, s.Days-current.claimed)
			if !claimedToday && left > 0 {
				plan.ClaimableDays = 1
				plan.Claims[current.key] = 1
			}
			plan.RemainingDays = max(left, 0)
			after = after.Add(PassDay)
		}
		for _, seg := range segs {
			if seg.active && seg.start.After(now) {
				plan.RemainingDays += s.Days - seg.claimed
			}
		}
	case MissedDaysCatchUp:
		for _, seg := range segs {
			if !seg.active {
				continue
			}
			plan.RemainingDays += s.Days - seg.claimed
			if now.Before(seg.start) {
				continue
			}
			elapsed := min(s.Days, int(now.Sub(seg.start)/PassDay)+1)
			if pending := elapsed - seg.claimed; pending > 0 {
				plan.Claims[seg.key] = pending
				plan.ClaimableDays += pending
			}
		}
		if current != nil {
			after = after.Add(PassDay)
		}
	}
	if plan.ClaimableDays > 0 {
		plan.NextClaimAt = now
	} else {
		plan.NextClaimAt = nextPassDay(segs, after)
	}
	return plan, nil
}

// passAnchor는 source가 이용권 기간을 차지하는지와 그 시작 시각을 돌려준다.
//
// 활성으로 확정된 적이 있는 source만 기간을 차지한다. ActivatedAt이 없는 활성 source는
// 이 필드 이전에 만든 것이므로 마켓 구매 시각으로 대신한다. 활성 확정 여부를 알 수 없는
// 회수 source와 보류 source는 기간을 차지하지 않는다.
func passAnchor(src Source) (time.Time, bool) {
	if !src.ActivatedAt.IsZero() {
		return src.ActivatedAt, true
	}
	if src.State == StateActive {
		return src.PurchasedAt, true
	}
	return time.Time{}, false
}

// nextPassDay는 t 이후(t 포함) 활성 기간 안에서 처음 시작하는 날의 시각이다.
func nextPassDay(segs []passSegment, t time.Time) time.Time {
	var best time.Time
	for _, seg := range segs {
		if !seg.active || !seg.end.After(t) {
			continue
		}
		candidate := seg.start
		if seg.start.Before(t) {
			k := t.Sub(seg.start) / PassDay
			candidate = seg.start.Add(k * PassDay)
			if candidate.Before(t) {
				candidate = candidate.Add(PassDay)
			}
			if !candidate.Before(seg.end) {
				continue
			}
		}
		if best.IsZero() || candidate.Before(best) {
			best = candidate
		}
	}
	return best
}
