package ledger

import (
	"context"
	"math"
	"sort"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/operational"
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

	// TransferSequence는 이 주문의 소유권 이전 횟수다. 이전할 때마다 증가하고
	// 같은 번호의 append-only evidence를 한 transaction에서 만든다.
	TransferSequence int64 `firestore:"transferSequence,omitempty"`

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

// sandboxResetBarrierDoc은 sandbox App Store Grant와 reset이 함께 갱신하는
// 사용자별 coordination 문서다. active intent와 마지막 완료 cutoff를 모두
// 남겨 phase 사이 중단과 완료 뒤 지연 구매를 각각 fail-closed한다.
//
// 요청별 immutable intent/completion은 별도 컬렉션에 있고 이 문서도 삭제하지
// 않는다. 불변식 5와 ADR 0012의 request-start-wins 경계다.
type sandboxResetBarrierDoc struct {
	Revision int64 `firestore:"revision"`

	ActiveRequestID string    `firestore:"activeRequestId,omitempty"`
	ActiveResetAt   time.Time `firestore:"activeResetAt,omitempty"`
	ActiveStartedAt time.Time `firestore:"activeStartedAt,omitempty"`

	LastCompletedRequestID string    `firestore:"lastCompletedRequestId,omitempty"`
	LastCompletedResetAt   time.Time `firestore:"lastCompletedResetAt,omitempty"`

	UpdatedAt time.Time `firestore:"updatedAt"`
}

// ownershipTransferDoc은 BigQuery 감사와 독립적인 최소 복구 증거다.
// 토큰, provider order ID, 마켓 계정 참조는 저장하지 않는다.
type ownershipTransferDoc struct {
	OrderKey           string          `firestore:"orderKey"`
	Sequence           int64           `firestore:"sequence"`
	FromPlatformUserID string          `firestore:"fromPlatformUserId"`
	ToPlatformUserID   string          `firestore:"toPlatformUserId"`
	EntitlementID      string          `firestore:"entitlementId"`
	Platform           domain.Platform `firestore:"platform"`
	State              domain.State    `firestore:"state"`
	ObservedAt         time.Time       `firestore:"observedAt"`
	CreatedAt          time.Time       `firestore:"createdAt"`
}

func newOwnershipTransferDoc(
	orderKey string,
	sequence int64,
	fromPUID, toPUID, entitlementID string,
	purchase domain.VerifiedPurchase,
	createdAt time.Time,
) ownershipTransferDoc {
	return ownershipTransferDoc{
		OrderKey: orderKey, Sequence: sequence,
		FromPlatformUserID: fromPUID, ToPlatformUserID: toPUID,
		EntitlementID: entitlementID, Platform: purchase.Platform, State: purchase.State,
		ObservedAt: purchase.ObservedAt, CreatedAt: createdAt,
	}
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
	store       *store.Client
	paths       pathBuilder
	env         domain.Environment
	appID       string
	operational *operational.Repository
	now         func() time.Time
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

// NewForApp은 신규 다중 앱 원장을 appId 아래에 격리한다. 기존 단일 앱
// 원장의 경로를 바꾸지 않기 위해 New는 그대로 둔다.
func NewForApp(s *store.Client, env domain.Environment, appID string) *Ledger {
	return &Ledger{
		store: s,
		paths: newAppPathBuilder(env, appID),
		env:   env,
		appID: appID,
		now:   time.Now,
	}
}

// WithAppID는 기존 unscoped 원장에도 운영 이벤트의 앱 범위를 명시한다.
func (l *Ledger) WithAppID(appID string) *Ledger {
	l.appID = appID
	return l
}

// WithOperationalEvents는 지급 커밋과 같은 transaction에 운영 이벤트를 쌓는다.
func (l *Ledger) WithOperationalEvents(repo *operational.Repository) *Ledger {
	l.operational = repo
	return l
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
//   - 4: 검증된 마켓 토큰은 단일 소유자로 원자적 이전
//   - 5: 원장 문서 삭제 금지
//   - 6: active = OR(sources)
func (l *Ledger) Grant(ctx context.Context, in GrantInput) (domain.GrantResult, error) {
	return l.grant(ctx, in, "")
}

// grantExpectedOwner는 웹훅이 transaction 밖에서 읽은 소유자를 그대로
// 상태 갱신 대상으로 쓸 때만 사용한다. expectedOwner가 바뀌었으면 이번
// transaction은 상태를 쓰거나 소유권을 이전하지 않고 재조정을 다시 시작한다.
// 웹훅에는 현재 마켓 계정의 구매 증명이 없으므로 불변식 4, 10의 경계다.
func (l *Ledger) grantExpectedOwner(
	ctx context.Context,
	in GrantInput,
	expectedOwner string,
) (domain.GrantResult, error) {
	if expectedOwner == "" || expectedOwner != in.PlatformUserID {
		return domain.GrantResult{}, platformerr.New(platformerr.CodeInternal,
			"웹훅 소유자 정보가 올바르지 않아요")
	}
	return l.grant(ctx, in, expectedOwner)
}

func (l *Ledger) grant(
	ctx context.Context,
	in GrantInput,
	expectedOwner string,
) (domain.GrantResult, error) {
	if in.PlatformUserID == "" || in.EntitlementID == "" {
		return domain.GrantResult{}, platformerr.New(platformerr.CodeInternal, "지급 정보가 올바르지 않아요")
	}
	if !in.Purchase.Platform.Valid() {
		return domain.GrantResult{}, platformerr.New(platformerr.CodePlatformInvalid, "알 수 없는 마켓이에요")
	}

	orderKey := in.Purchase.Key()
	var result domain.GrantResult

	err := l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := l.now().UTC()
		// 트랜잭션은 재시도될 수 있다. 매 시도마다 초기화한다.
		transferredFrom := ""
		var prevOwnerEnt entitlementDoc
		var transferredSource domain.Source
		var prevOwnerPUID string

		orderPath, err := l.paths.order(orderKey)
		if err != nil {
			return err
		}

		exists, snap, err := tx.Exists(orderPath)
		if err != nil {
			return err
		}
		if expectedOwner != "" && !exists {
			return errReconcileOwnerChanged
		}

		var order orderDoc
		if exists {
			if err := snap.DataTo(&order); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid, "원장을 읽지 못했어요")
			}
			if order.TransferSequence < 0 {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"소유권 이전 순번이 올바르지 않아요")
			}
			if expectedOwner != "" &&
				(order.Tombstone || order.PlatformUserID != expectedOwner ||
					order.EntitlementID != in.EntitlementID) {
				return errReconcileOwnerChanged
			}
			// 같은 주문인데 상품이나 마켓이 다르면 위조이거나 버그다.
			if err := checkReplay(order, in); err != nil {
				return err
			}

			// 불변식 4. 검증된 마켓 토큰은 한 사용자에게만 귀속한다.
			// tombstone은 소유자가 없으므로 이전 대상이 없다.
			//
			// 여기 도달했다는 것은 마켓이 이 토큰을 유효한 소유로 확인해
			// 줬다는 뜻이다. Play는 queryPurchases로 현재 로그인 계정이
			// 가진 구매만 돌려주고 Apple은 Apple ID에 묶인 거래만 준다.
			// 그 토큰을 마켓 API로 다시 검증한 뒤에야 여기 온다.
			//
			// 앱을 지우면 익명 uid가 새로 생기므로 소유자가 달라 보이는
			// 것은 정상이다. 그때 거부하면 재설치한 유저는 예외 없이 산
			// 상품을 잃는다. ADR 0010
			//
			// 남는 위험은 남의 purchaseToken을 손에 넣은 경우다. 토큰은
			// 로그에 남기지 않고 TLS로만 오가며 이전은 감사 원장에 남는다.
			// 근본 해결은 계정 연동이다.
			if !order.Tombstone && order.PlatformUserID != "" &&
				order.PlatformUserID != in.PlatformUserID {
				// 이전은 이동이지 복제가 아니다. 한 구매가 두 계정에서
				// 동시에 활성이면 원장이 깨진다.
				//
				// 여기서는 읽기만 한다. Firestore 트랜잭션은 모든 읽기가
				// 쓰기보다 앞서야 해서, 실제 반영은 아래 쓰기 구간에서 한다.
				prevOwnerEnt, transferredSource, prevOwnerPUID, err =
					l.readDetachedPreviousOwner(tx, order, orderKey)
				if err != nil {
					return err
				}
				transferredFrom = order.PlatformUserID
			}
		}

		// sandbox App Store Grant와 reset은 사용자별 barrier를 함께 읽고 쓴다.
		// reset의 컬렉션 query에 없던 주문이 뒤늦게 생겨도 같은 barrier 쓰기가
		// transaction 충돌을 만들며, reset 이후 재시도에서는 cutoff로 차단된다.
		usesSandboxBarrier := l.env == domain.EnvSandbox &&
			in.Purchase.Platform == domain.PlatformAppStore
		var barrier sandboxResetBarrierDoc
		var previousOwnerBarrier sandboxResetBarrierDoc
		if usesSandboxBarrier {
			barrier, err = l.readSandboxResetBarrier(tx, in.PlatformUserID)
			if err != nil {
				return err
			}
			// Cross-PUID 이전은 새 소유자뿐 아니라 order가 가리키는 이전
			// 소유자의 reset과도 직렬화한다. A reset intent 뒤 A에서 B로
			// 빠져나가 reset 대상에서 사라지는 회귀를 막는 경계다.
			if transferredFrom != "" {
				previousOwnerBarrier, err = l.readSandboxResetBarrier(tx, transferredFrom)
				if err != nil {
					return err
				}
			}
		}
		touchSandboxBarriers := func() error {
			if !usesSandboxBarrier {
				return nil
			}
			if err := l.touchSandboxResetBarrier(tx, in.PlatformUserID, barrier, now); err != nil {
				return err
			}
			if transferredFrom != "" {
				return l.touchSandboxResetBarrier(tx, transferredFrom, previousOwnerBarrier, now)
			}
			return nil
		}

		// 초기화 차단은 불변식 3보다 먼저 본다. 주문별 표식뿐 아니라
		// reset 시점에 아직 존재하지 않았던 주문을 사용자 barrier로 막는다.
		sandboxResetAt, err := blockingSandboxResetAt(
			l.env, order, barrier, in.Purchase, previousOwnerBarrier,
		)
		if err != nil {
			return err
		}

		crossUser := exists && !order.Tombstone && order.PlatformUserID != "" &&
			order.PlatformUserID != in.PlatformUserID
		stale := exists && sandboxResetAt.IsZero() &&
			domain.IsStaleUpdate(order.State, in.Purchase.State, order.ObservedAt, in.Purchase.ObservedAt)
		// 재설치 복원에서 같은 active 구매의 provider 관측 시각만 과거일 수 있다.
		// 이때 stale 판정으로 소유권 이동 자체를 생략하면 성공 응답만 주고 새
		// 사용자에게 entitlement가 없다. 소유권만 옮기되 최신 상태/시각은 보존한다.
		preserveLatestOnTransfer := stale && crossUser &&
			order.State == domain.StateActive && in.Purchase.State == domain.StateActive
		if stale && !preserveLatestOnTransfer {
			if err := touchSandboxBarriers(); err != nil {
				return err
			}
			result = domain.GrantResult{
				AlreadyGranted: true,
				EntitlementID:  in.EntitlementID,
			}
			return nil
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
			// 다른 소유자에게 reset 표식이 있는 주문을 새 PUID가 제시한 경우,
			// 소유권도 source도 복제하지 않는다. 과거 버전이 남긴 revoked shadow가
			// 있으면 제거해 이후 reset의 소유자 불일치도 없앤다.
			if crossUser {
				if _, ok := ent.Sources[orderKey]; ok {
					delete(ent.Sources, orderKey)
					if err := l.writeEntitlement(tx, in.PlatformUserID, ent, now); err != nil {
						return err
					}
				}
				if err := touchSandboxBarriers(); err != nil {
					return err
				}
				result = domain.GrantResult{
					EntitlementID:         in.EntitlementID,
					BlockedBySandboxReset: true,
				}
				return nil
			}

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

			mark := effectiveSandboxResetMark(order, barrier, previousOwnerBarrier)
			order.PlatformUserID = in.PlatformUserID
			order.EntitlementID = in.EntitlementID
			order.Platform = in.Purchase.Platform
			order.ProductID = in.Purchase.ProductID
			order.ProviderOrderID = in.Purchase.ProviderOrderID
			order.PlatformAccountIDHash = domain.HashAccountID(in.Purchase.PlatformAccountID)
			order.State = domain.StateRevoked
			order.PurchasedAt = in.Purchase.PurchasedAt
			order.ObservedAt = sandboxResetAt
			order.Tombstone = false
			order.SandboxReset = mark
			order.CreatedAt = firstNonZero(order.CreatedAt, now)
			order.UpdatedAt = now
			if err := tx.Set(orderPath, order); err != nil {
				return err
			}
			if err := touchSandboxBarriers(); err != nil {
				return err
			}

			// 지급도 중복 지급도 아니다. 둘 다 false가 유일하게 맞다.
			result = domain.GrantResult{
				EntitlementID:         in.EntitlementID,
				BlockedBySandboxReset: true,
			}
			return nil
		}

		// 여기서부터 쓰기다. 이전 소유자 회수를 먼저 반영한다.
		if prevOwnerPUID != "" {
			if err := l.writeEntitlement(tx, prevOwnerPUID, prevOwnerEnt, now); err != nil {
				return err
			}
		}

		prevSource, hadSource := ent.Sources[orderKey]
		alreadyActive := hadSource && prevSource.State == domain.StateActive &&
			order.PlatformUserID == in.PlatformUserID && order.State == domain.StateActive

		// stale active 복원은 소유권만 옮긴다. 기존 최신 state/ObservedAt을
		// 과거 값으로 덮으면 그 사이 시각의 revoked가 나중에 잘못 적용된다.
		storedState := in.Purchase.State
		storedPurchasedAt := in.Purchase.PurchasedAt
		storedObservedAt := in.Purchase.ObservedAt
		storedProviderOrderID := in.Purchase.ProviderOrderID
		storedAccountHash := domain.HashAccountID(in.Purchase.PlatformAccountID)
		if preserveLatestOnTransfer {
			storedState = order.State
			storedPurchasedAt = order.PurchasedAt
			storedObservedAt = order.ObservedAt
			storedProviderOrderID = order.ProviderOrderID
			storedAccountHash = order.PlatformAccountIDHash
		}

		transferSequence := order.TransferSequence
		if transferredFrom != "" {
			transferSequence, err = nextLedgerSequence(transferSequence)
			if err != nil {
				return err
			}
		}

		// 여기서부터 쓰기다.
		order = orderDoc{
			PlatformUserID:        in.PlatformUserID,
			EntitlementID:         in.EntitlementID,
			Platform:              in.Purchase.Platform,
			ProductID:             in.Purchase.ProductID,
			ProviderOrderID:       storedProviderOrderID,
			PlatformAccountIDHash: storedAccountHash,
			State:                 storedState,
			PurchasedAt:           storedPurchasedAt,
			ObservedAt:            storedObservedAt,
			Tombstone:             false,
			TransferSequence:      transferSequence,
			CreatedAt:             firstNonZero(order.CreatedAt, now),
			UpdatedAt:             now,
		}
		if err := tx.Set(orderPath, order); err != nil {
			return err
		}

		contentUnitsConsumed := prevSource.ContentUnitsConsumed
		if transferredSource.ContentUnitsConsumed > contentUnitsConsumed {
			contentUnitsConsumed = transferredSource.ContentUnitsConsumed
		}
		ent.Sources[orderKey] = domain.Source{
			Platform:             in.Purchase.Platform,
			ProductID:            in.Purchase.ProductID,
			State:                storedState,
			PurchasedAt:          storedPurchasedAt,
			ObservedAt:           storedObservedAt,
			UpdatedAt:            now,
			ContentUnitsConsumed: contentUnitsConsumed,
		}

		if err := l.writeEntitlement(tx, in.PlatformUserID, ent, now); err != nil {
			return err
		}

		// 불변식 5. BigQuery 감사 projection이 실패해도 이전 소유자를 복구할
		// 수 있도록 최소 증거를 소유권 변경과 같은 transaction에 append한다.
		if transferredFrom != "" {
			evidencePath, err := l.paths.ownershipTransfer(orderKey, transferSequence)
			if err != nil {
				return err
			}
			evidencePurchase := in.Purchase
			evidencePurchase.State = storedState
			evidencePurchase.ObservedAt = storedObservedAt
			if err := tx.Create(evidencePath, newOwnershipTransferDoc(
				orderKey, transferSequence, transferredFrom, in.PlatformUserID,
				in.EntitlementID, evidencePurchase, now,
			)); err != nil {
				return err
			}
		}
		if err := touchSandboxBarriers(); err != nil {
			return err
		}

		// 불변식 2. 둘은 항상 배타적이다.
		result = domain.GrantResult{
			Granted:         !alreadyActive,
			AlreadyGranted:  alreadyActive,
			EntitlementID:   in.EntitlementID,
			TransferredFrom: transferredFrom,
		}
		if result.Granted && l.operational != nil && l.appID != "" {
			if err := l.operational.EnqueueTx(tx, operational.Event{
				EventID: operational.StableEventID(
					"iap", l.appID, orderKey, in.PlatformUserID,
					in.Purchase.ObservedAt.UTC().Format(time.RFC3339Nano),
				),
				OccurredAt: now, Type: "iap.granted", AppID: l.appID, Outcome: "granted",
				Attributes: map[string]any{
					"platform": string(in.Purchase.Platform), "entitlementId": in.EntitlementID,
				},
			}); err != nil {
				return err
			}
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
	barrier sandboxResetBarrierDoc,
	p domain.VerifiedPurchase,
	additionalBarriers ...sandboxResetBarrierDoc,
) (time.Time, error) {
	mark := effectiveSandboxResetMark(order, barrier, additionalBarriers...)
	if env != domain.EnvSandbox ||
		p.Platform != domain.PlatformAppStore ||
		mark == nil || mark.ResetAt.IsZero() {
		return time.Time{}, nil
	}

	// 표식은 있는데 구매 시각을 모르면 판정할 수 없다.
	// 모르는 채로 통과시키면 초기화한 거래를 다시 지급하게 된다.
	if p.PurchasedAt.IsZero() {
		return time.Time{}, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"구매 시각을 확인할 수 없어요")
	}

	// 초기화 시각 이후에 산 것은 진짜 새 구매다. 통과시킨다.
	if p.PurchasedAt.After(mark.ResetAt) {
		return time.Time{}, nil
	}
	return mark.ResetAt, nil
}

