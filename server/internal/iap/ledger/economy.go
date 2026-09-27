package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

type EconomySnapshot struct {
	AvailableCollections []string       `json:"availableCollections"`
	ServerTime           int64          `json:"serverTime"`
	State                EconomyState   `json:"state"`
	Catalog              EconomyCatalog `json:"catalog"`
	Linked               bool           `json:"linked"`
	Enabled              bool           `json:"enabled"`
}
type economyTransactionDoc struct {
	Request   EconomyRequest `firestore:"request"`
	Receipt   EconomyReceipt `firestore:"receipt"`
	CreatedAt time.Time      `firestore:"createdAt"`
}

func economyDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (l *Ledger) economyStatePath(puid string) (fspath.Path, error) {
	return l.paths.parse("collection_economy/" + economyDigest(l.appID+"\x00"+puid))
}
func (l *Ledger) economyTransactionPath(puid, id string) (fspath.Path, error) {
	return l.paths.parse("collection_economy_transactions/" + economyDigest(l.appID+"\x00"+puid+"\x00"+id))
}
func (l *Ledger) economyCatalog() (EconomyCatalog, error) {
	if l.economy != nil {
		return *l.economy, nil
	}
	if l.appID != "lizard-tycoon" {
		return EconomyCatalog{}, economyError("이 앱은 수집 경제를 사용하지 않아요")
	}
	return LizardEconomyCatalog()
}

// WithEconomyCatalog makes staged validation use the same immutable catalog as production.
func (l *Ledger) WithEconomyCatalog(c EconomyCatalog) *Ledger { l.economy = &c; return l }
func (l *Ledger) readEconomy(tx *store.Tx, puid string, now time.Time) (EconomyState, error) {
	p, e := l.economyStatePath(puid)
	if e != nil {
		return EconomyState{}, e
	}
	exists, snap, e := tx.Exists(p)
	if e != nil {
		return EconomyState{}, e
	}
	state := newEconomyState(now)
	if exists {
		if e := snap.DataTo(&state); e != nil {
			return state, e
		}
	}
	return state.clone(), nil
}
func (l *Ledger) EconomySnapshot(ctx context.Context, puid string) (EconomySnapshot, error) {
	c, e := l.economyCatalog()
	if e != nil {
		return EconomySnapshot{}, e
	}
	p, e := l.economyStatePath(puid)
	if e != nil {
		return EconomySnapshot{}, e
	}
	state := newEconomyState(l.now())
	snap, e := l.store.Get(ctx, p)
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		return EconomySnapshot{}, e
	}
	if e == nil {
		if e := snap.DataTo(&state); e != nil {
			return EconomySnapshot{}, e
		}
	}
	if state.WeeklyKey != weekKey(l.now()) {
		state.WeeklyKey = weekKey(l.now())
		state.WeeklyExpeditions = 0
		state.WeeklyClaimed = false
	}
	available := []string{}
	for _, col := range c.Collections {
		if _, err := c.collection(col.ID, state, l.now()); err == nil {
			available = append(available, col.ID)
		}
	}
	return EconomySnapshot{State: state.clone(), Catalog: c, Enabled: c.Enabled && c.LaunchAt > 0, AvailableCollections: available, ServerTime: l.now().Unix()}, nil
}
func (l *Ledger) TransactFreeEconomy(ctx context.Context, puid string, req EconomyRequest) (EconomyReceipt, error) {
	req.freeOnly = true
	return l.TransactEconomy(ctx, puid, req)
}
func (l *Ledger) TransactEconomy(ctx context.Context, puid string, req EconomyRequest) (EconomyReceipt, error) {
	c, e := l.economyCatalog()
	if e != nil {
		return EconomyReceipt{}, e
	}

	if e := req.validate(); e != nil {
		return EconomyReceipt{}, e
	}
	p, e := l.economyStatePath(puid)
	if e != nil {
		return EconomyReceipt{}, e
	}
	rp, e := l.economyTransactionPath(puid, req.RequestID)
	if e != nil {
		return EconomyReceipt{}, e
	}
	var result EconomyReceipt
	// One crypto seed stream per attempt is safe: only the committed result is observable.
	e = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		exists, snap, e := tx.Exists(rp)
		if e != nil {
			return e
		}
		if exists {
			var d economyTransactionDoc
			if e := snap.DataTo(&d); e != nil {
				return e
			}
			comparison := req
			comparison.freeOnly = false
			if d.Request != comparison {
				return platformerr.New(platformerr.CodePurchaseReplayMismatch, "같은 요청 번호의 내용이 달라요")
			}
			result = d.Receipt
			return nil
		}
		if !c.Enabled || c.LaunchAt <= 0 {
			return economyError("수집 경제는 아직 준비 중이에요")
		}
		now := l.now().UTC()
		state, e := l.readEconomy(tx, puid, now)
		if e != nil {
			return e
		}
		receipt, e := c.applyEconomy(state, req, now, secureEconomyRoll)
		if e != nil {
			return e
		}
		if e := tx.Set(p, receipt.State); e != nil {
			return e
		}
		storedRequest := req
		storedRequest.freeOnly = false
		if e := tx.Create(rp, economyTransactionDoc{storedRequest, receipt, now}); e != nil {
			return e
		}
		result = receipt
		return nil
	})
	return result, e
}
func (l *Ledger) isEconomyPack(id string) bool {
	c, e := l.economyCatalog()
	if e != nil {
		return false
	}
	_, ok := c.Packs[id]
	return ok
}

