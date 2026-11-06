package ledger

import (
	"context"
	"time"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

type saltTransactionDoc struct {
	Request   SaltRequest `firestore:"request"`
	Receipt   SaltReceipt `firestore:"receipt"`
	CreatedAt time.Time   `firestore:"createdAt"`
}
type saltFinancialDoc struct {
	PlatformUserID string    `firestore:"platformUserId"`
	OrderKey       string    `firestore:"orderKey"`
	Action         string    `firestore:"action"`
	Delta          int64     `firestore:"delta"`
	Balance        int64     `firestore:"balance"`
	Version        int64     `firestore:"version"`
	CreatedAt      time.Time `firestore:"createdAt"`
}

func (l *Ledger) saltPath(collection, key string) (fspath.Path, error) {
	return l.paths.parse(collection + "/" + contentRequestDigest(key))
}
func (l *Ledger) saltStatePath(puid string) (fspath.Path, error) {
	return l.saltPath("salt_compass_states", puid)
}
func (l *Ledger) readSalt(tx *store.Tx, puid string, now time.Time) (saltState, error) {
	p, err := l.saltStatePath(puid)
	if err != nil {
		return saltState{}, err
	}
	exists, snap, err := tx.Exists(p)
	if err != nil {
		return saltState{}, err
	}
	s := newSaltState(now)
	if exists {
		if err := snap.DataTo(&s); err != nil {
			return saltState{}, err
		}
	}
	if s.Expeditions == nil {
		s.Expeditions = map[string]int64{}
	}
	if s.Version < 0 || s.FirstSeenAt <= 0 || s.SealBalance < -1000000000000 || s.SealBalance > 1000000000000 {
		return saltState{}, platformerr.New(platformerr.CodeLedgerStateInvalid, "인장 원장을 확인해 주세요")
	}
	return s, nil
}
func (l *Ledger) SaltSnapshot(ctx context.Context, puid string) (SaltSnapshot, error) {
	if l.appID != saltAppID || puid == "" {
		return SaltSnapshot{}, saltError("이 앱은 항해 인장 정산을 사용하지 않아요")
	}
	var out SaltSnapshot
	err := l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		now := l.now().UTC()
		s, err := l.readSalt(tx, puid, now)
		if err != nil {
			return err
		}
		out = s.snapshot(now)
		return nil
	})
	return out, err
}

// TransactSalt는 request 재시도와 다른 requestId를 쓴 동일 effect 모두 차단한다.
// receipt·effect·state를 함께 커밋해 응답 유실이 추가 차감/적립으로 이어지지 않는다(불변식 2·5).
func (l *Ledger) TransactSalt(ctx context.Context, puid string, r SaltRequest) (SaltReceipt, error) {
	if l.appID != saltAppID || puid == "" {
		return SaltReceipt{}, saltError("이 앱은 항해 인장 정산을 사용하지 않아요")
	}
	if err := r.validate(); err != nil {
		return SaltReceipt{}, err
	}
	rp, err := l.saltPath("salt_compass_transactions", puid+"\x00"+r.RequestID)
	if err != nil {
		return SaltReceipt{}, err
	}
	sp, err := l.saltStatePath(puid)
	if err != nil {
		return SaltReceipt{}, err
	}
	effectKey := r.Action + "\x00" + r.TargetID
	if r.Action == "ad_reward" {
		effectKey = "ad_reward\x00" + r.ClaimID
	}
	if r.Action == "prepare_starter" || r.Action == "cancel_starter" {
		effectKey = ""
	}
	var ep fspath.Path
	if effectKey != "" {
		ep, err = l.saltPath("salt_compass_effects", puid+"\x00"+effectKey)
		if err != nil {
			return SaltReceipt{}, err
		}
	}
	// 화면을 닫을 때의 미청구 취소가 prepare보다 먼저 도착해도 이후 예약을 막는다.
	// 다른 intent의 활성 예약은 보존하고, 이 대상의 미도착 prepare만 막는다.
	var cancellationPath fspath.Path
	if r.Action == "prepare_starter" || r.Action == "cancel_starter" {
		intent := r.RequestID
		if r.Action == "cancel_starter" {
			intent = r.TargetID
		}
		cancellationPath, err = l.saltPath("salt_compass_starter_cancellations", puid+"\x00"+intent)
		if err != nil {
			return SaltReceipt{}, err
		}
	}
	var out SaltReceipt
	err = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		exists, snap, err := tx.Exists(rp)
		if err != nil {
			return err
		}
		storedRequest := r
		storedRequest.rewardVerified = false
		storedRequest.legacyVerified = false
		if exists {
			var prior saltTransactionDoc
			if err := snap.DataTo(&prior); err != nil {
				return err
			}
			if prior.Request != storedRequest {
				return platformerr.New(platformerr.CodePurchaseReplayMismatch, "정산 재시도 내용이 달라요")
			}
			out = prior.Receipt
			return nil
		}
		now := l.now().UTC()
		s, err := l.readSalt(tx, puid, now)
		if err != nil {
			return err
		}
		if effectKey != "" {
			exists, _, err := tx.Exists(ep)
			if err != nil {
				return err
			}
			if exists {
				return platformerr.New(platformerr.CodePurchaseReplayMismatch, "이미 정산한 행동이에요")
			}
		}
		if r.Action == "bootstrap" {
			if err := l.verifySaltLegacy(tx, puid, r.LegacySeals); err != nil {
				return err
			}
			r.legacyVerified = true
		}
		if r.Action == "prepare_starter" {
			cancelled, _, err := tx.Exists(cancellationPath)
			if err != nil {
				return err
			}
			if cancelled {
				return saltError("중단한 구매 준비는 다시 시작할 수 없어요")
			}
		}
		if err := saltApply(&s, r, now); err != nil {
			return err
		}
		out = SaltReceipt{RequestID: r.RequestID, Action: r.Action, Snapshot: s.snapshot(now)}
		doc := saltTransactionDoc{Request: storedRequest, Receipt: out, CreatedAt: now}
		if err := tx.Set(sp, s); err != nil {
			return err
		}
		if err := tx.Create(rp, doc); err != nil {
			return err
		}
		if r.Action == "cancel_starter" {
			if err := tx.Set(cancellationPath, map[string]any{"cancelledAt": now}); err != nil {
				return err
			}
		}
		if effectKey != "" {
			return tx.Create(ep, doc)
		}
		return nil
	})
	return out, err
}