func effectiveSandboxResetMark(
	order orderDoc,
	barrier sandboxResetBarrierDoc,
	additionalBarriers ...sandboxResetBarrierDoc,
) *sandboxResetMark {
	var mark *sandboxResetMark
	if order.SandboxReset != nil && !order.SandboxReset.ResetAt.IsZero() {
		copy := *order.SandboxReset
		mark = &copy
	}
	barriers := append([]sandboxResetBarrierDoc{barrier}, additionalBarriers...)
	for _, candidate := range barriers {
		barrierMarks := []sandboxResetMark{
			{RequestID: candidate.LastCompletedRequestID, ResetAt: candidate.LastCompletedResetAt},
			{RequestID: candidate.ActiveRequestID, ResetAt: candidate.ActiveResetAt},
		}
		for _, candidateMark := range barrierMarks {
			if candidateMark.ResetAt.IsZero() {
				continue
			}
			if mark == nil || candidateMark.ResetAt.After(mark.ResetAt) {
				copy := candidateMark
				mark = &copy
			}
		}
	}
	return mark
}

func (l *Ledger) readSandboxResetBarrier(
	tx *store.Tx,
	puid string,
) (sandboxResetBarrierDoc, error) {
	path, err := l.paths.sandboxResetBarrier(puid)
	if err != nil {
		return sandboxResetBarrierDoc{}, err
	}
	exists, snap, err := tx.Exists(path)
	if err != nil || !exists {
		return sandboxResetBarrierDoc{}, err
	}
	var doc sandboxResetBarrierDoc
	if err := snap.DataTo(&doc); err != nil {
		return sandboxResetBarrierDoc{}, platformerr.Wrap(err,
			platformerr.CodeLedgerStateInvalid, "sandbox 초기화 barrier를 읽지 못했어요")
	}
	if !validSandboxResetBarrier(doc) {
		return sandboxResetBarrierDoc{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"sandbox 초기화 barrier가 올바르지 않아요")
	}
	return doc, nil
}

