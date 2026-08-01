package ledger

import (
	"context"
	"sort"
	"time"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

// orderDoc은 주문 원장 문서다. 불변식 5에 따라 삭제하지 않는다.
type orderDoc struct {
	PlatformUserID string `firestore:"platformUserId"`
	EntitlementID  string `firestore:"entitlementId"`

	Platform        domain.Platform `firestore:"platform"`
	ProductID       string          `firestore:"productId"`
	ProviderOrderID string          `firestore:"providerOrderId"`

	// 마켓 계정 참조는 원문이 아니라 해시로 저장한다. ADR 0005
	PlatformAccountIDHash string `firestore:"platformAccountIdHash"`

	State       domain.State `firestore:"state"`
	PurchasedAt time.Time    `firestore:"purchasedAt"`
	ObservedAt  time.Time    `firestore:"observedAt"`

	// Tombstone은 소유자를 모르는 환불이다.
	// 마켓이 먼저 환불을 알렸는데 우리가 그 주문을 모를 때 만든다.
	Tombstone bool `firestore:"tombstone"`

	// SandboxReset은 운영자가 App Store sandbox 구매내역을 지웠다는 표식이다.
	// 초기화 전에 산 거래는 Apple 쪽에는 없고 기기에만 남아 있다.
	SandboxReset *sandboxResetMark `firestore:"sandboxReset,omitempty"`

	CreatedAt time.Time `firestore:"createdAt"`
	UpdatedAt time.Time `firestore:"updatedAt"`
}

// sandboxResetMark는 초기화 시점을 남긴다.
//
// RequestID는 어느 운영 작업이 지웠는지 되짚기 위한 것이다.
type sandboxResetMark struct {
	RequestID string    `firestore:"requestId"`
	ResetAt   time.Time `firestore:"resetAt"`
}

// entitlementDoc은 내부 원장이다. sources를 들고 있다.
type entitlementDoc struct {
	EntitlementID string                   `firestore:"entitlementId"`
	Active        bool                     `firestore:"active"`
	Sources       map[string]domain.Source `firestore:"sources"`
	UpdatedAt     time.Time                `firestore:"updatedAt"`
}

// projectionDoc은 클라이언트가 읽는 공개 문서다.
//
// sources를 노출하지 않는다. 마켓 계정 해시 같은 내부 정보가 들어 있다.
type projectionDoc struct {
	EntitlementID string    `firestore:"entitlementId"`
	Active        bool      `firestore:"active"`
	UpdatedAt     time.Time `firestore:"updatedAt"`
}

// GrantInput은 지급 요청이다.
type GrantInput struct {
	PlatformUserID string
	EntitlementID  string
	Purchase       domain.VerifiedPurchase
}

// Ledger는 Firestore 기반 entitlement 원장이다.
type Ledger struct {
	store *store.Client
	paths pathBuilder
	env   domain.Environment
	now   func() time.Time
}

// New는 원장을 만든다.
func New(s *store.Client, env domain.Environment) *Ledger {
	return &Ledger{
		store: s,
		paths: newPathBuilder(env),
		env:   env,
		now:   time.Now,
	}
}

// WithClock은 시계를 주입한다. 테스트용이다.
func (l *Ledger) WithClock(now func() time.Time) *Ledger {
	l.now = now
	return l
}

// Environment는 이 원장의 환경이다.
func (l *Ledger) Environment() domain.Environment { return l.env }

// Grant는 검증된 구매를 지급한다.
//
// 트랜잭션 안에서 주문 원장, 내부 원장, 공개 projection을 한 번에 갱신한다.
// 셋이 어긋나면 클라이언트가 보는 값과 원장이 달라진다.
//
// 강제하는 불변식:
//   - 2: granted와 alreadyGranted는 배타적
//   - 3: stale 갱신 억제
//   - 4: cross-user 자동 이전 금지
//   - 5: 원장 문서 삭제 금지
//   - 6: active = OR(sources)
func (l *Ledger) Grant(ctx context.Context, in GrantInput) (domain.GrantResult, error) {
	if in.PlatformUserID == "" || in.EntitlementID == "" {
		return domain.GrantResult{}, platformerr.New(platformerr.CodeInternal, "지급 정보가 올바르지 않아요")
	}
	if !in.Purchase.Platform.Valid() {
		return domain.GrantResult{}, platformerr.New(platformerr.CodePlatformInvalid, "알 수 없는 마켓이에요")
	}

	orderKey := in.Purchase.Key()
	var result domain.GrantResult

	err := l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := l.now()

		orderPath, err := l.paths.order(orderKey)
		if err != nil {
			return err
		}

		exists, snap, err := tx.Exists(orderPath)
		if err != nil {
			return err
		}

		var order orderDoc
		var sandboxResetAt time.Time
		if exists {
			if err := snap.DataTo(&order); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid, "원장을 읽지 못했어요")
			}

			// 불변식 4. 다른 사용자의 구매는 자동으로 옮기지 않는다.
			// tombstone은 소유자가 없으므로 예외다.
			if !order.Tombstone && order.PlatformUserID != "" &&
				order.PlatformUserID != in.PlatformUserID {
				return platformerr.New(platformerr.CodePurchaseOwnedByAnotherUser,
					"다른 계정에서 구매한 상품이에요")
			}

			// 같은 주문인데 상품이나 마켓이 다르면 위조이거나 버그다.
			if err := checkReplay(order, in); err != nil {
				return err
			}

			// 초기화 차단은 불변식 3보다 먼저 본다.
			//
			// 한 번 차단해 revoked로 만든 주문에 같은 거래가 다시 오면
			// revoked(rank 3) > active(rank 2)라 stale로 걸린다. 그러면
			// alreadyGranted=true가 돌아가고, 앱은 받은 적 없는 상품을
			// 가진 것으로 안다. 순서가 곧 정확성이다.
			sandboxResetAt, err = blockingSandboxResetAt(l.env, order, in.Purchase)
			if err != nil {
				return err
			}

			// 불변식 3. 늦게 온 갱신은 무시한다.
			if sandboxResetAt.IsZero() &&
				domain.IsStaleUpdate(order.State, in.Purchase.State, order.ObservedAt, in.Purchase.ObservedAt) {
				// 이미 반영된 상태를 그대로 돌려준다.
				// 여기서 granted를 true로 주면 클라이언트가 중복 지급으로 오해한다.
				result = domain.GrantResult{
					AlreadyGranted: true,
					EntitlementID:  in.EntitlementID,
				}
				return nil
			}
		}

		// 내부 원장을 읽는다. Firestore 트랜잭션은 모든 읽기가 쓰기보다 앞서야 한다.
		intPath, err := l.paths.internalEntitlement(in.PlatformUserID, in.EntitlementID)
		if err != nil {
			return err
		}
		ent, err := l.readEntitlement(tx, intPath, in.EntitlementID)
		if err != nil {
			return err
		}

		// 초기화 이전 거래다. 지급하지 않고 revoked로 확정한다.
		//
		// 여기서 그냥 거부하고 끝내면 기기의 미완료 거래가 그대로 남아
		// 다음 구매도 같은 거래를 돌려받는다. revoked로 커밋해 두면
		// 검증 서비스가 Apple finish를 부르고, 클라이언트는 sync 뒤
		// 새 구매를 시작할 수 있다.
		if !sandboxResetAt.IsZero() {
			src := ent.Sources[orderKey]
			src.Platform = in.Purchase.Platform
			src.ProductID = in.Purchase.ProductID
			src.State = domain.StateRevoked
			src.PurchasedAt = in.Purchase.PurchasedAt
			src.ObservedAt = sandboxResetAt
			src.UpdatedAt = now
			ent.Sources[orderKey] = src

			if err := l.writeEntitlement(tx, in.PlatformUserID, ent, now); err != nil {
				return err
			}

			order.State = domain.StateRevoked
			order.ObservedAt = sandboxResetAt
			order.UpdatedAt = now
			if err := tx.Set(orderPath, order); err != nil {
				return err
			}

			// 지급도 중복 지급도 아니다. 둘 다 false가 유일하게 맞다.
			result = domain.GrantResult{
				EntitlementID:         in.EntitlementID,
				BlockedBySandboxReset: true,
			}
			return nil
		}

		prevSource, hadSource := ent.Sources[orderKey]
		alreadyActive := hadSource && prevSource.State == domain.StateActive &&
			order.PlatformUserID == in.PlatformUserID && order.State == domain.StateActive

		// 여기서부터 쓰기다.
		order = orderDoc{
			PlatformUserID:        in.PlatformUserID,
			EntitlementID:         in.EntitlementID,
			Platform:              in.Purchase.Platform,
			ProductID:             in.Purchase.ProductID,
			ProviderOrderID:       in.Purchase.ProviderOrderID,
			PlatformAccountIDHash: domain.HashAccountID(in.Purchase.PlatformAccountID),
			State:                 in.Purchase.State,
			PurchasedAt:           in.Purchase.PurchasedAt,
			ObservedAt:            in.Purchase.ObservedAt,
			Tombstone:             false,
			CreatedAt:             firstNonZero(order.CreatedAt, now),
			UpdatedAt:             now,
		}
		if err := tx.Set(orderPath, order); err != nil {
			return err
		}

		ent.Sources[orderKey] = domain.Source{
			Platform:    in.Purchase.Platform,
			ProductID:   in.Purchase.ProductID,
			State:       in.Purchase.State,
			PurchasedAt: in.Purchase.PurchasedAt,
			ObservedAt:  in.Purchase.ObservedAt,
			UpdatedAt:   now,
		}

		if err := l.writeEntitlement(tx, in.PlatformUserID, ent, now); err != nil {
			return err
		}

		// 불변식 2. 둘은 항상 배타적이다.
		result = domain.GrantResult{
			Granted:        !alreadyActive,
			AlreadyGranted: alreadyActive,
			EntitlementID:  in.EntitlementID,
		}
		return nil
	})
	if err != nil {
		return domain.GrantResult{}, err
	}

	// 활성 목록은 트랜잭션 밖에서 읽는다. 최종 일관성이지만
	// 트랜잭션 안에서 컬렉션을 훑으면 경합 범위가 넓어진다.
	list, err := l.ListActive(ctx, in.PlatformUserID)
	if err == nil {
		result.Entitlements = list
	}

	// 런타임에서도 불변식 2를 확인한다.
	// 위 로직이 바뀌어 깨지면 여기서 드러난다.
	if !result.Valid() {
		return domain.GrantResult{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"지급 결과가 올바르지 않아요")
	}
	return result, nil
}

