package ledger

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

// passRequestIDPattern은 OpenAPI PassClaimRequest.requestId와 같다.
var passRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// passClaimDoc은 하루 몫 수령 한 번의 증거다. 지급량과 source별 날 수를 남겨
// 응답이 끊긴 재시도, 환불 대응, 문의 재현에 쓴다. 불변식 5에 따라 지우지 않는다.
type passClaimDoc struct {
	PlatformUserID string         `firestore:"platformUserId"`
	EntitlementID  string         `firestore:"entitlementId"`
	RequestID      string         `firestore:"requestId"`
	Days           int            `firestore:"days"`
	Amount         int            `firestore:"amount"`
	DailyAmount    int            `firestore:"dailyAmount"`
	MissedDays     string         `firestore:"missedDays"`
	Sources        map[string]int `firestore:"sources"`
	ClaimedAt      time.Time      `firestore:"claimedAt"`
}

// PassStatus는 OpenAPI PassStatus다. 시각은 UTC Unix 초이고 0은 없음이다.
type PassStatus struct {
	EntitlementID   string `json:"entitlementId"`
	ImmediateAmount int    `json:"immediateAmount"`
	DailyAmount     int    `json:"dailyAmount"`
	Days            int    `json:"days"`
	MissedDays      string `json:"missedDays"`
	Active          bool   `json:"active"`
	Claimable       bool   `json:"claimable"`
	ClaimableAmount int    `json:"claimableAmount"`
	ClaimedDays     int    `json:"claimedDays"`
	TotalDays       int    `json:"totalDays"`
	RemainingDays   int    `json:"remainingDays"`
	NextClaimAt     int64  `json:"nextClaimAt"`
	EndsAt          int64  `json:"endsAt"`
}

// PassReceipt는 OpenAPI PassClaimReceipt다. Applied=false는 같은 requestId의
// 재시도이며 Amount와 Days는 처음 수령 결과다.
type PassReceipt struct {
	RequestID     string     `json:"requestId"`
	Applied       bool       `json:"applied"`
	EntitlementID string     `json:"entitlementId"`
	Days          int        `json:"days"`
	Amount        int        `json:"amount"`
	ClaimedAt     int64      `json:"claimedAt"`
	Pass          PassStatus `json:"pass"`
}

func passStatus(entID string, s domain.PassSchedule, p domain.PassPlan) PassStatus {
	return PassStatus{
		EntitlementID: entID, ImmediateAmount: s.ImmediateAmount, DailyAmount: s.DailyAmount,
		Days: s.Days, MissedDays: string(s.MissedDays),
		Active: p.Active, Claimable: p.ClaimableDays > 0, ClaimableAmount: p.ClaimableDays * s.DailyAmount,
		ClaimedDays: p.ClaimedDays, TotalDays: p.TotalDays, RemainingDays: p.RemainingDays,
		NextClaimAt: unixCeil(p.NextClaimAt), EndsAt: unixCeil(p.EndsAt),
	}
}

// unixCeil은 초 단위로 올림한다. 내림하면 경계 직전 초에 받기를 눌러 409가 난다.
func unixCeil(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	sec := t.Unix()
	if t.Nanosecond() > 0 {
		sec++
	}
	return sec
}

// PassSnapshot은 받지 않고 이용권 상태만 읽는다.
func (l *Ledger) PassSnapshot(ctx context.Context, puid, entitlementID string, s domain.PassSchedule) (PassStatus, error) {
	if puid == "" || entitlementID == "" {
		return PassStatus{}, platformerr.New(platformerr.CodeInternal, "이용권 조회 정보가 올바르지 않아요")
	}
	p, err := l.paths.internalEntitlement(puid, entitlementID)
	if err != nil {
		return PassStatus{}, err
	}
	var out PassStatus
	err = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		doc, err := l.readEntitlement(tx, p, entitlementID)
		if err != nil {
			return err
		}
		plan, err := domain.PlanPass(s, doc.Sources, l.now().UTC())
		out = passStatus(entitlementID, s, plan)
		return err
	})
	return out, err
}