func validSandboxResetBarrier(doc sandboxResetBarrierDoc) bool {
	activeFieldsMatch := (doc.ActiveRequestID == "") == doc.ActiveResetAt.IsZero() &&
		(doc.ActiveRequestID == "") == doc.ActiveStartedAt.IsZero()
	completedFieldsMatch := (doc.LastCompletedRequestID == "") == doc.LastCompletedResetAt.IsZero()
	if doc.Revision <= 0 || doc.UpdatedAt.IsZero() || !activeFieldsMatch ||
		!completedFieldsMatch ||
		(doc.ActiveRequestID != "" && !operatorRequestIDPattern.MatchString(doc.ActiveRequestID)) ||
		(doc.LastCompletedRequestID != "" &&
			!operatorRequestIDPattern.MatchString(doc.LastCompletedRequestID)) ||
		(!doc.ActiveResetAt.IsZero() && doc.ActiveResetAt.Before(doc.LastCompletedResetAt)) {
		return false
	}
	return true
}

func sandboxResetBarrierMatchesPrepared(
	barrier sandboxResetBarrierDoc,
	intent sandboxResetRequestDoc,
) bool {
	return barrier.ActiveRequestID == intent.RequestID &&
		barrier.ActiveResetAt.Equal(intent.ResetAt) &&
		barrier.Revision >= intent.BarrierRevision
}

func sandboxResetBarrierCoversCompletion(
	barrier sandboxResetBarrierDoc,
	completion sandboxResetCompletionDoc,
) bool {
	return barrier.Revision >= completion.BarrierRevision &&
		!barrier.LastCompletedResetAt.Before(completion.ResetAt)
}

func (l *Ledger) touchSandboxResetBarrier(
	tx *store.Tx,
	puid string,
	doc sandboxResetBarrierDoc,
	now time.Time,
) error {
	path, err := l.paths.sandboxResetBarrier(puid)
	if err != nil {
		return err
	}
	doc.Revision, err = nextLedgerSequence(doc.Revision)
	if err != nil {
		return err
	}
	doc.UpdatedAt = now
	return tx.Set(path, doc)
}

func nextLedgerSequence(current int64) (int64, error) {
	if current < 0 || current == math.MaxInt64 {
		return 0, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"원장 순번을 증가시킬 수 없어요")
	}
	return current + 1, nil
}

const (
	// 초기화는 테스터 한 명을 되돌리는 도구다. 수십 건이 잡히면 대상을
	// 잘못 지정한 것이므로 진행하지 않고 멈춘다.
	maxSandboxResetOrders = 20
	// Firestore transaction은 500 document read/write 한도가 있다. entitlement
	// 자체도 상한을 두어 source가 없는 문서가 비정상적으로 늘어도 fail-closed한다.
	maxSandboxResetEntitlements = 200
	sandboxResetSchemaVersion   = 2
	sandboxResetClosureVersion  = 1
)