// blockingSandboxResetAt은 이 구매가 sandbox 초기화 이전 거래인지 본다.
//
// 차단해야 하면 초기화 시각을, 아니면 zero를 돌려준다.
//
// 세 겹으로 막는다. production 원장, App Store 아닌 마켓, 표식 없는 주문은
// 이 판정에 들어오지 못한다. 초기화는 sandbox 테스트 도구이므로
// 실사용자 결제 경로가 여기에 닿을 방법이 없어야 한다.
func blockingSandboxResetAt(
	env domain.Environment,
	order orderDoc,
	p domain.VerifiedPurchase,
) (time.Time, error) {
	if env != domain.EnvSandbox ||
		p.Platform != domain.PlatformAppStore ||
		order.SandboxReset == nil ||
		order.SandboxReset.ResetAt.IsZero() {
		return time.Time{}, nil
	}

	// 표식은 있는데 구매 시각을 모르면 판정할 수 없다.
	// 모르는 채로 통과시키면 초기화한 거래를 다시 지급하게 된다.
	if p.PurchasedAt.IsZero() {
		return time.Time{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"구매 시각을 확인할 수 없어요")
	}

	// 초기화 시각 이후에 산 것은 진짜 새 구매다. 통과시킨다.
	if p.PurchasedAt.After(order.SandboxReset.ResetAt) {
		return time.Time{}, nil
	}
	return order.SandboxReset.ResetAt, nil
}

