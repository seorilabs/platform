package ledger

import (
	"context"
	_ "embed"
	"encoding/json"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
	"sort"
	"time"
)

//go:embed defense_catalog.json
var defenseCatalogJSON []byte

type DefensePack struct {
	ID            string `json:"id"`
	EntitlementID string `json:"entitlementId"`
	Units         int    `json:"units"`
	USDCents      int    `json:"usdCents"`
}
type DefenseAction struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Stat       string `json:"stat,omitempty"`
	BaseCost   int    `json:"baseCost"`
	CostStep   int    `json:"costStep,omitempty"`
	Step       int    `json:"step,omitempty"`
	MaxLevel   int    `json:"maxLevel,omitempty"`
	Multiplier int    `json:"multiplier,omitempty"`
}
type DefenseCatalog struct {
	Version string          `json:"version"`
	Packs   []DefensePack   `json:"packs"`
	Actions []DefenseAction `json:"actions"`
}
type DefenseState struct {
	Version  int64          `json:"version" firestore:"version"`
	Balance  int64          `json:"balance" firestore:"balance"`
	Debt     int64          `json:"debt" firestore:"debt"`
	Enabled  bool           `json:"enabled" firestore:"enabled"`
	Upgrades map[string]int `json:"upgrades" firestore:"upgrades"`
	Weapons  []string       `json:"weapons" firestore:"weapons"`
}
type DefenseSnapshot struct {
	State   DefenseState   `json:"state"`
	Catalog DefenseCatalog `json:"catalog"`
}
type DefenseRequest struct {
	RequestID      string `json:"requestId" firestore:"requestId"`
	ActionID       string `json:"actionId" firestore:"actionId"`
	CatalogVersion string `json:"catalogVersion" firestore:"catalogVersion"`
	RunID          string `json:"runId,omitempty" firestore:"runId"`
}
type DefenseReceipt struct {
	RequestID      string       `json:"requestId" firestore:"requestId"`
	ActionID       string       `json:"actionId" firestore:"actionId"`
	CatalogVersion string       `json:"catalogVersion" firestore:"catalogVersion"`
	RunID          string       `json:"runId" firestore:"runId"`
	Cost           int          `json:"cost" firestore:"cost"`
	State          DefenseState `json:"state" firestore:"state"`
}
type defenseDoc struct {
	Request   DefenseRequest `firestore:"request"`
	Receipt   DefenseReceipt `firestore:"receipt"`
	CreatedAt time.Time      `firestore:"createdAt"`
}

