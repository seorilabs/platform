package ledger

import (
	"regexp"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

const saltAppID = "salt-compass"
const saltStarterID = "saltcompass_starter"

// SaltRequest는 허용된 게임 행동만 받는다. 잔액 설정·임의 credit API가 아니다.
// 원정/건조의 금화·진행 규칙은 게임 core에 남긴다.
type SaltRequest struct {
	RequestID       string `json:"requestId" firestore:"requestId"`
	Action          string `json:"action" firestore:"action"`
	TargetID        string `json:"targetId,omitempty" firestore:"targetId"`
	LegacySeals     int64  `json:"legacySeals,omitempty" firestore:"legacySeals"`
	DurationSeconds int64  `json:"durationSeconds,omitempty" firestore:"durationSeconds"`
	ShipClass       string `json:"shipClass,omitempty" firestore:"shipClass"`
	ClaimID         string `json:"claimId,omitempty" firestore:"claimId"`
	Deadline        int64  `json:"deadline,omitempty" firestore:"deadline"`
	TimerKind       string `json:"timerKind,omitempty" firestore:"timerKind"`
	// 보상 수량은 HTTP 입력이 아니라 서버에서 확인한 claim으로만 주입한다.
	rewardVerified bool
	effectDeadline int64
}

type SaltStarter struct {
	Eligible  bool  `json:"eligible" firestore:"eligible"`
	Purchased bool  `json:"purchased" firestore:"purchased"`
	Active    bool  `json:"active" firestore:"active"`
	StartsAt  int64 `json:"startsAt" firestore:"startsAt"`
	ExpiresAt int64 `json:"expiresAt" firestore:"expiresAt"`
	Reserved  bool  `json:"reserved" firestore:"reserved"`
}
type SaltSnapshot struct {
	Version     int64       `json:"version" firestore:"version"`
	ServerTime  int64       `json:"serverTime" firestore:"serverTime"`
	Initialized bool        `json:"initialized" firestore:"initialized"`
	Available   int64       `json:"available" firestore:"available"`
	Debt        int64       `json:"debt" firestore:"debt"`
	Starter     SaltStarter `json:"starter" firestore:"starter"`
}
type SaltReceipt struct {
	RequestID      string       `json:"requestId" firestore:"requestId"`
	Action         string       `json:"action" firestore:"action"`
	Snapshot       SaltSnapshot `json:"snapshot" firestore:"snapshot"`
	EffectDeadline int64        `json:"effectDeadline,omitempty" firestore:"effectDeadline"`
}

// signed balance가 정본이다. 화면에서 0으로 보이는 부족분도 원장에서 지우지 않는다.
// 구매 source를 PUID 사이에 복제하지 않아 불변식 2·4·5를 유지한다.
type saltState struct {
	Version           int64            `firestore:"version"`
	FirstSeenAt       int64            `firestore:"firstSeenAt"`
	Initialized       bool             `firestore:"initialized"`
	SealBalance       int64            `firestore:"sealBalance"`
	StarterStartsAt   int64            `firestore:"starterStartsAt"`
	StarterPurchased  bool             `firestore:"starterPurchased"`
	StarterActive     bool             `firestore:"starterActive"`
	StarterOrderKey   string           `firestore:"starterOrderKey"`
	StarterIntent     string           `firestore:"starterIntent"`
	StarterIntentAt   int64            `firestore:"starterIntentAt"`
	StarterPendingKey string           `firestore:"starterPendingKey"`
	Expeditions       map[string]int64 `firestore:"expeditions"`
}

func newSaltState(now time.Time) saltState {
	return saltState{FirstSeenAt: now.Unix(), Expeditions: map[string]int64{}}
}
func (s saltState) snapshot(now time.Time) SaltSnapshot {
	available, debt := max(int64(0), s.SealBalance), max(int64(0), -s.SealBalance)
	expires := int64(0)
	if s.StarterStartsAt > 0 {
		expires = s.StarterStartsAt + 72*3600
	}
	return SaltSnapshot{Version: s.Version, ServerTime: now.Unix(), Initialized: s.Initialized,
		Available: available, Debt: debt, Starter: SaltStarter{
			Eligible:  s.Initialized && s.StarterStartsAt > 0 && now.Unix() < expires && !s.StarterPurchased && s.StarterIntent == "",
			Purchased: s.StarterPurchased, Active: s.StarterActive, StartsAt: s.StarterStartsAt,
			ExpiresAt: expires, Reserved: s.StarterIntent != "",
		}}
}

var saltID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func saltError(message string) error {
	return platformerr.New(platformerr.CodePurchaseInvalid, message)
}
func (r SaltRequest) validate() error {
	if !saltID.MatchString(r.RequestID) || (r.TargetID != "" && !saltID.MatchString(r.TargetID)) {
		return saltError("정산 요청 번호를 확인해 주세요")
	}
	// 입력을 묵살하지 않는다. 행동에 맞지 않는 필드는 재시도 충돌로 숨지 않게 거부한다.
	allowed := ""
	switch r.Action {
	case "bootstrap":
		allowed = "legacy"
		if r.LegacySeals < 0 || r.LegacySeals > 1000000 {
			return saltError("기존 인장 수량을 확인해 주세요")
		}
	case "expedition_dispatch":
		allowed = "target,duration"
		if r.TargetID == "" || r.DurationSeconds < 10800 || r.DurationSeconds > 43200 {
			return saltError("원정 시간을 확인해 주세요")
		}
	case "expedition_collect", "cancel_starter":
		allowed = "target"
		if r.TargetID == "" {
			return saltError("정산 대상을 확인해 주세요")
		}
	case "spend_expedition":
		allowed = "target,deadline"
		if r.TargetID == "" || r.Deadline <= 0 {
			return saltError("단축할 원정과 완료 시각을 확인해 주세요")
		}
	case "spend_build":
		allowed = "target,ship,deadline"
		if r.TargetID == "" || saltBuildCost(r.ShipClass) <= 0 || r.Deadline <= 0 {
			return saltError("건조 대상을 확인해 주세요")
		}
	case "ad_reward":
		allowed = "claim"
		if !saltID.MatchString(r.ClaimID) {
			return saltError("보상 기록을 확인해 주세요")
		}
	case "ad_timer":
		allowed = "target,claim,timer,deadline"
		if !saltID.MatchString(r.ClaimID) || r.TargetID == "" || r.Deadline <= 0 || (r.TimerKind != "expedition" && r.TimerKind != "build") {
			return saltError("광고와 단축할 타이머를 확인해 주세요")
		}
	case "prepare_starter":
	default:
		return saltError("지원하지 않는 인장 행동이에요")
	}
	if (r.LegacySeals != 0 && allowed != "legacy") || (r.DurationSeconds != 0 && allowed != "target,duration") ||
		(r.ShipClass != "" && allowed != "target,ship,deadline") || (r.ClaimID != "" && allowed != "claim" && allowed != "target,claim,timer,deadline") ||
		(r.TimerKind != "" && allowed != "target,claim,timer,deadline") ||
		(r.Deadline != 0 && allowed != "target,deadline" && allowed != "target,ship,deadline" && allowed != "target,claim,timer,deadline") ||
		(r.TargetID != "" && allowed != "target" && allowed != "target,duration" && allowed != "target,deadline" && allowed != "target,ship,deadline" && allowed != "target,claim,timer,deadline") {
		return saltError("행동과 정산 내용이 맞지 않아요")
	}
	return nil
}

func saltDebit(s *saltState, amount int64) error {
	if amount <= 0 || s.SealBalance < amount {
		return saltError("사용할 인장이 부족해요")
	}
	s.SealBalance -= amount
	return nil
}

func saltApply(s *saltState, r SaltRequest, now time.Time) error {
	if r.Action == "bootstrap" {
		if s.Initialized {
			return saltError("기존 인장은 이미 정산했어요")
		}
		if r.LegacySeals > 0 {
			return saltError("기존 인장 기록의 확인이 필요해요")
		}
		s.Initialized = true
		s.SealBalance += r.LegacySeals
		s.Version++
		return nil
	}
	if !s.Initialized {
		return saltError("인장 기록을 먼저 연결해 주세요")
	}
	switch r.Action {
	case "expedition_dispatch":
		if _, exists := s.Expeditions[r.TargetID]; exists {
			return saltError("이미 등록한 원정이에요")
		}
		if len(s.Expeditions) >= 6 {
			return saltError("진행 중인 원정을 먼저 정산해 주세요")
		}
		s.Expeditions[r.TargetID] = now.Unix() + r.DurationSeconds
	case "expedition_collect":
		returnsAt, exists := s.Expeditions[r.TargetID]
		if !exists {
			return saltError("등록된 원정 기록을 확인해 주세요")
		}
		if now.Unix() < returnsAt {
			return platformerr.New(platformerr.CodeSaltExpeditionPending, "아직 원정의 귀환을 확인 중이에요")
		}
		delete(s.Expeditions, r.TargetID)
		if now.Unix() >= s.FirstSeenAt+24*3600 && s.StarterStartsAt == 0 {
			s.StarterStartsAt = now.Unix()
		}
	case "prepare_starter":
		if !s.snapshot(now).Starter.Eligible {
			return saltError("지금은 첫 출항 꾸러미를 구매할 수 없어요")
		}
		s.StarterIntent = r.RequestID
		s.StarterIntentAt = now.Unix()
	case "cancel_starter":
		if s.StarterIntent != r.TargetID {
			// 다른 세션의 활성 예약은 그대로 두고 이 대상의 늦은 prepare만 막는다.
			break
		}
		if s.StarterPendingKey != "" || s.StarterPurchased {
			return saltError("확인 중인 구매는 취소할 수 없어요")
		}
		s.StarterIntent = ""
		s.StarterIntentAt = 0
	case "spend_expedition":
		if now.Unix() >= r.Deadline {
			return saltError("이미 완료된 타이머는 인장을 쓰지 않아요")
		}
		returnsAt, exists := s.Expeditions[r.TargetID]
		if exists && returnsAt <= now.Unix() {
			return saltError("단축할 원정이 없어요")
		}
		if err := saltDebit(s, 10); err != nil {
			return err
		}
		if exists {
			s.Expeditions[r.TargetID] = now.Unix()
		}
	case "spend_build":
		if now.Unix() >= r.Deadline {
			return saltError("이미 완료된 타이머는 인장을 쓰지 않아요")
		}
		if err := saltDebit(s, saltBuildCost(r.ShipClass)); err != nil {
			return err
		}
	case "ad_reward":
		if !r.rewardVerified {
			return platformerr.New(platformerr.CodeClaimStateInvalid, "확정된 광고 보상을 기다리고 있어요")
		}
		s.SealBalance += 3
	case "ad_timer":
		if !r.rewardVerified || r.effectDeadline <= 0 {
			return platformerr.New(platformerr.CodeClaimStateInvalid, "확정된 타이머 광고 보상을 기다리고 있어요")
		}
		if r.TimerKind == "expedition" {
			if _, exists := s.Expeditions[r.TargetID]; exists {
				s.Expeditions[r.TargetID] = r.effectDeadline
			}
		}
	default:
		return saltError("지원하지 않는 인장 행동이에요")
	}
	s.Version++
	return nil
}

// 게임 데이터 원장의 빌드 산출물에서 읽는다. 클라이언트가 비용을 정하지 않는다.
func saltBuildCost(ship string) int64 { return saltRules.BuildCosts[ship] }