// MarkSandboxReset은 App Store sandbox 구매내역 초기화를 원장에 남긴다.
//
// 표식만 남기는 것이 아니라 주문과 entitlement를 revoked로 확정한다.
// 표식만 남기면 다음 검증까지 유저가 갖고 있지도 않은 상품을 보게 된다.
func (l *Ledger) MarkSandboxReset(
	ctx context.Context,
	puid, requestID string,
	orderKeys []string,
) error {
	if l.env != domain.EnvSandbox {
		return platformerr.New(platformerr.CodeLedgerStateInvalid,
			"sandbox 원장에서만 초기화할 수 있어요")
	}
	if puid == "" || requestID == "" {
		return platformerr.New(platformerr.CodeInternal, "초기화 요청이 올바르지 않아요")
	}

	for _, orderKey := range orderKeys {
		if err := l.markOneSandboxReset(ctx, puid, requestID, orderKey); err != nil {
			return err
		}
	}
	return nil
}

func (l *Ledger) markOneSandboxReset(ctx context.Context, puid, requestID, orderKey string) error {
	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := l.now()

		orderPath, err := l.paths.order(orderKey)
		if err != nil {
			return err
		}
		exists, snap, err := tx.Exists(orderPath)
		if err != nil {
			return err
		}
		if !exists {
			return platformerr.New(platformerr.CodePurchaseNotFound, "주문을 찾을 수 없어요")
		}

		var order orderDoc
		if err := snap.DataTo(&order); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid, "원장을 읽지 못했어요")
		}
		if order.Platform != domain.PlatformAppStore {
			return platformerr.New(platformerr.CodePlatformInvalid,
				"App Store 주문만 초기화할 수 있어요")
		}
		if order.PlatformUserID != puid {
			return platformerr.New(platformerr.CodePurchaseOwnedByAnotherUser,
				"다른 계정의 주문이에요")
		}

		intPath, err := l.paths.internalEntitlement(puid, order.EntitlementID)
		if err != nil {
			return err
		}
		ent, err := l.readEntitlement(tx, intPath, order.EntitlementID)
		if err != nil {
			return err
		}

		src := ent.Sources[orderKey]
		src.Platform = order.Platform
		src.ProductID = order.ProductID
		src.State = domain.StateRevoked
		src.PurchasedAt = order.PurchasedAt
		src.ObservedAt = now
		src.UpdatedAt = now
		ent.Sources[orderKey] = src

		if err := l.writeEntitlement(tx, puid, ent, now); err != nil {
			return err
		}

		order.State = domain.StateRevoked
		order.ObservedAt = now
		order.UpdatedAt = now
		order.SandboxReset = &sandboxResetMark{RequestID: requestID, ResetAt: now}
		return tx.Set(orderPath, order)
	})
}

