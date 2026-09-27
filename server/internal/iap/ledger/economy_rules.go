package ledger

import (
	"crypto/rand"
	"embed"
	"encoding/json"
	"math/big"
	"regexp"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

//go:embed lizard_economy.json
var economyFiles embed.FS

// EconomyCatalog is versioned with purchase receipts. It is never supplied by a client.
type EconomyCatalog struct {
	LaunchAt          int64                  `json:"launch_at"`
	Version           string                 `json:"version"`
	Enabled           bool                   `json:"enabled"`
	AppID             string                 `json:"app_id"`
	PurchaseMarkets   []string               `json:"purchase_markets"`
	DrawCost          int64                  `json:"draw_cost"`
	PityLimit         int                    `json:"pity_limit"`
	ChoiceCost        int64                  `json:"choice_cost"`
	Weights           map[string]int         `json:"weights"`
	DuplicateShards   map[string]int64       `json:"duplicate_shards"`
	ResearchCosts     []int64                `json:"research_costs"`
	ShardPackCost     int64                  `json:"shard_pack_cost"`
	ShardPackAmount   int64                  `json:"shard_pack_amount"`
	WeeklyCrystals    int64                  `json:"weekly_crystals"`
	WeeklyShards      int64                  `json:"weekly_shards"`
	WeeklyExpeditions int                    `json:"weekly_expeditions"`
	ExpeditionSeconds int64                  `json:"expedition_seconds"`
	ExpeditionShards  int64                  `json:"expedition_shards"`
	Packs             map[string]EconomyPack `json:"packs"`
	Collections       []EconomyCollection    `json:"collections"`
}
type EconomyPack struct {
	Crystals     int64 `json:"crystals"`
	PriceKRW     int64 `json:"price_krw"`
	Once         bool  `json:"once"`
	CommonChoice int   `json:"common_choice,omitempty"`
}
type EconomyCollection struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	ReleaseOffsetDays int            `json:"release_offset_days"`
	Morphs            []EconomyMorph `json:"morphs"`
}
type EconomyMorph struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Rarity   string   `json:"rarity"`
	Palette  []string `json:"palette"`
	HueShift float64  `json:"hue_shift"`
}

type EconomyState struct {
	Version              int64           `json:"version" firestore:"version"`
	Paid                 int64           `json:"paid" firestore:"paid"`
	Free                 int64           `json:"free" firestore:"free"`
	Shards               int64           `json:"shards" firestore:"shards"`
	Tokens               int64           `json:"tokens" firestore:"tokens"`
	Pity                 int             `json:"pity" firestore:"pity"`
	Unlocked             map[string]bool `json:"unlocked" firestore:"unlocked"`
	Research             map[string]int  `json:"research" firestore:"research"`
	StarterPurchased     bool            `json:"starterPurchased" firestore:"starterPurchased"`
	StarterChoices       int             `json:"starterChoices" firestore:"starterChoices"`
	ExpeditionCollection string          `json:"expeditionCollection" firestore:"expeditionCollection"`
	ExpeditionReturnAt   int64           `json:"expeditionReturnAt" firestore:"expeditionReturnAt"`
	WeeklyKey            string          `json:"weeklyKey" firestore:"weeklyKey"`
	WeeklyExpeditions    int             `json:"weeklyExpeditions" firestore:"weeklyExpeditions"`
	WeeklyClaimed        bool            `json:"weeklyClaimed" firestore:"weeklyClaimed"`
}
type EconomyRequest struct {
	freeOnly     bool
	RequestID    string `json:"requestId" firestore:"requestId"`
	Action       string `json:"action" firestore:"action"`
	CollectionID string `json:"collectionId,omitempty" firestore:"collectionId"`
	TargetID     string `json:"targetId,omitempty" firestore:"targetId"`
	Count        int    `json:"count,omitempty" firestore:"count"`
}
type EconomyDraw struct {
	MorphID   string `json:"morphId" firestore:"morphId"`
	Rarity    string `json:"rarity" firestore:"rarity"`
	Duplicate bool   `json:"duplicate" firestore:"duplicate"`
	Shards    int64  `json:"shards" firestore:"shards"`
}
type EconomyReceipt struct {
	RequestID      string        `json:"requestId" firestore:"requestId"`
	Action         string        `json:"action" firestore:"action"`
	CatalogVersion string        `json:"catalogVersion" firestore:"catalogVersion"`
	State          EconomyState  `json:"state" firestore:"state"`
	Draws          []EconomyDraw `json:"draws" firestore:"draws"`
}