func (l *Ledger) isSaltConsumable(entitlement string) bool {
	return l.appID == saltAppID && saltRules.Credits[entitlement] > 0
}

// grantSaltPurchase는 소비·복원으로 복제할 수 없는 금융 처리다. 영구 상품은 기존 Grant를 쓴다.
// 예약 없음·다른 소유자·두 번째 청구는 durable hold를 저장한 뒤 오류를 반환한다.
// AlreadyGranted 성공을 반환하면 grantAndComplete가 consume/finish하므로 보류는 오류다(불변식 7).
func (l *Ledger) grantSaltPurchase(ctx context.Context, in GrantInput) (domain.GrantResult, error) {
	if in.PlatformUserID == "" || !in.Purchase.Platform.IsMarket() || in.Purchase.CanonicalID == "" ||
		(in.Purchase.State != domain.StateActive && in.Purchase.State != domain.StateRevoked) {
		return domain.GrantResult{}, saltError("검증된 구매 정보가 필요해요")
	}
	op, err := l.paths.order(in.Purchase.Key())
	if err != nil {
		return domain.GrantResult{}, err
	}
	sp, err := l.saltStatePath(in.PlatformUserID)
	if err != nil {
		return domain.GrantResult{}, err
	}
	var out domain.GrantResult
	var held error
	err = l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		held = nil
		exists, snap, err := tx.Exists(op)
		if err != nil {
			return err
		}
		var order orderDoc
		if exists {
			if err := snap.DataTo(&order); err != nil {
				return err
			}
			if err := checkReplay(order, in); err != nil {
				return err
			}
		}
		now := l.now().UTC()
		s, err := l.readSalt(tx, in.PlatformUserID, now)
		if err != nil {
			return err
		}
		if exists && !order.Tombstone && order.PlatformUserID != "" && order.PlatformUserID != in.PlatformUserID {
			// 원래 소유자 주문은 보존하고 요청자별 보류 증거만 추가한다.
			p, err := l.saltPath("salt_compass_reconciliation", in.Purchase.Key()+"\x00"+in.PlatformUserID)
			if err != nil {
				return err
			}
			prior, _, err := tx.Exists(p)
			if err != nil {
				return err
			}
			held = platformerr.New(platformerr.CodePurchaseOwnedByAnotherUser, "구매한 원래 계정에서 복구해 주세요")
			if !prior {
				return tx.Create(p, map[string]any{"orderKey": in.Purchase.Key(), "platformUserId": in.PlatformUserID, "reason": "owner_mismatch", "createdAt": now})
			}
			return nil
		}
		var barrier sandboxResetBarrierDoc
		usesBarrier := l.env == domain.EnvSandbox && in.Purchase.Platform == domain.PlatformAppStore
		if usesBarrier {
			barrier, err = l.readSandboxResetBarrier(tx, in.PlatformUserID)
			if err != nil {
				return err
			}
			blockedAt, err := blockingSandboxResetAt(l.env, order, barrier, in.Purchase)
			if err != nil {
				return err
			}
			if !blockedAt.IsZero() {
				// reset 이전 증빙은 지급하지 않고 기존 cleanup 계약으로 반환한다.
				if order.PlatformUserID == "" {
					order.PlatformUserID = in.PlatformUserID
				}
				if order.EntitlementID == "" {
					order.EntitlementID = in.EntitlementID
				}
				if err := l.revokeSaltOrder(tx, &order, in.Purchase.Key(), now); err != nil {
					return err
				}
				order.Platform = in.Purchase.Platform
				order.ProductID = in.Purchase.ProductID
				order.ProviderOrderID = in.Purchase.ProviderOrderID
				order.PurchasedAt = in.Purchase.PurchasedAt
				order.ObservedAt = blockedAt
				order.State = domain.StateRevoked
				order.Tombstone = false
				order.SandboxReset = effectiveSandboxResetMark(order, barrier)
				order.CreatedAt = firstNonZero(order.CreatedAt, now)
				order.UpdatedAt = now
				if err := tx.Set(op, order); err != nil {
					return err
				}
				if err := l.touchSandboxResetBarrier(tx, in.PlatformUserID, barrier, now); err != nil {
					return err
				}
				out = domain.GrantResult{EntitlementID: in.EntitlementID, BlockedBySandboxReset: true}
				return nil
			}
		}
		entPath, err := l.paths.internalEntitlement(in.PlatformUserID, in.EntitlementID)
		if err != nil {
			return err
		}
		ent, err := l.readEntitlement(tx, entPath, in.EntitlementID)
		if err != nil {
			return err
		}
		// 환불은 종결 상태다. 더 나중 active timestamp도 환불을 되돌리지 못한다.
		terminal := order.SaltRefunded || order.State == domain.StateRevoked
		if terminal {
			if in.Purchase.State == domain.StateActive {
				held = saltError("이미 환불되었거나 확인 중인 구매예요")
			}
			out = domain.GrantResult{EntitlementID: in.EntitlementID, AlreadyGranted: true}
			return nil
		}
		if order.SaltReconciliation && order.SaltHoldReason == "migration_required" && s.Initialized && !order.SaltGranted && !order.SaltRefunded {
			order.SaltReconciliation = false
			order.SaltHoldReason = ""
		}
		if order.SaltReconciliation && in.Purchase.State == domain.StateActive {
			held = saltError("구매 확인이 필요해요")
			return nil
		}
		financialAction := ""
		delta := int64(0)
		applied := false
		if in.Purchase.State == domain.StateActive && !order.SaltGranted {
			reason := ""
			if !s.Initialized {
				reason = "migration_required"
			}
			if in.EntitlementID == saltStarterID && reason == "" {
				switch {
				case s.StarterPurchased && s.StarterOrderKey != in.Purchase.Key():
					reason = "starter_already_purchased"
				case s.StarterIntent == "":
					reason = "starter_intent_missing"
				case s.StarterPendingKey != "" && s.StarterPendingKey != in.Purchase.Key():
					reason = "starter_other_pending"
				case in.Purchase.PurchasedAt.Unix() < s.StarterIntentAt:
					reason = "starter_predates_intent"
				}
			}
			if reason != "" {
				order.SaltReconciliation = true
				order.SaltHoldReason = reason
				held = saltError("청구된 구매를 확인 중이에요. 다시 구매하지 마세요")
			} else {
				delta = saltRules.Credits[in.EntitlementID]
				s.SealBalance += delta
				s.Version++
				order.SaltGranted, order.SaltCredits = true, delta
				financialAction = "credit"
				applied = true
				if in.EntitlementID == saltStarterID {
					s.StarterPurchased, s.StarterActive = true, true
					s.StarterOrderKey = in.Purchase.Key()
					s.StarterIntent = ""
					s.StarterPendingKey = ""
				}
			}
		} else if in.Purchase.State == domain.StateRevoked && order.SaltGranted && !order.SaltRefunded {
			delta = -order.SaltCredits
			s.SealBalance += delta
			s.Version++
			order.SaltRefunded = true
			financialAction = "refund"
			if in.EntitlementID == saltStarterID {
				s.StarterActive = false
			}
		}
		order.PlatformUserID = in.PlatformUserID
		order.EntitlementID = in.EntitlementID
		order.Platform = in.Purchase.Platform
		order.ProductID = in.Purchase.ProductID
		order.ProviderOrderID = in.Purchase.ProviderOrderID
		order.PlatformAccountIDHash = domain.HashAccountID(in.Purchase.PlatformAccountID)
		order.IsTestPurchase = in.Purchase.IsTestPurchase
		order.State = in.Purchase.State
		order.PurchasedAt = in.Purchase.PurchasedAt
		order.ObservedAt = in.Purchase.ObservedAt
		order.CreatedAt = firstNonZero(order.CreatedAt, now)
		order.UpdatedAt = now
		order.Tombstone = false
		if in.Purchase.State == domain.StateRevoked {
			releaseSaltPending(&s, order, in.Purchase.Key())
		}
		if order.SaltGranted {
			ent.Sources[in.Purchase.Key()] = domain.Source{Platform: in.Purchase.Platform, ProductID: in.Purchase.ProductID,
				State: in.Purchase.State, PurchasedAt: in.Purchase.PurchasedAt, ObservedAt: in.Purchase.ObservedAt, UpdatedAt: now}
			if err := l.writeEntitlement(tx, in.PlatformUserID, ent, now); err != nil {
				return err
			}
		}
		if financialAction != "" {
			p, err := l.saltPath("salt_compass_financial", in.Purchase.Key()+"\x00"+financialAction)
			if err != nil {
				return err
			}
			if err := tx.Create(p, saltFinancialDoc{PlatformUserID: in.PlatformUserID, OrderKey: in.Purchase.Key(), Action: financialAction, Delta: delta, Balance: s.SealBalance, Version: s.Version, CreatedAt: now}); err != nil {
				return err
			}
		}
		if err := tx.Set(sp, s); err != nil {
			return err
		}
		if err := tx.Set(op, order); err != nil {
			return err
		}
		if in.Purchase.State == domain.StateRevoked {
			outPath, err := l.paths.outbox(in.Purchase.Key())
			if err != nil {
				return err
			}
			if err := tx.Delete(outPath); err != nil {
				return err
			}
		}
		if usesBarrier {
			if err := l.touchSandboxResetBarrier(tx, in.PlatformUserID, barrier, now); err != nil {
				return err
			}
		}
		out = domain.GrantResult{EntitlementID: in.EntitlementID, Granted: applied, AlreadyGranted: !applied}
		return nil
	})
	if err != nil {
		return domain.GrantResult{}, err
	}
	if held != nil {
		return domain.GrantResult{}, held
	}
	// 금전 커밋 뒤 권리 조회가 실패해도 재검증으로 복구한다. 조회 실패를 빈 성공으로 바꾸지 않는다.
	out.Entitlements, err = l.ListActive(ctx, in.PlatformUserID)
	return out, err
}