func defenseError(message string) error {
	return platformerr.New(platformerr.CodeContentReplayMismatch, message)
}
func DefenseCatalogCurrent() (DefenseCatalog, error) {
	var c DefenseCatalog
	err := json.Unmarshal(defenseCatalogJSON, &c)
	if err != nil {
		return c, err
	}
	if c.Version == "" || len(c.Packs) != 5 || len(c.Actions) != 8 {
		return c, defenseError("방어 카탈로그를 확인해 주세요")
	}
	return c, nil
}
func defenseUnits(c DefenseCatalog, ents map[string]entitlementDoc) (int64, int64, error) {
	var remaining, debt int64
	for _, p := range c.Packs {
		for _, src := range ents[p.EntitlementID].Sources {
			used := src.ContentUnitsConsumed
			if used < 0 || used > p.Units {
				return 0, 0, defenseError("구매 소비 단위를 확인해 주세요")
			}
			if src.State == domain.StateActive {
				remaining += int64(p.Units - used)
			}
			if src.State == domain.StateRevoked {
				debt += int64(used)
			}
		}
	}
	return remaining - debt, debt, nil
}
func defenseView(s DefenseState, balance, debt int64) DefenseState {
	s.Balance = balance
	s.Debt = debt
	s.Enabled = balance >= 0
	if s.Upgrades == nil {
		s.Upgrades = map[string]int{}
	}
	if s.Weapons == nil {
		s.Weapons = []string{}
	}
	return s
}
func defenseApply(c DefenseCatalog, s DefenseState, r DefenseRequest) (DefenseState, int, error) {
	if r.CatalogVersion != c.Version {
		return s, 0, defenseError("방어 카탈로그가 변경됐어요")
	}
	var a DefenseAction
	for _, item := range c.Actions {
		if item.ID == r.ActionID {
			a = item
		}
	}
	if a.ID == "" {
		return s, 0, defenseError("알 수 없는 방어 행동이에요")
	}
	if (a.Kind == "consumable" && !boxRequestIDPattern.MatchString(r.RunID)) || (a.Kind != "consumable" && r.RunID != "") {
		return s, 0, defenseError("전투 요청 범위를 확인해 주세요")
	}
	cost := a.BaseCost
	// 불변식 8: 가격과 지급량은 서버 카탈로그만 결정한다.
	levels := map[string]int{}
	for k, v := range s.Upgrades {
		levels[k] = v
	}
	s.Upgrades = levels
	s.Weapons = append([]string{}, s.Weapons...)
	switch a.Kind {
	case "upgrade":
		level := s.Upgrades[a.Stat]
		if level < 0 || level >= a.MaxLevel {
			return s, 0, defenseError("강화 상한에 도달했어요")
		}
		cost += level * a.CostStep
		s.Upgrades[a.Stat] = level + 1
	case "weapon":
		for _, id := range s.Weapons {
			if id == a.ID {
				return s, 0, defenseError("이미 보유한 병기예요")
			}
		}
		s.Weapons = append(s.Weapons, a.ID)
	}
	if s.Balance < int64(cost) {
		return s, 0, platformerr.New(platformerr.CodeContentTicketEmpty, "방어 재화가 부족해요")
	}
	s.Balance -= int64(cost)
	s.Version++
	return s, cost, nil
}
func (l *Ledger) readDefense(tx *store.Tx, puid string, c DefenseCatalog) (DefenseState, map[string]entitlementDoc, error) {
	var state DefenseState
	p, e := l.saltPath("defense_states", puid)
	if e != nil {
		return state, nil, e
	}
	exists, snap, e := tx.Exists(p)
	if e != nil {
		return state, nil, e
	}
	if exists {
		if e = snap.DataTo(&state); e != nil {
			return state, nil, e
		}
	}
	ents := map[string]entitlementDoc{}
	for _, pack := range c.Packs {
		path, e := l.paths.internalEntitlement(puid, pack.EntitlementID)
		if e != nil {
			return state, nil, e
		}
		ok, s, e := tx.Exists(path)
		if e != nil {
			return state, nil, e
		}
		if ok {
			var ent entitlementDoc
			if e = s.DataTo(&ent); e != nil {
				return state, nil, e
			}
			if ent.EntitlementID != "" && ent.EntitlementID != pack.EntitlementID {
				return state, nil, defenseError("구매 원장 종류를 확인해 주세요")
			}
			ents[pack.EntitlementID] = ent
		}
	}
	balance, debt, e := defenseUnits(c, ents)
	return defenseView(state, balance, debt), ents, e
}
func (l *Ledger) DefenseSnapshot(ctx context.Context, puid string) (DefenseSnapshot, error) {
	var out DefenseSnapshot
	if l.appID != "mechanical-frontier" || puid == "" {
		return out, defenseError("이 앱은 방어 자산을 지원하지 않아요")
	}
	c, e := DefenseCatalogCurrent()
	if e != nil {
		return out, e
	}
	out.Catalog = c
	e = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		s, _, err := l.readDefense(tx, puid, c)
		out.State = s
		return err
	})
	return out, e
}
func (l *Ledger) ActDefense(ctx context.Context, puid string, r DefenseRequest) (DefenseReceipt, error) {
	var out DefenseReceipt
	if l.appID != "mechanical-frontier" || puid == "" || !boxRequestIDPattern.MatchString(r.RequestID) {
		return out, defenseError("방어 요청을 확인해 주세요")
	}
	c, e := DefenseCatalogCurrent()
	if e != nil {
		return out, e
	}
	rp, e := l.saltPath("defense_actions", puid+"\x00"+r.RequestID)
	if e != nil {
		return out, e
	}
	sp, e := l.saltPath("defense_states", puid)
	if e != nil {
		return out, e
	}
	// 불변식 2·5: source 차감·영구 상태·immutable 영수증이 같은 transaction이다.
	e = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		exists, snap, err := tx.Exists(rp)
		if err != nil {
			return err
		}
		if exists {
			var doc defenseDoc
			if err = snap.DataTo(&doc); err != nil {
				return err
			}
			if doc.Request != r {
				return defenseError("이미 사용한 요청 ID의 내용이 달라요")
			}
			out = doc.Receipt
			return nil
		}
		state, ents, err := l.readDefense(tx, puid, c)
		if err != nil {
			return err
		}
		next, cost, err := defenseApply(c, state, r)
		if err != nil {
			return err
		}
		left := cost
		for _, pack := range c.Packs {
			ent := ents[pack.EntitlementID]
			keys := []string{}
			for key := range ent.Sources {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			changed := false
			for _, key := range keys {
				src := ent.Sources[key]
				if left == 0 || src.State != domain.StateActive {
					continue
				}
				n := min(left, pack.Units-src.ContentUnitsConsumed)
				if n <= 0 {
					continue
				}
				src.ContentUnitsConsumed += n
				ent.Sources[key] = src
				left -= n
				changed = true
			}
			if changed {
				path, err := l.paths.internalEntitlement(puid, pack.EntitlementID)
				if err != nil {
					return err
				}
				ent.UpdatedAt = l.now().UTC()
				if err = tx.Set(path, ent); err != nil {
					return err
				}
			}
		}
		if left != 0 {
			return defenseError("구매 잔액 정산을 확인해 주세요")
		}
		out = DefenseReceipt{RequestID: r.RequestID, ActionID: r.ActionID, CatalogVersion: c.Version, RunID: r.RunID, Cost: cost, State: next}
		if err = tx.Set(sp, next); err != nil {
			return err
		}
		return tx.Create(rp, defenseDoc{Request: r, Receipt: out, CreatedAt: l.now().UTC()})
	})
	return out, e
}