// SandboxResetInput은 App Store sandbox 초기화의 immutable intent payload다.
// 자유 서술이나 OIDC 이메일 원문은 이 경계에 들어오지 않는다.
type SandboxResetInput struct {
	RequestID      string
	PlatformUserID string
	AppID          string
	ActorLogin     string
	Reason         string
}

// SandboxResetState는 immutable intent의 처리 상태다.
type SandboxResetState string

const (
	SandboxResetPrepared         SandboxResetState = "prepared"
	SandboxResetCompleted        SandboxResetState = "completed"
	SandboxResetClosedNotStarted SandboxResetState = "closed_not_started"
)

// SandboxResetStatus는 관리자 상태 조회와 recovery가 사용하는 내부 결과다.
// HTTP 응답은 PUID와 order key를 노출하지 않고 필요한 필드만 projection한다.
type SandboxResetStatus struct {
	RequestID      string
	PlatformUserID string
	AppID          string
	State          SandboxResetState
	ResetAt        time.Time
	OrderKeys      []string
}

// SandboxResetClosureInput은 intent 부재를 확인한 unknown reset을 영구
// 종결하는 PII-free payload다. PUID와 자유 서술은 closure에 저장하지 않는다.
type SandboxResetClosureInput struct {
	RequestID  string
	AppID      string
	ActorLogin string
}

// sandboxResetClosureDoc은 "이 requestId는 시작되지 않았다"는 immutable
// 종결 기록이다. 이후 늦게 도착한 동일 reset은 영구적으로 거부한다.
type sandboxResetClosureDoc struct {
	SchemaVersion int       `firestore:"schemaVersion"`
	RequestID     string    `firestore:"requestId"`
	AppID         string    `firestore:"appId"`
	ActorLogin    string    `firestore:"actorLogin"`
	ClosedAt      time.Time `firestore:"closedAt"`
}

// sandboxResetRequestDoc은 phase 1에서 barrier와 함께 생성하는 immutable intent다.
type sandboxResetRequestDoc struct {
	SchemaVersion   int       `firestore:"schemaVersion"`
	RequestID       string    `firestore:"requestId"`
	PlatformUserID  string    `firestore:"platformUserId"`
	AppID           string    `firestore:"appId"`
	ActorLogin      string    `firestore:"actorLogin"`
	Reason          string    `firestore:"reason"`
	ResetAt         time.Time `firestore:"resetAt"`
	PreparedAt      time.Time `firestore:"preparedAt"`
	BarrierRevision int64     `firestore:"barrierRevision"`
}

// sandboxResetCompletionDoc은 phase 2 결과다. intent처럼 생성 후 수정하지 않는다.
type sandboxResetCompletionDoc struct {
	SchemaVersion   int       `firestore:"schemaVersion"`
	RequestID       string    `firestore:"requestId"`
	PlatformUserID  string    `firestore:"platformUserId"`
	AppID           string    `firestore:"appId"`
	OrderKeys       []string  `firestore:"orderKeys"`
	ResetAt         time.Time `firestore:"resetAt"`
	CompletedAt     time.Time `firestore:"completedAt"`
	BarrierRevision int64     `firestore:"barrierRevision"`
}

func (in SandboxResetClosureInput) validate() error {
	switch {
	case !operatorRequestIDPattern.MatchString(in.RequestID):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"종결할 초기화 요청 식별자가 올바르지 않아요")
	case !operatorAppIDPattern.MatchString(in.AppID):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"종결할 초기화 앱이 올바르지 않아요")
	case !operatorActorPattern.MatchString(in.ActorLogin):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"초기화를 종결한 운영자 정보가 올바르지 않아요")
	default:
		return nil
	}
}

func validSandboxResetClosure(doc sandboxResetClosureDoc) bool {
	return doc.SchemaVersion == sandboxResetClosureVersion &&
		operatorRequestIDPattern.MatchString(doc.RequestID) &&
		operatorAppIDPattern.MatchString(doc.AppID) &&
		operatorActorPattern.MatchString(doc.ActorLogin) &&
		!doc.ClosedAt.IsZero()
}

func sameSandboxResetClosure(doc sandboxResetClosureDoc, in SandboxResetClosureInput) bool {
	return doc.RequestID == in.RequestID && doc.AppID == in.AppID &&
		doc.ActorLogin == in.ActorLogin
}

func (in SandboxResetInput) validate() error {
	switch {
	case !operatorRequestIDPattern.MatchString(in.RequestID):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"초기화 요청 식별자가 올바르지 않아요")
	case !operatorPUIDPattern.MatchString(in.PlatformUserID):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"초기화 대상 사용자가 올바르지 않아요")
	case !operatorAppIDPattern.MatchString(in.AppID):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"초기화 대상 앱이 올바르지 않아요")
	case !operatorActorPattern.MatchString(in.ActorLogin):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"초기화 실행자가 올바르지 않아요")
	case !ValidAdminMutationReason(in.Reason):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"허용된 운영 사유 코드가 필요해요")
	default:
		return nil
	}
}

func validateSandboxResetRequestID(requestID string) error {
	if !operatorRequestIDPattern.MatchString(requestID) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"초기화 요청 식별자가 올바르지 않아요")
	}
	return nil
}

func sameSandboxResetRequest(doc sandboxResetRequestDoc, in SandboxResetInput) bool {
	return doc.RequestID == in.RequestID &&
		doc.PlatformUserID == in.PlatformUserID &&
		doc.AppID == in.AppID &&
		doc.ActorLogin == in.ActorLogin &&
		doc.Reason == in.Reason
}

func validSandboxResetIntent(doc sandboxResetRequestDoc) bool {
	return doc.SchemaVersion == sandboxResetSchemaVersion &&
		operatorRequestIDPattern.MatchString(doc.RequestID) &&
		operatorPUIDPattern.MatchString(doc.PlatformUserID) &&
		operatorAppIDPattern.MatchString(doc.AppID) &&
		operatorActorPattern.MatchString(doc.ActorLogin) &&
		ValidAdminMutationReason(doc.Reason) &&
		!doc.ResetAt.IsZero() && !doc.PreparedAt.IsZero() &&
		!doc.ResetAt.Before(doc.PreparedAt) && doc.BarrierRevision > 0
}

func validSandboxResetCompletion(
	doc sandboxResetCompletionDoc,
	intent sandboxResetRequestDoc,
) bool {
	if doc.SchemaVersion != sandboxResetSchemaVersion ||
		doc.RequestID != intent.RequestID ||
		doc.PlatformUserID != intent.PlatformUserID ||
		doc.AppID != intent.AppID || !doc.ResetAt.Equal(intent.ResetAt) ||
		doc.CompletedAt.IsZero() || doc.CompletedAt.Before(intent.ResetAt) ||
		doc.BarrierRevision <= intent.BarrierRevision ||
		len(doc.OrderKeys) > maxSandboxResetOrders {
		return false
	}
	previous := ""
	for _, orderKey := range doc.OrderKeys {
		if !operatorOrderKeyPattern.MatchString(orderKey) ||
			(previous != "" && orderKey <= previous) {
			return false
		}
		previous = orderKey
	}
	return true
}