// checkReplay는 같은 주문 키에 다른 내용이 오는지 본다.
func checkReplay(order orderDoc, in GrantInput) error {
	if order.Tombstone {
		return nil
	}
	mismatch := order.Platform != in.Purchase.Platform ||
		order.ProductID != in.Purchase.ProductID ||
		(order.EntitlementID != "" && order.EntitlementID != in.EntitlementID)

	if mismatch {
		return platformerr.New(platformerr.CodePurchaseReplayMismatch,
			"이전 구매 정보와 맞지 않아요")
	}
	return nil
}

// RecordPending은 아직 완료되지 않은 구매를 기록한다.
//
// 이미 active이거나 revoked면 아무것도 하지 않는다.
// pending이 확정 상태를 덮으면 안 된다.
func (l *Ledger) RecordPending(ctx context.Context, in GrantInput) error {
	orderKey := in.Purchase.Key()

	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := l.now()

		orderPath, err := l.paths.order(orderKey)
		if err != nil {
			return err
		}

		exists, snap, err := tx.Exists(orderPath)
		if err != nil {
			return err
		}

		if exists {
			var order orderDoc
			if err := snap.DataTo(&order); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid, "원장을 읽지 못했어요")
			}
			// 확정된 상태는 건드리지 않는다.
			if order.State == domain.StateActive || order.State == domain.StateRevoked {
				return nil
			}
			if !order.Tombstone && order.PlatformUserID != "" &&
				order.PlatformUserID != in.PlatformUserID {
				return platformerr.New(platformerr.CodePurchaseOwnedByAnotherUser,
					"다른 계정에서 구매한 상품이에요")
			}
		}

		return tx.Set(orderPath, orderDoc{
			PlatformUserID:        in.PlatformUserID,
			EntitlementID:         in.EntitlementID,
			Platform:              in.Purchase.Platform,
			ProductID:             in.Purchase.ProductID,
			ProviderOrderID:       in.Purchase.ProviderOrderID,
			PlatformAccountIDHash: domain.HashAccountID(in.Purchase.PlatformAccountID),
			State:                 domain.StatePending,
			PurchasedAt:           in.Purchase.PurchasedAt,
			ObservedAt:            in.Purchase.ObservedAt,
			CreatedAt:             now,
			UpdatedAt:             now,
		})
	})
}