type economyFinancialDoc struct {
	Action         string    `firestore:"action"`
	ProductID      string    `firestore:"productId"`
	CatalogVersion string    `firestore:"catalogVersion"`
	PaidDelta      int64     `firestore:"paidDelta"`
	WalletVersion  int64     `firestore:"walletVersion"`
	CreatedAt      time.Time `firestore:"createdAt"`
}

func (l *Ledger) recordEconomyFinancial(tx *store.Tx, orderKey, action string, order orderDoc, state EconomyState, now time.Time) error {
	p, err := l.paths.parse("collection_economy_financial/" + economyDigest(l.appID+"\x00"+orderKey+"\x00"+action))
	if err != nil {
		return err
	}
	delta := order.EconomyCredits
	if action == "refund" {
		delta = -delta
	}
	return tx.Create(p, economyFinancialDoc{action, order.EntitlementID, order.EconomyVersion, delta, state.Version, now})
}

// grantEconomyPurchase is atomic with processed_orders. IAP invariants 2, 3, 4, 5, 7:
// a re-submitted or consumed receipt can never create a second balance or change owners.
func (l *Ledger) grantEconomyPurchase(ctx context.Context, in GrantInput) (domain.GrantResult, error) {
	c, e := l.economyCatalog()
	if e != nil {
		return domain.GrantResult{}, e
	}
	pack, ok := c.Packs[in.EntitlementID]
	if !ok {
		return domain.GrantResult{}, economyError("재화 상품은 아직 준비 중이에요")
	}
	op, e := l.paths.order(in.Purchase.Key())
	if e != nil {
		return domain.GrantResult{}, e
	}
	sp, e := l.economyStatePath(in.PlatformUserID)
	if e != nil {
		return domain.GrantResult{}, e
	}
	var result domain.GrantResult
	e = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		exists, snap, e := tx.Exists(op)
		if e != nil {
			return e
		}
		var order orderDoc
		if exists {
			if e := snap.DataTo(&order); e != nil {
				return e
			}
			if e := checkReplay(order, in); e != nil {
				return e
			}
			if !order.Tombstone && order.PlatformUserID != "" && order.PlatformUserID != in.PlatformUserID {
				return platformerr.New(platformerr.CodePurchaseOwnedByAnotherUser, "재화 구매는 연결된 원래 계정에서 복구해 주세요")
			}
		}
		now := l.now().UTC()
		state, e := l.readEconomy(tx, in.PlatformUserID, now)
		if e != nil {
			return e
		}
		if exists && (order.EconomyRefunded || domain.IsStaleUpdate(order.State, in.Purchase.State, order.ObservedAt, in.Purchase.ObservedAt)) {
			result = domain.GrantResult{EntitlementID: in.EntitlementID, AlreadyGranted: true}
			return nil
		}
		applied := false
		financialAction := ""
		switch in.Purchase.State {
		case domain.StateActive:
			if !order.EconomyGranted {
				if !c.Enabled || c.LaunchAt <= 0 {
					return economyError("재화 상품은 아직 준비 중이에요")
				}
				if pack.Once && state.StarterPurchased {
					return economyError("스타터는 계정당 한 번만 구매할 수 있어요")
				}
				state.Paid += pack.Crystals
				state.Version++
				if pack.Once {
					state.StarterPurchased = true
					state.StarterChoices += pack.CommonChoice
				}
				order.EconomyGranted = true
				order.EconomyCredits = pack.Crystals
				order.EconomyVersion = c.Version
				applied = true
				financialAction = "credit"
			}
		case domain.StateRevoked:
			if order.EconomyGranted && !order.EconomyRefunded {
				state.Paid -= order.EconomyCredits
				state.Version++
				order.EconomyRefunded = true
				financialAction = "refund"
			}
		default:
			return economyError("완료된 구매만 적립할 수 있어요")
		}
		order.PlatformUserID = in.PlatformUserID
		order.EntitlementID = in.EntitlementID
		order.Platform = in.Purchase.Platform
		order.ProductID = in.Purchase.ProductID
		order.State = in.Purchase.State
		order.ObservedAt = in.Purchase.ObservedAt
		order.PurchasedAt = in.Purchase.PurchasedAt
		order.ProviderOrderID = in.Purchase.ProviderOrderID
		order.IsTestPurchase = in.Purchase.IsTestPurchase
		order.PlatformAccountIDHash = domain.HashAccountID(in.Purchase.PlatformAccountID)
		order.Tombstone = false
		order.CreatedAt = firstNonZero(order.CreatedAt, now)
		order.UpdatedAt = now
		if financialAction != "" {
			if e := l.recordEconomyFinancial(tx, in.Purchase.Key(), financialAction, order, state, now); e != nil {
				return e
			}
		}
		if e := tx.Set(sp, state); e != nil {
			return e
		}
		if e := tx.Set(op, order); e != nil {
			return e
		}
		result = domain.GrantResult{EntitlementID: in.EntitlementID, Granted: applied, AlreadyGranted: !applied}
		return nil
	})
	return result, e
}