// CloseSandboxResetNotStarted는 상태 조회에서 intent가 없었던 unknown reset을
// 영구 종결한다. status GET과 백오피스 unlock 사이 늦게 도착한 prepare가 같은
// requestId를 다시 시작하지 못하도록 closure와 intent가 서로의 경로를 읽고
// create하는 transaction으로 직렬화한다.
func (l *Ledger) CloseSandboxResetNotStarted(
	ctx context.Context,
	in SandboxResetClosureInput,
) (bool, error) {
	if l.env != domain.EnvSandbox {
		return false, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"sandbox 원장에서만 초기화 부재를 종결할 수 있어요")
	}
	if err := in.validate(); err != nil {
		return false, err
	}
	closurePath, err := l.paths.sandboxResetClosure(in.RequestID)
	if err != nil {
		return false, err
	}
	intentPath, err := l.paths.sandboxResetRequest(in.RequestID)
	if err != nil {
		return false, err
	}
	completionPath, err := l.paths.sandboxResetCompletion(in.RequestID)
	if err != nil {
		return false, err
	}
	grantPath, err := l.paths.operatorGrant(in.RequestID)
	if err != nil {
		return false, err
	}
	revokePath, err := l.paths.operatorRevocation(in.RequestID)
	if err != nil {
		return false, err
	}
	refundDecisionPath, err := l.paths.refundReviewDecision(in.RequestID)
	if err != nil {
		return false, err
	}

	applied := false
	err = l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		applied = false
		closureExists, closureSnap, err := tx.Exists(closurePath)
		if err != nil {
			return err
		}
		intentExists, _, err := tx.Exists(intentPath)
		if err != nil {
			return err
		}
		completionExists, _, err := tx.Exists(completionPath)
		if err != nil {
			return err
		}
		grantExists, _, err := tx.Exists(grantPath)
		if err != nil {
			return err
		}
		revokeExists, _, err := tx.Exists(revokePath)
		if err != nil {
			return err
		}
		refundDecisionExists, _, err := tx.Exists(refundDecisionPath)
		if err != nil {
			return err
		}

		if closureExists && (intentExists || completionExists) {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 종결 기록과 intent 또는 completion이 함께 존재해요")
		}
		if completionExists && !intentExists {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 완료 기록에 intent가 없어요")
		}
		if intentExists {
			return platformerr.New(platformerr.CodeSandboxResetAlreadyStarted,
				"sandbox 초기화가 이미 시작되어 미시작으로 종결할 수 없어요")
		}
		if closureExists {
			if grantExists || revokeExists || refundDecisionExists {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 종결 기록과 다른 운영 조작이 함께 존재해요")
			}
			var closure sandboxResetClosureDoc
			if err := closureSnap.DataTo(&closure); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 종결 기록을 읽지 못했어요")
			}
			if !validSandboxResetClosure(closure) {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 종결 기록이 올바르지 않아요")
			}
			if !sameSandboxResetClosure(closure, in) {
				return platformerr.New(platformerr.CodeOperatorReplayMismatch,
					"같은 requestId의 이전 초기화 종결 요청과 내용이 달라요")
			}
			return nil
		}
		if grantExists || revokeExists || refundDecisionExists {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"requestId가 다른 운영 조작에 이미 사용됐어요")
		}

		closedAt := l.now().UTC()
		if err := tx.Create(closurePath, sandboxResetClosureDoc{
			SchemaVersion: sandboxResetClosureVersion,
			RequestID:     in.RequestID, AppID: in.AppID,
			ActorLogin: in.ActorLogin, ClosedAt: closedAt,
		}); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

// GetSandboxResetStatus는 immutable intent/completion/closure를 대조한다.
// 셋 모두 없을 때만 not found다. prepared는 미적용이 아니라 반드시 같은
// requestId로 재개해야 하고, closed_not_started는 영구 종결 상태다.
func (l *Ledger) GetSandboxResetStatus(
	ctx context.Context,
	requestID string,
) (SandboxResetStatus, error) {
	if l.env != domain.EnvSandbox {
		return SandboxResetStatus{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"sandbox 원장에서만 초기화 상태를 조회할 수 있어요")
	}
	if err := validateSandboxResetRequestID(requestID); err != nil {
		return SandboxResetStatus{}, err
	}
	intentPath, err := l.paths.sandboxResetRequest(requestID)
	if err != nil {
		return SandboxResetStatus{}, err
	}
	completionPath, err := l.paths.sandboxResetCompletion(requestID)
	if err != nil {
		return SandboxResetStatus{}, err
	}
	closurePath, err := l.paths.sandboxResetClosure(requestID)
	if err != nil {
		return SandboxResetStatus{}, err
	}
	var status SandboxResetStatus
	err = l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		status = SandboxResetStatus{}
		intentExists, intentSnap, err := tx.Exists(intentPath)
		if err != nil {
			return err
		}
		completionExists, completionSnap, err := tx.Exists(completionPath)
		if err != nil {
			return err
		}
		closureExists, closureSnap, err := tx.Exists(closurePath)
		if err != nil {
			return err
		}
		if closureExists {
			var closure sandboxResetClosureDoc
			if err := closureSnap.DataTo(&closure); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 종결 기록을 읽지 못했어요")
			}
			if !validSandboxResetClosure(closure) || closure.RequestID != requestID {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 종결 기록이 올바르지 않아요")
			}
			if intentExists || completionExists {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 intent와 종결 기록이 함께 존재해요")
			}
			status = SandboxResetStatus{
				RequestID: closure.RequestID,
				AppID:     closure.AppID,
				State:     SandboxResetClosedNotStarted,
			}
			return nil
		}
		if !intentExists {
			if completionExists {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 완료 기록에 intent가 없어요")
			}
			return platformerr.New(platformerr.CodeSandboxResetNotFound,
				"sandbox 초기화 요청을 찾을 수 없어요")
		}

		var intent sandboxResetRequestDoc
		if err := intentSnap.DataTo(&intent); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 intent를 읽지 못했어요")
		}
		if !validSandboxResetIntent(intent) {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 intent가 올바르지 않아요")
		}
		barrier, err := l.readSandboxResetBarrier(tx, intent.PlatformUserID)
		if err != nil {
			return err
		}
		if barrier.Revision == 0 {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 barrier를 찾을 수 없어요")
		}
		status = SandboxResetStatus{
			RequestID: intent.RequestID, PlatformUserID: intent.PlatformUserID,
			AppID: intent.AppID, State: SandboxResetPrepared, ResetAt: intent.ResetAt,
		}
		if !completionExists {
			if !sandboxResetBarrierMatchesPrepared(barrier, intent) {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 intent와 active barrier가 맞지 않아요")
			}
			return nil
		}
		var completion sandboxResetCompletionDoc
		if err := completionSnap.DataTo(&completion); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 완료 기록을 읽지 못했어요")
		}
		if !validSandboxResetCompletion(completion, intent) {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 완료 기록이 intent와 맞지 않아요")
		}
		if !sandboxResetBarrierCoversCompletion(barrier, completion) {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 완료 기록을 barrier가 보존하지 않아요")
		}
		status.State = SandboxResetCompleted
		status.OrderKeys = append([]string{}, completion.OrderKeys...)
		return nil
	})
	return status, err
}