func LizardEconomyCatalog() (EconomyCatalog, error) {
	b, err := economyFiles.ReadFile("lizard_economy.json")
	if err != nil {
		return EconomyCatalog{}, err
	}
	var c EconomyCatalog
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, c.validate()
}
func (c EconomyCatalog) validate() error {
	if c.AppID != "lizard-tycoon" || c.Version == "" || c.DrawCost <= 0 || c.DrawCost > 1000000 || (c.Enabled && c.LaunchAt <= 0) || c.ShardPackCost <= 0 || c.ShardPackAmount <= 0 || c.WeeklyCrystals < 0 || c.WeeklyShards < 0 || c.WeeklyExpeditions < 1 || c.ExpeditionShards < 1 || c.PityLimit < 1 || c.ChoiceCost < 1 || c.Weights["common"]+c.Weights["rare"]+c.Weights["legendary"] != 10000 || len(c.ResearchCosts) != 5 || c.ExpeditionSeconds <= 0 {
		return economyError("경제 설정이 올바르지 않아요")
	}
	ids := map[string]bool{}
	for _, col := range c.Collections {
		if col.ID == "" || ids[col.ID] {
			return economyError("컬렉션 설정이 올바르지 않아요")
		}
		ids[col.ID] = true
		counts := map[string]int{}
		for _, m := range col.Morphs {
			if m.ID == "" || ids[m.ID] || c.Weights[m.Rarity] <= 0 || c.DuplicateShards[m.Rarity] <= 0 {
				return economyError("모프 설정이 올바르지 않아요")
			}
			ids[m.ID] = true
			counts[m.Rarity]++
		}
		if counts["common"] != 6 || counts["rare"] != 4 || counts["legendary"] != 2 {
			return economyError("컬렉션 수량이 올바르지 않아요")
		}
	}
	for _, cost := range c.ResearchCosts {
		if cost <= 0 {
			return economyError("연구 설정이 올바르지 않아요")
		}
	}
	for _, p := range c.Packs {
		if p.Crystals <= 0 || p.Crystals > 1000000 {
			return economyError("재화 상품 설정이 올바르지 않아요")
		}
	}
	return nil
}
func economyError(message string) error {
	return platformerr.New(platformerr.CodeProductNotAllowed, message)
}
func newEconomyState(now time.Time) EconomyState {
	return EconomyState{Unlocked: map[string]bool{}, Research: map[string]int{}}
}
func secureEconomyRoll(n int) (int, error) {
	v, e := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if e != nil {
		return 0, e
	}
	return int(v.Int64()), nil
}

var economyRequestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,96}$`)

func (r EconomyRequest) validate() error {
	if !economyRequestPattern.MatchString(r.RequestID) || len(r.CollectionID) > 64 || len(r.TargetID) > 64 || (r.Count != 0 && r.Count != 1 && r.Count != 10) {
		return platformerr.New(platformerr.CodeRequestInvalid, "거래 정보가 올바르지 않아요")
	}
	switch r.Action {
	case "draw", "exchange", "research", "shards", "expedition_start", "expedition_claim", "weekly", "starter_choice":
	default:
		return platformerr.New(platformerr.CodeRequestInvalid, "알 수 없는 거래예요")
	}
	return nil
}
func (s EconomyState) clone() EconomyState {
	u := map[string]bool{}
	for k, v := range s.Unlocked {
		u[k] = v
	}
	s.Unlocked = u
	r := map[string]int{}
	for k, v := range s.Research {
		r[k] = v
	}
	s.Research = r
	return s
}
func (c EconomyCatalog) collection(id string, s EconomyState, now time.Time) (EconomyCollection, error) {
	for _, col := range c.Collections {
		if col.ID == id && c.LaunchAt > 0 && now.Unix() >= c.LaunchAt+int64(col.ReleaseOffsetDays)*86400 {
			return col, nil
		}
	}
	return EconomyCollection{}, economyError("이 컬렉션은 아직 열리지 않았어요")
}
func (c EconomyCatalog) spend(s *EconomyState, amount int64, freeOnly bool) error {
	if s.Paid < 0 {
		return economyError("환불 잔액을 먼저 확인해 주세요")
	}
	available := s.Free + s.Paid
	if freeOnly {
		available = s.Free
	}
	if amount <= 0 || amount > available {
		return economyError("크리스털이 부족해요")
	}
	free := min(amount, s.Free)
	s.Free -= free
	s.Paid -= amount - free
	return nil
}
func weekKey(now time.Time) string {
	y, w := now.UTC().ISOWeek()
	return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006") + "-" + big.NewInt(int64(w)).String()
}

// applyEconomy returns a new state: rejected operations never partially debit the caller.
// Random values are injected for tests; production uses crypto/rand, not client seeds.
func (c EconomyCatalog) applyEconomy(original EconomyState, req EconomyRequest, now time.Time, roll func(int) (int, error)) (EconomyReceipt, error) {
	if err := req.validate(); err != nil {
		return EconomyReceipt{}, err
	}
	s := original.clone()
	if s.Free < 0 || s.Shards < 0 || s.Tokens < 0 || s.Pity < 0 || s.Pity >= c.PityLimit || s.Version < 0 || s.Paid > 1000000000000 || s.Paid < -1000000000000 || s.Free > 1000000000000 || s.Shards > 1000000000000 || s.Tokens > 1000000000000 {
		return EconomyReceipt{}, economyError("경제 상태를 확인할 수 없어요")
	}
	if s.WeeklyKey != weekKey(now) {
		s.WeeklyKey = weekKey(now)
		s.WeeklyExpeditions = 0
		s.WeeklyClaimed = false
	}
	draws := []EconomyDraw{}
	var col EconomyCollection
	if req.Action == "draw" || req.Action == "exchange" || req.Action == "research" || req.Action == "starter_choice" || req.Action == "expedition_start" {
		var err error
		col, err = c.collection(req.CollectionID, s, now)
		if err != nil {
			return EconomyReceipt{}, err
		}
	}
	unlock := func(m EconomyMorph) {
		duplicate := s.Unlocked[m.ID]
		shards := int64(0)
		if duplicate {
			shards = c.DuplicateShards[m.Rarity]
			s.Shards += shards
		} else {
			s.Unlocked[m.ID] = true
		}
		draws = append(draws, EconomyDraw{m.ID, m.Rarity, duplicate, shards})
	}
	switch req.Action {
	case "draw":
		count := req.Count
		if count == 0 {
			count = 1
		}
		if err := c.spend(&s, c.DrawCost*int64(count), req.freeOnly); err != nil {
			return EconomyReceipt{}, err
		}
		for range count {
			rarity := "legendary"
			if s.Pity < c.PityLimit-1 {
				v, err := roll(10000)
				if err != nil {
					return EconomyReceipt{}, err
				}
				if v < 0 || v >= 10000 {
					return EconomyReceipt{}, economyError("추첨을 완료하지 못했어요")
				}
				if v < c.Weights["common"] {
					rarity = "common"
				} else if v < c.Weights["common"]+c.Weights["rare"] {
					rarity = "rare"
				}
			}
			pool := []EconomyMorph{}
			for _, m := range col.Morphs {
				if m.Rarity == rarity {
					pool = append(pool, m)
				}
			}
			i, err := roll(len(pool))
			if err != nil {
				return EconomyReceipt{}, err
			}
			if i < 0 || i >= len(pool) {
				return EconomyReceipt{}, economyError("추첨을 완료하지 못했어요")
			}
			unlock(pool[i])
			s.Tokens++
			if rarity == "legendary" {
				s.Pity = 0
			} else {
				s.Pity++
			}
		}
	case "exchange", "starter_choice":
		var chosen *EconomyMorph
		for i := range col.Morphs {
			m := &col.Morphs[i]
			if m.ID == req.TargetID {
				chosen = m
				break
			}
		}
		if chosen == nil || s.Unlocked[req.TargetID] {
			return EconomyReceipt{}, economyError("해금할 모프를 선택해 주세요")
		}
		if req.Action == "exchange" {
			if chosen.Rarity != "legendary" || s.Tokens < c.ChoiceCost {
				return EconomyReceipt{}, economyError("선택 증표가 부족해요")
			}
			s.Tokens -= c.ChoiceCost
		} else {
			if chosen.Rarity != "common" || s.StarterChoices < 1 {
				return EconomyReceipt{}, economyError("선택권이 없어요")
			}
			s.StarterChoices--
		}
		unlock(*chosen)
	case "shards":
		if err := c.spend(&s, c.ShardPackCost, req.freeOnly); err != nil {
			return EconomyReceipt{}, err
		}
		s.Shards += c.ShardPackAmount
	case "research":
		if req.TargetID != "breeding" && req.TargetID != "expedition" {
			return EconomyReceipt{}, economyError("연구 대상을 확인해 주세요")
		}
		key := col.ID + "_" + req.TargetID
		level := s.Research[key]
		if level < 0 || level >= len(c.ResearchCosts) {
			return EconomyReceipt{}, economyError("완료한 연구예요")
		}
		cost := c.ResearchCosts[level]
		if s.Shards < cost {
			return EconomyReceipt{}, economyError("연구 조각이 부족해요")
		}
		s.Shards -= cost
		s.Research[key] = level + 1
	case "expedition_start":
		if s.ExpeditionReturnAt != 0 {
			return EconomyReceipt{}, economyError("탐사가 이미 진행 중이에요")
		}
		s.ExpeditionCollection = col.ID
		s.ExpeditionReturnAt = now.Unix() + c.ExpeditionSeconds
	case "expedition_claim":
		if s.ExpeditionReturnAt == 0 || now.Unix() < s.ExpeditionReturnAt {
			return EconomyReceipt{}, economyError("탐사가 아직 돌아오지 않았어요")
		}
		s.Shards += c.ExpeditionShards + int64(s.Research[s.ExpeditionCollection+"_expedition"])*4
		s.ExpeditionReturnAt = 0
		s.ExpeditionCollection = ""
		s.WeeklyExpeditions++
	case "weekly":
		if s.WeeklyClaimed || s.WeeklyExpeditions < c.WeeklyExpeditions {
			return EconomyReceipt{}, economyError("이번 주 탐사 목표를 먼저 완료해 주세요")
		}
		s.Free += c.WeeklyCrystals
		s.Shards += c.WeeklyShards
		s.WeeklyClaimed = true
	}
	s.Version++
	return EconomyReceipt{req.RequestID, req.Action, c.Version, s, draws}, nil
}