// revokeSaltOrder는 webhook의 기존 order 트랜잭션에서 다른 쓰기 전에 호출한다.
func (l *Ledger) revokeSaltOrder(tx *store.Tx, order *orderDoc, key string, now time.Time) error {
	outPath, err := l.paths.outbox(key)
	if err != nil {
		return err
	}
	if !order.SaltGranted || order.SaltRefunded {
		if order.EntitlementID == saltStarterID && !order.SaltGranted {
			s, err := l.readSalt(tx, order.PlatformUserID, now)
			if err != nil {
				return err
			}
			if releaseSaltPending(&s, *order, key) {
				sp, err := l.saltStatePath(order.PlatformUserID)
				if err != nil {
					return err
				}
				if err := tx.Set(sp, s); err != nil {
					return err
				}
			}
		}
		return tx.Delete(outPath)
	}
	s, err := l.readSalt(tx, order.PlatformUserID, now)
	if err != nil {
		return err
	}
	ep, err := l.paths.internalEntitlement(order.PlatformUserID, order.EntitlementID)
	if err != nil {
		return err
	}
	ent, err := l.readEntitlement(tx, ep, order.EntitlementID)
	if err != nil {
		return err
	}
	s.SealBalance -= order.SaltCredits
	s.Version++
	if order.EntitlementID == saltStarterID {
		s.StarterActive = false
	}
	sp, err := l.saltStatePath(order.PlatformUserID)
	if err != nil {
		return err
	}
	p, err := l.saltPath("salt_compass_financial", key+"\x00refund")
	if err != nil {
		return err
	}
	order.SaltRefunded = true
	src := ent.Sources[key]
	src.State = domain.StateRevoked
	src.ObservedAt = order.ObservedAt
	src.UpdatedAt = now
	ent.Sources[key] = src
	if err := l.writeEntitlement(tx, order.PlatformUserID, ent, now); err != nil {
		return err
	}
	if err := tx.Create(p, saltFinancialDoc{PlatformUserID: order.PlatformUserID, OrderKey: key, Action: "refund", Delta: -order.SaltCredits, Balance: s.SealBalance, Version: s.Version, CreatedAt: now}); err != nil {
		return err
	}
	if err := tx.Delete(outPath); err != nil {
		return err
	}
	return tx.Set(sp, s)
}