// RevokeByCanonicalID는 환불을 반영한다.
//
// 주문을 모르면 tombstone을 만든다. 마켓이 환불을 먼저 알리고
// 우리가 그 구매를 아직 모르는 경우가 있기 때문이다.
// 나중에 그 구매가 검증되면 stale 억제가 재지급을 막는다.
func (l *Ledger) RevokeByCanonicalID(
	ctx context.Context,
	platform domain.Platform,
	canonicalID string,
	observedAt time.Time,
) error {
	orderKey := domain.OrderKey(platform, canonicalID)

	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := l.now()

		orderPath, err := l.paths.order(orderKey)
		if err != nil {
			return err
		}

		exists, snap, err := tx.Exists(orderPath)
		if err != nil {
			return err
		}

		if !exists {
			// 소유자를 모르는 환불. tombstone으로 남긴다.
			// 불변식 10. 알림만으로 신규 지급을 하지 않지만 기록은 남긴다.
			return tx.Set(orderPath, orderDoc{
				Platform:   platform,
				State:      domain.StateRevoked,
				ObservedAt: observedAt,
				Tombstone:  true,
				CreatedAt:  now,
				UpdatedAt:  now,
			})
		}

		var order orderDoc
		if err := snap.DataTo(&order); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid, "원장을 읽지 못했어요")
		}

		if domain.IsStaleUpdate(order.State, domain.StateRevoked, order.ObservedAt, observedAt) {
			return nil
		}

		// 소유자가 없으면 주문만 갱신한다.
		if order.PlatformUserID == "" || order.EntitlementID == "" {
			order.State = domain.StateRevoked
			order.ObservedAt = observedAt
			order.UpdatedAt = now
			return tx.Set(orderPath, order)
		}

		intPath, err := l.paths.internalEntitlement(order.PlatformUserID, order.EntitlementID)
		if err != nil {
			return err
		}
		ent, err := l.readEntitlement(tx, intPath, order.EntitlementID)
		if err != nil {
			return err
		}

		order.State = domain.StateRevoked
		order.ObservedAt = observedAt
		order.UpdatedAt = now
		if err := tx.Set(orderPath, order); err != nil {
			return err
		}

		src := ent.Sources[orderKey]
		src.Platform = order.Platform
		src.ProductID = order.ProductID
		src.State = domain.StateRevoked
		src.ObservedAt = observedAt
		src.UpdatedAt = now
		ent.Sources[orderKey] = src

		// 완료 대기가 남아 있으면 지운다. 회수된 구매를 마켓에 완료 처리할 이유가 없다.
		// outbox는 원장 중 유일하게 삭제가 허용된다.
		outPath, err := l.paths.outbox(orderKey)
		if err != nil {
			return err
		}
		if err := tx.Delete(outPath); err != nil {
			// 없으면 그만이다. Firestore delete는 없어도 에러가 아니다.
			_ = err
		}

		return l.writeEntitlement(tx, order.PlatformUserID, ent, now)
	})
}

// ListActive는 활성 entitlement 목록을 돌려준다.
func (l *Ledger) ListActive(ctx context.Context, puid string) ([]string, error) {
	p, err := l.paths.internalEntitlements(puid)
	if err != nil {
		return nil, err
	}

	it, err := l.store.Query(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	defer it.Stop()

	var out []string
	for {
		snap, err := it.Next()
		if store.IsDone(err) {
			break
		}
		if err != nil {
			return nil, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid, "보유 목록을 읽지 못했어요")
		}

		var doc entitlementDoc
		if err := snap.DataTo(&doc); err != nil {
			continue
		}
		if doc.Active {
			id := doc.EntitlementID
			if id == "" {
				id = snap.Ref.ID
			}
			out = append(out, id)
		}
	}

	// 정렬해 응답이 실행마다 바뀌지 않게 한다.
	// 클라이언트가 목록을 비교해 변경을 감지할 수 있어야 한다.
	sort.Strings(out)
	return out, nil
}

// readEntitlement는 내부 원장을 읽는다. 없으면 빈 문서를 만든다.
func (l *Ledger) readEntitlement(
	tx *store.Tx,
	path fspath.Path,
	entID string,
) (entitlementDoc, error) {
	exists, snap, err := tx.Exists(path)
	if err != nil {
		return entitlementDoc{}, err
	}

	doc := entitlementDoc{EntitlementID: entID, Sources: map[string]domain.Source{}}
	if !exists {
		return doc, nil
	}

	if err := snap.DataTo(&doc); err != nil {
		return entitlementDoc{}, platformerr.Wrap(err,
			platformerr.CodeLedgerStateInvalid, "원장을 읽지 못했어요")
	}
	if doc.Sources == nil {
		doc.Sources = map[string]domain.Source{}
	}
	doc.EntitlementID = entID
	return doc, nil
}

// writeEntitlement는 내부 원장과 공개 projection을 함께 쓴다.
//
// 불변식 6. active를 sources에서 재계산해 둘을 같은 트랜잭션에 넣는다.
// 한쪽만 갱신되면 클라이언트가 보는 값과 원장이 어긋난다.
func (l *Ledger) writeEntitlement(
	tx *store.Tx,
	puid string,
	ent entitlementDoc,
	now time.Time,
) error {
	ent.Active = domain.IsActiveFrom(ent.Sources)
	ent.UpdatedAt = now

	intPath, err := l.paths.internalEntitlement(puid, ent.EntitlementID)
	if err != nil {
		return err
	}
	if err := tx.Set(intPath, ent); err != nil {
		return err
	}

	pubPath, err := l.paths.publicEntitlement(puid, ent.EntitlementID)
	if err != nil {
		return err
	}
	return tx.Set(pubPath, projectionDoc{
		EntitlementID: ent.EntitlementID,
		Active:        ent.Active,
		UpdatedAt:     now,
	})
}

func firstNonZero(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}