// FindSandboxResetReplay는 exact retry를 mutable app/user precondition과 rate
// gate보다 먼저 찾는다. prepared intent도 같은 requestId로 phase 2를 재개한다.
func (l *Ledger) FindSandboxResetReplay(
	ctx context.Context,
	in SandboxResetInput,
) ([]string, bool, error) {
	if l.env != domain.EnvSandbox {
		return nil, false, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"sandbox 원장에서만 초기화 결과를 조회할 수 있어요")
	}
	if err := in.validate(); err != nil {
		return nil, false, err
	}
	intentPath, err := l.paths.sandboxResetRequest(in.RequestID)
	if err != nil {
		return nil, false, err
	}
	completionPath, err := l.paths.sandboxResetCompletion(in.RequestID)
	if err != nil {
		return nil, false, err
	}
	closurePath, err := l.paths.sandboxResetClosure(in.RequestID)
	if err != nil {
		return nil, false, err
	}
	grantPath, err := l.paths.operatorGrant(in.RequestID)
	if err != nil {
		return nil, false, err
	}
	revokePath, err := l.paths.operatorRevocation(in.RequestID)
	if err != nil {
		return nil, false, err
	}
	refundDecisionPath, err := l.paths.refundReviewDecision(in.RequestID)
	if err != nil {
		return nil, false, err
	}
	intentSnap, intentExists, err := l.getOptional(ctx, intentPath)
	if err != nil {
		return nil, false, err
	}
	_, completionExists, err := l.getOptional(ctx, completionPath)
	if err != nil {
		return nil, false, err
	}
	closureSnap, closureExists, err := l.getOptional(ctx, closurePath)
	if err != nil {
		return nil, false, err
	}
	_, grantExists, err := l.getOptional(ctx, grantPath)
	if err != nil {
		return nil, false, err
	}
	_, revokeExists, err := l.getOptional(ctx, revokePath)
	if err != nil {
		return nil, false, err
	}
	_, refundDecisionExists, err := l.getOptional(ctx, refundDecisionPath)
	if err != nil {
		return nil, false, err
	}
	if grantExists || revokeExists || refundDecisionExists {
		return nil, false, platformerr.New(platformerr.CodeOperatorReplayMismatch,
			"requestId가 다른 운영 조작에 이미 사용됐어요")
	}
	if closureExists {
		var closure sandboxResetClosureDoc
		if err := closureSnap.DataTo(&closure); err != nil {
			return nil, false, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 종결 기록을 읽지 못했어요")
		}
		if !validSandboxResetClosure(closure) || closure.RequestID != in.RequestID {
			return nil, false, platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 종결 기록이 올바르지 않아요")
		}
		return nil, false, platformerr.New(platformerr.CodeSandboxResetClosed,
			"미시작으로 종결된 sandbox 초기화 requestId예요")
	}
	if !intentExists {
		if completionExists {
			return nil, false, platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 완료 기록에 intent가 없어요")
		}
		return nil, false, nil
	}
	var intent sandboxResetRequestDoc
	if err := intentSnap.DataTo(&intent); err != nil {
		return nil, true, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
			"sandbox 초기화 intent를 읽지 못했어요")
	}
	if !validSandboxResetIntent(intent) {
		return nil, true, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"sandbox 초기화 intent가 올바르지 않아요")
	}
	if !sameSandboxResetRequest(intent, in) {
		return nil, true, platformerr.New(platformerr.CodeOperatorReplayMismatch,
			"같은 requestId의 이전 초기화 요청과 내용이 달라요")
	}
	orderKeys, err := l.applySandboxResetIntent(ctx, in.RequestID)
	if err != nil {
		if platformerr.CodeOf(err) == platformerr.CodeSandboxResetClosed {
			return nil, true, err
		}
		return nil, true, sandboxResetPendingError(err)
	}
	return orderKeys, true, nil
}

// MarkSandboxReset은 request-start-wins 초기화를 두 트랜잭션으로 수행한다.
// phase 1의 intent+barrier commit이 요청 시작의 선형화 지점이고, phase 2가
// 실패해도 intent를 지우지 않고 같은 requestId로 계속 재개한다. ADR 0012.
func (l *Ledger) MarkSandboxReset(
	ctx context.Context,
	in SandboxResetInput,
) ([]string, error) {
	if l.env != domain.EnvSandbox {
		return nil, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"sandbox 원장에서만 초기화할 수 있어요")
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	if _, err := l.prepareSandboxResetIntent(ctx, in); err != nil {
		switch platformerr.CodeOf(err) {
		case platformerr.CodeOperatorReplayMismatch,
			platformerr.CodeSandboxResetBusy,
			platformerr.CodeSandboxResetClosed,
			platformerr.CodeLedgerStateInvalid:
			return nil, err
		default:
			// phase 1의 commit 응답만 유실됐을 수 있다. 새 requestId를 만들지
			// 않도록 unknown을 same-id retry 가능한 pending으로 번역한다.
			return nil, sandboxResetPendingError(err)
		}
	}
	orderKeys, err := l.applySandboxResetIntent(ctx, in.RequestID)
	if err != nil {
		return nil, sandboxResetPendingError(err)
	}
	return orderKeys, nil
}

// ResumeSandboxReset은 장기 prepared intent를 저장된 payload 그대로 재개한다.
func (l *Ledger) ResumeSandboxReset(ctx context.Context, requestID string) ([]string, error) {
	if l.env != domain.EnvSandbox {
		return nil, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"sandbox 원장에서만 초기화를 재개할 수 있어요")
	}
	if err := validateSandboxResetRequestID(requestID); err != nil {
		return nil, err
	}
	orderKeys, err := l.applySandboxResetIntent(ctx, requestID)
	if code := platformerr.CodeOf(err); code == platformerr.CodeSandboxResetNotFound ||
		code == platformerr.CodeSandboxResetClosed {
		return nil, err
	}
	if err != nil {
		return nil, sandboxResetPendingError(err)
	}
	return orderKeys, nil
}

func sandboxResetPendingError(err error) error {
	if platformerr.CodeOf(err) == platformerr.CodeSandboxResetPending {
		return err
	}
	return platformerr.Wrap(err, platformerr.CodeSandboxResetPending,
		"sandbox 초기화 intent가 남아 있어 같은 requestId로 재개해야 해요")
}

func (l *Ledger) prepareSandboxResetIntent(
	ctx context.Context,
	in SandboxResetInput,
) (sandboxResetRequestDoc, error) {
	return l.prepareSandboxResetIntentWithClock(ctx, in, l.now, nil, nil)
}

