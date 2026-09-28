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

type EconomyTesterEnrollment struct {
	Enabled   bool      `json:"enabled" firestore:"enabled"`
	Actor     string    `json:"actor" firestore:"actor"`
	UpdatedAt time.Time `json:"updatedAt" firestore:"updatedAt"`
}

func economyDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (l *Ledger) economyStatePath(puid string, test bool) (fspath.Path, error) {
	collection := "collection_economy"
	if test {
		collection = "collection_economy_test"
	}
	return l.paths.parse(collection + "/" + economyDigest(l.appID+"\x00"+puid))
}
func (l *Ledger) economyTransactionPath(puid, id string, test bool) (fspath.Path, error) {
	collection := "collection_economy_transactions"
	if test {
		collection = "collection_economy_test_transactions"
	}
	return l.paths.parse(collection + "/" + economyDigest(l.appID+"\x00"+puid+"\x00"+id))
}
func (l *Ledger) economyTesterPath(puid string) (fspath.Path, error) {
	// Enrollment is an app policy shared by Play production and Apple sandbox.
	// The wallets and orders still use their own environment-specific paths.
	return newPathBuilder(domain.EnvProduction).parse("collection_economy_testers/" + economyDigest(l.appID+"\x00"+puid))
}
func (l *Ledger) EconomyTester(ctx context.Context, puid string) (EconomyTesterEnrollment, error) {
	if l.appID != "lizard-tycoon" {
		return EconomyTesterEnrollment{}, economyError("이 앱은 수집 경제를 사용하지 않아요")
	}
	p, err := l.economyTesterPath(puid)
	if err != nil {
		return EconomyTesterEnrollment{}, err
	}
	snap, err := l.store.Get(ctx, p)
	if errors.Is(err, store.ErrNotFound) {
		return EconomyTesterEnrollment{}, nil
	}
	if err != nil {
		return EconomyTesterEnrollment{}, err
	}
	var enrollment EconomyTesterEnrollment
	if err := snap.DataTo(&enrollment); err != nil {
		return EconomyTesterEnrollment{}, err
	}
	return enrollment, nil
}
func (l *Ledger) SetEconomyTester(ctx context.Context, puid, actor string, enabled bool) (EconomyTesterEnrollment, error) {
	if l.appID != "lizard-tycoon" || puid == "" || actor == "" {
		return EconomyTesterEnrollment{}, economyError("시험 계정 등록 대상을 확인해 주세요")
	}
	p, err := l.economyTesterPath(puid)
	if err != nil {
		return EconomyTesterEnrollment{}, err
	}
	enrollment := EconomyTesterEnrollment{Enabled: enabled, Actor: actor, UpdatedAt: l.now().UTC()}
	if err := l.store.Set(ctx, p, enrollment); err != nil {
		return EconomyTesterEnrollment{}, err
	}
	return l.EconomyTester(ctx, puid)
}
func (l *Ledger) economyCatalogFor(test bool) (EconomyCatalog, error) {
	c, err := l.economyCatalog()
	if err != nil {
		return c, err
	}
	if test {
		c.Enabled, c.LaunchAt = c.TestEnabled, c.TestLaunchAt
	}
	return c, nil
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
func (l *Ledger) readEconomy(tx *store.Tx, puid string, now time.Time, test bool) (EconomyState, error) {
	p, e := l.economyStatePath(puid, test)
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
	enrollment, e := l.EconomyTester(ctx, puid)
	if e != nil {
		return EconomySnapshot{}, e
	}
	c, e := l.economyCatalogFor(enrollment.Enabled)
	if e != nil {
		return EconomySnapshot{}, e
	}
	p, e := l.economyStatePath(puid, enrollment.Enabled)
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
	enrollment, e := l.EconomyTester(ctx, puid)
	if e != nil {
		return EconomyReceipt{}, e
	}
	c, e := l.economyCatalogFor(enrollment.Enabled)
	if e != nil {
		return EconomyReceipt{}, e
	}

	if e := req.validate(); e != nil {
		return EconomyReceipt{}, e
	}
	p, e := l.economyStatePath(puid, enrollment.Enabled)
	if e != nil {
		return EconomyReceipt{}, e
	}
	rp, e := l.economyTransactionPath(puid, req.RequestID, enrollment.Enabled)
	if e != nil {
		return EconomyReceipt{}, e
	}
	otherRP, e := l.economyTransactionPath(puid, req.RequestID, !enrollment.Enabled)
	if e != nil {
		return EconomyReceipt{}, e
	}
	var result EconomyReceipt
	// One crypto seed stream per attempt is safe: only the committed result is observable.
	e = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		// A tester can later return to the public wallet. Keep a request ID
		// bound to its original ledger across that change.
		for _, transactionPath := range []fspath.Path{rp, otherRP} {
			exists, snap, e := tx.Exists(transactionPath)
			if e != nil {
				return e
			}
			if !exists {
				continue
			}
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
		currentTester, err := l.economyTesterInTx(tx, puid)
		if err != nil {
			return err
		}
		if currentTester != enrollment.Enabled {
			return economyError("시험 계정 상태가 바뀌었어요. 다시 조회해 주세요")
		}
		if !c.Enabled || c.LaunchAt <= 0 {
			return economyError("수집 경제는 아직 준비 중이에요")
		}
		now := l.now().UTC()
		state, e := l.readEconomy(tx, puid, now, enrollment.Enabled)
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

func (l *Ledger) recordEconomyFinancial(tx *store.Tx, orderKey, action string, order orderDoc, state EconomyState, now time.Time, test bool) error {
	collection := "collection_economy_financial"
	if test {
		collection = "collection_economy_test_financial"
	}
	p, err := l.paths.parse(collection + "/" + economyDigest(l.appID+"\x00"+orderKey+"\x00"+action))
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
func (l *Ledger) isEconomyTestPurchase(purchase domain.VerifiedPurchase) bool {
	return (purchase.Platform == domain.PlatformGooglePlay && purchase.IsTestPurchase != nil && *purchase.IsTestPurchase) ||
		(purchase.Platform == domain.PlatformAppStore && l.env == domain.EnvSandbox)
}

func (l *Ledger) grantEconomyPurchase(ctx context.Context, in GrantInput) (domain.GrantResult, error) {
	test := l.isEconomyTestPurchase(in.Purchase)
	c, e := l.economyCatalogFor(test)
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
	sp, e := l.economyStatePath(in.PlatformUserID, test)
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
			if order.IsTestPurchase != nil && *order.IsTestPurchase != test {
				return platformerr.New(platformerr.CodePurchaseReplayMismatch, "시험 주문 상태가 이전 구매와 달라요")
			}
			if !order.Tombstone && order.PlatformUserID != "" && order.PlatformUserID != in.PlatformUserID {
				return platformerr.New(platformerr.CodePurchaseOwnedByAnotherUser, "재화 구매는 연결된 원래 계정에서 복구해 주세요")
			}
		}
		now := l.now().UTC()
		state, e := l.readEconomy(tx, in.PlatformUserID, now, test)
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
				// An order recorded while sales were open is already accepted. Finish it
				// after the switch closes; only a new order needs the live sale gate.
				acceptedPending := exists && !order.Tombstone && order.State == domain.StatePending &&
					order.PlatformUserID == in.PlatformUserID
				if test && !acceptedPending {
					enrolled, err := l.economyTesterInTx(tx, in.PlatformUserID)
					if err != nil {
						return err
					}
					if !enrolled {
						return economyError("등록된 시험 계정에서만 시험 구매를 사용할 수 있어요")
					}
				}
				if (!c.Enabled || c.LaunchAt <= 0) && !acceptedPending {
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
			if e := l.recordEconomyFinancial(tx, in.Purchase.Key(), financialAction, order, state, now, test); e != nil {
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
	test := (order.IsTestPurchase != nil && *order.IsTestPurchase && order.Platform == domain.PlatformGooglePlay) ||
		(order.Platform == domain.PlatformAppStore && l.env == domain.EnvSandbox)
	state, e := l.readEconomy(tx, order.PlatformUserID, now, test)
	if e != nil {
		return e
	}
	state.Paid -= order.EconomyCredits
	state.Version++
	order.EconomyRefunded = true
	p, e := l.economyStatePath(order.PlatformUserID, test)
	if e != nil {
		return e
	}
	if err := l.recordEconomyFinancial(tx, orderKey, "refund", *order, state, now, test); err != nil {
		return err
	}
	return tx.Set(p, state)
}

type EconomyLookup struct {
	Found     bool          `json:"found"`
	State     *EconomyState `json:"state,omitempty"`
	TestFound bool          `json:"testFound"`
	TestState *EconomyState `json:"testState,omitempty"`
}

func (l *Ledger) economyTesterInTx(tx *store.Tx, puid string) (bool, error) {
	p, err := l.economyTesterPath(puid)
	if err != nil {
		return false, err
	}
	exists, snap, err := tx.Exists(p)
	if err != nil || !exists {
		return false, err
	}
	var enrollment EconomyTesterEnrollment
	if err := snap.DataTo(&enrollment); err != nil {
		return false, err
	}
	return enrollment.Enabled, nil
}

func (l *Ledger) LookupEconomy(ctx context.Context, puid string) (EconomyLookup, error) {
	if _, err := l.economyCatalog(); err != nil {
		return EconomyLookup{}, err
	}
	production, err := l.lookupEconomyState(ctx, puid, false)
	if err != nil {
		return EconomyLookup{}, err
	}
	test, err := l.lookupEconomyState(ctx, puid, true)
	if err != nil {
		return EconomyLookup{}, err
	}
	return EconomyLookup{Found: production != nil, State: production, TestFound: test != nil, TestState: test}, nil
}

func (l *Ledger) lookupEconomyState(ctx context.Context, puid string, test bool) (*EconomyState, error) {
	p, err := l.economyStatePath(puid, test)
	if err != nil {
		return nil, err
	}
	snap, err := l.store.Get(ctx, p)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state EconomyState
	if err = snap.DataTo(&state); err != nil {
		return nil, err
	}
	return &state, nil
}