// 지급된 스타터의 once latch는 유지한다. 미지급 주문의 실제 취소만 예약을 해제한다.
func releaseSaltPending(s *saltState, order orderDoc, key string) bool {
	if order.EntitlementID != saltStarterID || order.SaltGranted || s.StarterPurchased || s.StarterPendingKey != key {
		return false
	}
	s.StarterPendingKey = ""
	s.StarterIntent = ""
	s.StarterIntentAt = 0
	s.Version++
	return true
}

// 대기 주문을 한 예약의 canonical key에 결속해 만료 뒤에도 같은 주문만 복구한다.
func (l *Ledger) recordSaltPending(ctx context.Context, in GrantInput) error {
	if in.PlatformUserID == "" || !in.Purchase.Platform.IsMarket() || in.Purchase.CanonicalID == "" || in.Purchase.State != domain.StatePending {
		return saltError("검증된 대기 구매 정보가 필요해요")
	}
	op, err := l.paths.order(in.Purchase.Key())
	if err != nil {
		return err
	}
	sp, err := l.saltStatePath(in.PlatformUserID)
	if err != nil {
		return err
	}
	return l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		exists, snap, err := tx.Exists(op)
		if err != nil {
			return err
		}
		var order orderDoc
		if exists {
			if err := snap.DataTo(&order); err != nil {
				return err
			}
			if err := checkReplay(order, in); err != nil {
				return err
			}
			if order.PlatformUserID != "" && order.PlatformUserID != in.PlatformUserID {
				return platformerr.New(platformerr.CodePurchaseOwnedByAnotherUser, "구매한 원래 계정에서 복구해 주세요")
			}
			if order.State != domain.StatePending {
				return nil
			}
		}
		now := l.now().UTC()
		s, err := l.readSalt(tx, in.PlatformUserID, now)
		if err != nil {
			return err
		}
		if !s.Initialized {
			return saltError("인장 기록을 먼저 연결해 주세요")
		}
		if in.EntitlementID == saltStarterID {
			if s.StarterIntent == "" || s.StarterPurchased || (s.StarterPendingKey != "" && s.StarterPendingKey != in.Purchase.Key()) {
				return saltError("확인 중인 꾸러미 구매가 있어요")
			}
			s.StarterPendingKey = in.Purchase.Key()
			s.Version++
		}
		order.PlatformUserID = in.PlatformUserID
		order.EntitlementID = in.EntitlementID
		order.Platform = in.Purchase.Platform
		order.ProductID = in.Purchase.ProductID
		order.ProviderOrderID = in.Purchase.ProviderOrderID
		order.IsTestPurchase = in.Purchase.IsTestPurchase
		order.State = domain.StatePending
		order.PurchasedAt = in.Purchase.PurchasedAt
		order.ObservedAt = in.Purchase.ObservedAt
		order.CreatedAt = firstNonZero(order.CreatedAt, now)
		order.UpdatedAt = now
		if err := tx.Set(sp, s); err != nil {
			return err
		}
		return tx.Set(op, order)
	})
}

// 서버에서 확인한 claim만 소비자가 주입한다. HTTP JSON에는 이 필드가 없다.
func (r SaltRequest) WithVerifiedReward() SaltRequest { r.rewardVerified = true; return r }