// revokeEconomyOrder runs inside the webhook transaction, before any writes.
func (l *Ledger) revokeEconomyOrder(tx *store.Tx, order *orderDoc, orderKey string, now time.Time) error {
	if !order.EconomyGranted || order.EconomyRefunded {
		return nil
	}
	state, e := l.readEconomy(tx, order.PlatformUserID, now)
	if e != nil {
		return e
	}
	state.Paid -= order.EconomyCredits
	state.Version++
	order.EconomyRefunded = true
	p, e := l.economyStatePath(order.PlatformUserID)
	if e != nil {
		return e
	}
	if err := l.recordEconomyFinancial(tx, orderKey, "refund", *order, state, now); err != nil {
		return err
	}
	return tx.Set(p, state)
}

type EconomyLookup struct {
	Found bool          `json:"found"`
	State *EconomyState `json:"state,omitempty"`
}

func (l *Ledger) LookupEconomy(ctx context.Context, puid string) (EconomyLookup, error) {
	if _, err := l.economyCatalog(); err != nil {
		return EconomyLookup{}, err
	}
	p, err := l.economyStatePath(puid)
	if err != nil {
		return EconomyLookup{}, err
	}
	snap, err := l.store.Get(ctx, p)
	if errors.Is(err, store.ErrNotFound) {
		return EconomyLookup{}, nil
	}
	if err != nil {
		return EconomyLookup{}, err
	}
	var state EconomyState
	if err = snap.DataTo(&state); err != nil {
		return EconomyLookup{}, err
	}
	return EconomyLookup{true, &state}, nil
}