// prepareSandboxResetIntentWithClock의 clock과 두 hook은 transaction
// retry 순서를 실제 Firestore에서 고정하는 integration test seam이다.
// production은 prepareSandboxResetIntent를 통해 l.now와 nil hook만 쓴다.
func (l *Ledger) prepareSandboxResetIntentWithClock(
	ctx context.Context,
	in SandboxResetInput,
	clock func() time.Time,
	beforeAttempt func(attempt int) error,
	afterCutoff func(attempt int, cutoff time.Time) error,
) (sandboxResetRequestDoc, error) {
	intentPath, err := l.paths.sandboxResetRequest(in.RequestID)
	if err != nil {
		return sandboxResetRequestDoc{}, err
	}
	completionPath, err := l.paths.sandboxResetCompletion(in.RequestID)
	if err != nil {
		return sandboxResetRequestDoc{}, err
	}
	closurePath, err := l.paths.sandboxResetClosure(in.RequestID)
	if err != nil {
		return sandboxResetRequestDoc{}, err
	}
	grantPath, err := l.paths.operatorGrant(in.RequestID)
	if err != nil {
		return sandboxResetRequestDoc{}, err
	}
	revokePath, err := l.paths.operatorRevocation(in.RequestID)
	if err != nil {
		return sandboxResetRequestDoc{}, err
	}
	refundDecisionPath, err := l.paths.refundReviewDecision(in.RequestID)
	if err != nil {
		return sandboxResetRequestDoc{}, err
	}
	barrierPath, err := l.paths.sandboxResetBarrier(in.PlatformUserID)
	if err != nil {
		return sandboxResetRequestDoc{}, err
	}

	var prepared sandboxResetRequestDoc
	attempt := 0
	err = l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		currentAttempt := attempt
		attempt++
		if beforeAttempt != nil {
			if err := beforeAttempt(currentAttempt); err != nil {
				return err
			}
		}
		prepared = sandboxResetRequestDoc{}
		intentExists, intentSnap, err := tx.Exists(intentPath)
		if err != nil {
			return err
		}
		completionExists, completionSnap, err := tx.Exists(completionPath)
		if err != nil {
			return err
		}
		closureExists, closureSnap, err := tx.Exists(closurePath)
		if err != nil {
			return err
		}
		grantExists, _, err := tx.Exists(grantPath)
		if err != nil {
			return err
		}
		revokeExists, _, err := tx.Exists(revokePath)
		if err != nil {
			return err
		}
		refundDecisionExists, _, err := tx.Exists(refundDecisionPath)
		if err != nil {
			return err
		}
		barrier, err := l.readSandboxResetBarrier(tx, in.PlatformUserID)
		if err != nil {
			return err
		}
		if grantExists || revokeExists || refundDecisionExists {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"requestId가 다른 운영 조작에 이미 사용됐어요")
		}
		if closureExists {
			var closure sandboxResetClosureDoc
			if err := closureSnap.DataTo(&closure); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 종결 기록을 읽지 못했어요")
			}
			if !validSandboxResetClosure(closure) || closure.RequestID != in.RequestID {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 종결 기록이 올바르지 않아요")
			}
			return platformerr.New(platformerr.CodeSandboxResetClosed,
				"미시작으로 종결된 sandbox 초기화 requestId예요")
		}
		if !intentExists {
			if completionExists {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 완료 기록에 intent가 없어요")
			}
			if barrier.ActiveRequestID != "" {
				return platformerr.New(platformerr.CodeSandboxResetBusy,
					"같은 사용자의 다른 sandbox 초기화가 진행 중이에요")
			}
			revision, err := nextLedgerSequence(barrier.Revision)
			if err != nil {
				return err
			}
			// ADR 0012: barrier를 읽은 뒤, 그리고 Firestore가 transaction을
			// 재시도할 때마다 cutoff를 다시 잡는다. 이 read 뒤 App Store Grant가
			// 먼저 commit하면 barrier 충돌로 재시도되고 새 cutoff가 그 Grant를
			// 포함해야 "prepare commit = 요청 시작" 선형화가 성립한다.
			requestedAt := clock().UTC()
			if afterCutoff != nil {
				if err := afterCutoff(currentAttempt, requestedAt); err != nil {
					return err
				}
			}
			resetAt := requestedAt
			if barrier.LastCompletedResetAt.After(resetAt) {
				resetAt = barrier.LastCompletedResetAt
			}
			prepared = sandboxResetRequestDoc{
				SchemaVersion: sandboxResetSchemaVersion, RequestID: in.RequestID,
				PlatformUserID: in.PlatformUserID, AppID: in.AppID,
				ActorLogin: in.ActorLogin, Reason: in.Reason, ResetAt: resetAt,
				PreparedAt: requestedAt, BarrierRevision: revision,
			}
			barrier.Revision = revision
			barrier.ActiveRequestID = in.RequestID
			barrier.ActiveResetAt = resetAt
			barrier.ActiveStartedAt = requestedAt
			barrier.UpdatedAt = resetAt
			if err := tx.Create(intentPath, prepared); err != nil {
				return err
			}
			return tx.Set(barrierPath, barrier)
		}

		if err := intentSnap.DataTo(&prepared); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 intent를 읽지 못했어요")
		}
		if !validSandboxResetIntent(prepared) {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 intent가 올바르지 않아요")
		}
		if !sameSandboxResetRequest(prepared, in) {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"같은 requestId의 이전 초기화 요청과 내용이 달라요")
		}
		if completionExists {
			var completion sandboxResetCompletionDoc
			if err := completionSnap.DataTo(&completion); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 완료 기록을 읽지 못했어요")
			}
			if !validSandboxResetCompletion(completion, prepared) {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 완료 기록이 intent와 맞지 않아요")
			}
			if !sandboxResetBarrierCoversCompletion(barrier, completion) {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 완료 기록을 barrier가 보존하지 않아요")
			}
			return nil
		}
		if !sandboxResetBarrierMatchesPrepared(barrier, prepared) {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 intent와 active barrier가 맞지 않아요")
		}
		return nil
	})
	return prepared, err
}