// ClaimPass는 지금 받을 수 있는 하루 몫을 받는다. ADR 0036.
//
// 한 트랜잭션에서: 요청 증거 확인(멱등) → source로 오늘 몫 판정 → source마다 받은 날 수와
// 마지막 수령 시각 갱신 → 증거 create. 같은 날의 다른 요청과 동시 요청은 entitlement
// 문서 충돌로 직렬화되고, 늦은 쪽은 마지막 수령 시각을 보고 pass_already_claimed가 된다.
// 같은 requestId 재시도는 applied=false로 처음 결과와 현재 상태를 돌려준다.
func (l *Ledger) ClaimPass(
	ctx context.Context,
	puid, entitlementID string,
	s domain.PassSchedule,
	requestID string,
) (PassReceipt, error) {
	if puid == "" || entitlementID == "" || !s.Valid() {
		return PassReceipt{}, platformerr.New(platformerr.CodeInternal, "이용권 수령 정보가 올바르지 않아요")
	}
	if !passRequestIDPattern.MatchString(requestID) {
		return PassReceipt{}, platformerr.New(platformerr.CodeRequestInvalid, "이용권 요청 id가 올바르지 않아요")
	}
	claimPath, err := l.paths.passClaim(contentRequestDigest(
		l.appID + "\x00" + string(l.env) + "\x00" + puid + "\x00" + requestID,
	))
	if err != nil {
		return PassReceipt{}, err
	}
	entPath, err := l.paths.internalEntitlement(puid, entitlementID)
	if err != nil {
		return PassReceipt{}, err
	}
	var out PassReceipt
	err = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		replayed, snap, err := tx.Exists(claimPath)
		if err != nil {
			return err
		}
		ent, err := l.readEntitlement(tx, entPath, entitlementID)
		if err != nil {
			return err
		}
		now := l.now().UTC()
		if replayed {
			var prev passClaimDoc
			if err := snap.DataTo(&prev); err != nil {
				return err
			}
			if prev.PlatformUserID != puid || prev.RequestID != requestID || prev.EntitlementID != entitlementID {
				return platformerr.New(platformerr.CodePassReplayMismatch, "이용권 재시도 내용이 기존 수령과 달라요")
			}
			plan, err := domain.PlanPass(s, ent.Sources, now)
			if err != nil {
				return err
			}
			out = passReceipt(prev, false, passStatus(entitlementID, s, plan))
			return nil
		}

		plan, err := domain.PlanPass(s, ent.Sources, now)
		if err != nil {
			return err
		}
		if plan.ClaimableDays == 0 {
			if plan.Active {
				return platformerr.New(platformerr.CodePassAlreadyClaimed, "오늘 몫은 이미 받았어요")
			}
			return platformerr.New(platformerr.CodePassInactive, "지금 받을 수 있는 이용권이 없어요")
		}
		for key, days := range plan.Claims {
			src := ent.Sources[key]
			src.ContentUnitsConsumed += days
			src.LastClaimedAt = now
			ent.Sources[key] = src
		}
		if err := tx.Set(entPath, ent); err != nil {
			return err
		}
		evidence := passClaimDoc{
			PlatformUserID: puid, EntitlementID: entitlementID, RequestID: requestID,
			Days: plan.ClaimableDays, Amount: plan.ClaimableDays * s.DailyAmount,
			DailyAmount: s.DailyAmount, MissedDays: string(s.MissedDays),
			Sources: plan.Claims, ClaimedAt: now,
		}
		if err := tx.Create(claimPath, evidence); err != nil {
			return err
		}
		after, err := domain.PlanPass(s, ent.Sources, now)
		if err != nil {
			return err
		}
		out = passReceipt(evidence, true, passStatus(entitlementID, s, after))
		return nil
	})
	if err != nil {
		var pe *platformerr.Error
		if errors.As(err, &pe) {
			return PassReceipt{}, err
		}
		return PassReceipt{}, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid, "이용권을 받지 못했어요")
	}
	return out, nil
}

func passReceipt(d passClaimDoc, applied bool, status PassStatus) PassReceipt {
	return PassReceipt{
		RequestID: d.RequestID, Applied: applied, EntitlementID: d.EntitlementID,
		Days: d.Days, Amount: d.Amount, ClaimedAt: d.ClaimedAt.Unix(), Pass: status,
	}
}