func (l *Ledger) applySandboxResetIntent(
	ctx context.Context,
	requestID string,
) ([]string, error) {
	intentPath, err := l.paths.sandboxResetRequest(requestID)
	if err != nil {
		return nil, err
	}
	completionPath, err := l.paths.sandboxResetCompletion(requestID)
	if err != nil {
		return nil, err
	}
	closurePath, err := l.paths.sandboxResetClosure(requestID)
	if err != nil {
		return nil, err
	}

	result := []string{}
	err = l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		result = []string{}
		intentExists, intentSnap, err := tx.Exists(intentPath)
		if err != nil {
			return err
		}
		completionExists, completionSnap, err := tx.Exists(completionPath)
		if err != nil {
			return err
		}
		closureExists, closureSnap, err := tx.Exists(closurePath)
		if err != nil {
			return err
		}
		if closureExists {
			var closure sandboxResetClosureDoc
			if err := closureSnap.DataTo(&closure); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 종결 기록을 읽지 못했어요")
			}
			if !validSandboxResetClosure(closure) || closure.RequestID != requestID {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 종결 기록이 올바르지 않아요")
			}
			return platformerr.New(platformerr.CodeSandboxResetClosed,
				"미시작으로 종결된 sandbox 초기화 requestId예요")
		}
		if !intentExists {
			if completionExists {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 완료 기록에 intent가 없어요")
			}
			return platformerr.New(platformerr.CodeSandboxResetNotFound,
				"sandbox 초기화 요청을 찾을 수 없어요")
		}
		var intent sandboxResetRequestDoc
		if err := intentSnap.DataTo(&intent); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 intent를 읽지 못했어요")
		}
		if !validSandboxResetIntent(intent) {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 intent가 올바르지 않아요")
		}
		barrierPath, err := l.paths.sandboxResetBarrier(intent.PlatformUserID)
		if err != nil {
			return err
		}
		barrier, err := l.readSandboxResetBarrier(tx, intent.PlatformUserID)
		if err != nil {
			return err
		}
		if completionExists {
			var completion sandboxResetCompletionDoc
			if err := completionSnap.DataTo(&completion); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 완료 기록을 읽지 못했어요")
			}
			if !validSandboxResetCompletion(completion, intent) {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 완료 기록이 intent와 맞지 않아요")
			}
			if !sandboxResetBarrierCoversCompletion(barrier, completion) {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"sandbox 초기화 완료 기록을 barrier가 보존하지 않아요")
			}
			result = append([]string{}, completion.OrderKeys...)
			return nil
		}
		if !sandboxResetBarrierMatchesPrepared(barrier, intent) {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 intent와 active barrier가 맞지 않아요")
		}

		entitlementsPath, err := l.paths.internalEntitlements(intent.PlatformUserID)
		if err != nil {
			return err
		}
		iter, err := tx.Query(entitlementsPath, func(q firestore.Query) firestore.Query {
			return q.Limit(maxSandboxResetEntitlements + 1)
		})
		if err != nil {
			return err
		}
		defer iter.Stop()

		entitlementsByID := make(map[string]entitlementDoc)
		sourceEntitlement := make(map[string]string)
		for {
			snap, err := iter.Next()
			if store.IsDone(err) {
				break
			}
			if err != nil {
				return err
			}
			if len(entitlementsByID) >= maxSandboxResetEntitlements {
				return platformerr.New(platformerr.CodeRequestInvalid,
					"초기화 대상 entitlement가 너무 많아요")
			}
			var ent entitlementDoc
			if err := snap.DataTo(&ent); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"초기화할 entitlement 원장을 읽지 못했어요")
			}
			entitlementID := snap.Ref.ID
			if ent.EntitlementID != "" && ent.EntitlementID != entitlementID {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"초기화할 entitlement 식별자가 올바르지 않아요")
			}
			ent.EntitlementID = entitlementID
			if ent.Sources == nil {
				ent.Sources = map[string]domain.Source{}
			}
			entitlementsByID[entitlementID] = ent
			for orderKey, src := range ent.Sources {
				if src.Platform != domain.PlatformAppStore {
					continue
				}
				if !operatorOrderKeyPattern.MatchString(orderKey) {
					return platformerr.New(platformerr.CodeLedgerStateInvalid,
						"초기화할 App Store source 식별자가 올바르지 않아요")
				}
				if _, duplicate := sourceEntitlement[orderKey]; duplicate {
					return platformerr.New(platformerr.CodeLedgerStateInvalid,
						"같은 주문 source가 여러 entitlement에 있어요")
				}
				sourceEntitlement[orderKey] = entitlementID
			}
		}
		if len(sourceEntitlement) > maxSandboxResetOrders {
			return platformerr.New(platformerr.CodeRequestInvalid,
				"초기화 대상이 너무 많아요")
		}

		allSourceKeys := make([]string, 0, len(sourceEntitlement))
		for orderKey := range sourceEntitlement {
			allSourceKeys = append(allSourceKeys, orderKey)
		}
		sort.Strings(allSourceKeys)

		// 모든 주문을 읽고 소유권을 판정한 뒤에만 쓰기 시작한다.
		orders := make(map[string]orderDoc, len(allSourceKeys))
		orderKeys := make([]string, 0, len(allSourceKeys))
		shadowKeys := make([]string, 0)
		for _, orderKey := range allSourceKeys {
			orderPath, err := l.paths.order(orderKey)
			if err != nil {
				return err
			}
			exists, snap, err := tx.Exists(orderPath)
			if err != nil {
				return err
			}
			if !exists {
				return platformerr.New(platformerr.CodePurchaseNotFound,
					"초기화할 주문을 찾을 수 없어요")
			}
			var order orderDoc
			if err := snap.DataTo(&order); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"초기화할 주문을 읽지 못했어요")
			}
			if order.Platform != domain.PlatformAppStore {
				return platformerr.New(platformerr.CodePlatformInvalid,
					"App Store 주문만 초기화할 수 있어요")
			}
			entitlementID := sourceEntitlement[orderKey]
			ent := entitlementsByID[entitlementID]
			src := ent.Sources[orderKey]
			if order.PlatformUserID == intent.PlatformUserID {
				if order.EntitlementID != entitlementID {
					return platformerr.New(platformerr.CodeLedgerStateInvalid,
						"초기화할 주문과 entitlement가 맞지 않아요")
				}
				// phase 1 cutoff 뒤 산 실제 신규 구매는 유지한다.
				if order.PurchasedAt.After(intent.ResetAt) {
					continue
				}
				orders[orderKey] = order
				orderKeys = append(orderKeys, orderKey)
				continue
			}

			// 과거 버전의 cross-PUID revoked shadow만 보수적으로 정리한다.
			if src.State == domain.StateRevoked && order.State == domain.StateRevoked &&
				order.SandboxReset != nil && !order.SandboxReset.ResetAt.IsZero() {
				shadowKeys = append(shadowKeys, orderKey)
				continue
			}
			return platformerr.New(platformerr.CodePurchaseOwnedByAnotherUser,
				"다른 계정의 주문이에요")
		}

		dirtyEntitlements := make(map[string]bool)
		for _, orderKey := range orderKeys {
			order := orders[orderKey]
			entitlementID := sourceEntitlement[orderKey]
			ent := entitlementsByID[entitlementID]
			src := ent.Sources[orderKey]
			src.ProductID = order.ProductID
			src.State = domain.StateRevoked
			src.PurchasedAt = order.PurchasedAt
			src.ObservedAt = intent.ResetAt
			src.UpdatedAt = intent.ResetAt
			ent.Sources[orderKey] = src
			entitlementsByID[entitlementID] = ent
			dirtyEntitlements[entitlementID] = true

			order.State = domain.StateRevoked
			order.ObservedAt = intent.ResetAt
			order.UpdatedAt = intent.ResetAt
			order.SandboxReset = &sandboxResetMark{RequestID: intent.RequestID, ResetAt: intent.ResetAt}
			orders[orderKey] = order
		}
		for _, orderKey := range shadowKeys {
			entitlementID := sourceEntitlement[orderKey]
			ent := entitlementsByID[entitlementID]
			delete(ent.Sources, orderKey)
			entitlementsByID[entitlementID] = ent
			dirtyEntitlements[entitlementID] = true
		}

		sortedEntitlementIDs := make([]string, 0, len(dirtyEntitlements))
		for entitlementID := range dirtyEntitlements {
			sortedEntitlementIDs = append(sortedEntitlementIDs, entitlementID)
		}
		sort.Strings(sortedEntitlementIDs)
		for _, entitlementID := range sortedEntitlementIDs {
			if err := l.writeEntitlement(tx, intent.PlatformUserID,
				entitlementsByID[entitlementID], intent.ResetAt); err != nil {
				return err
			}
		}
		for _, orderKey := range orderKeys {
			orderPath, err := l.paths.order(orderKey)
			if err != nil {
				return err
			}
			if err := tx.Set(orderPath, orders[orderKey]); err != nil {
				return err
			}
		}

		completionRevision, err := nextLedgerSequence(barrier.Revision)
		if err != nil {
			return err
		}
		completedAt := l.now().UTC()
		if completedAt.Before(intent.ResetAt) {
			completedAt = intent.ResetAt
		}
		barrier.Revision = completionRevision
		barrier.ActiveRequestID = ""
		barrier.ActiveResetAt = time.Time{}
		barrier.ActiveStartedAt = time.Time{}
		barrier.LastCompletedRequestID = intent.RequestID
		barrier.LastCompletedResetAt = intent.ResetAt
		barrier.UpdatedAt = completedAt
		if barrier.UpdatedAt.Before(intent.ResetAt) {
			barrier.UpdatedAt = intent.ResetAt
		}
		if err := tx.Set(barrierPath, barrier); err != nil {
			return err
		}
		if err := tx.Create(completionPath, sandboxResetCompletionDoc{
			SchemaVersion: sandboxResetSchemaVersion, RequestID: intent.RequestID,
			PlatformUserID: intent.PlatformUserID, AppID: intent.AppID,
			OrderKeys: append([]string{}, orderKeys...), ResetAt: intent.ResetAt,
			CompletedAt: completedAt, BarrierRevision: completionRevision,
		}); err != nil {
			return err
		}
		result = append([]string{}, orderKeys...)
		return nil
	})
	return result, err
}

// readDetachedPreviousOwner는 이전 소유자의 원장을 읽고 이 구매의 근거를
// 뺀 상태를 만들어 돌려준다. 쓰지는 않는다.
//
// Firestore 트랜잭션은 모든 읽기가 쓰기보다 앞서야 한다. 여기서 바로
// 쓰면 뒤따르는 새 소유자 읽기가 read-after-write로 거부된다.
//
// 소유자만 바꾸고 이전 원장을 그대로 두면 한 구매가 두 계정에서 활성이
// 된다. 불변식 6이 sources의 OR로 active를 재계산하므로, 근거를 빼고
// projection을 다시 써야 이전 계정이 실제로 잃는다.
//
// 불변식 5에 따라 문서를 지우지는 않는다. source만 뺀다.
func (l *Ledger) readDetachedPreviousOwner(
	tx *store.Tx,
	order orderDoc,
	orderKey string,
) (entitlementDoc, domain.Source, string, error) {
	if order.PlatformUserID == "" || order.EntitlementID == "" {
		return entitlementDoc{}, domain.Source{}, "", nil
	}

	intPath, err := l.paths.internalEntitlement(order.PlatformUserID, order.EntitlementID)
	if err != nil {
		return entitlementDoc{}, domain.Source{}, "", err
	}
	prev, err := l.readEntitlement(tx, intPath, order.EntitlementID)
	if err != nil {
		return entitlementDoc{}, domain.Source{}, "", err
	}
	source, ok := prev.Sources[orderKey]
	if !ok {
		// 이전 소유자에게 이 근거가 없다. 쓸 것도 없다.
		return entitlementDoc{}, domain.Source{}, "", nil
	}
	delete(prev.Sources, orderKey)

	return prev, source, order.PlatformUserID, nil
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
