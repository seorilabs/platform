package ledger

import (
	"context"
	"errors"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

// 운영자 지급과 회수.
//
// CS 보상과 오지급 정정에 쓴다. 마켓을 거치지 않으므로 근거가 코드가
// 아니라 사람의 판단이다. 그래서 누가 왜 했는지를 영구히 남긴다.
//
// 감사 기록은 지우지 않는다. 불변식 5다.

// operatorDoc은 운영자 조작 감사 원장이다.
type operatorDoc struct {
	RequestID      string `firestore:"requestId"`
	PlatformUserID string `firestore:"platformUserId"`
	EntitlementID  string `firestore:"entitlementId"`

	// ActorLogin은 조작한 운영자다. 백오피스가 넘긴다.
	ActorLogin string `firestore:"actorLogin"`
	// Reason은 왜 했는지다. 비어 있으면 받지 않는다.
	Reason string `firestore:"reason"`

	AppID     string    `firestore:"appId"`
	CreatedAt time.Time `firestore:"createdAt"`
}

// OperatorInput은 운영자 조작 요청이다.
type OperatorInput struct {
	// RequestID는 멱등 키다. 백오피스가 UUID로 만든다.
	//
	// 같은 요청이 두 번 오면 두 번째는 아무것도 하지 않는다.
	// 네트워크가 끊겨 재시도할 때 보상이 두 번 나가는 것을 막는다.
	RequestID      string
	PlatformUserID string
	EntitlementID  string
	ActorLogin     string
	Reason         string
	AppID          string
}

// OperatorResult는 조작 결과다.
type OperatorResult struct {
	// Applied가 false면 이미 처리된 요청이었다.
	Applied bool
	// Entitlements는 조작 후 활성 목록이다.
	Entitlements []string
}

func (in OperatorInput) validate() error {
	switch {
	case in.RequestID == "":
		return platformerr.New(platformerr.CodeRequestInvalid,
			"요청 식별자가 필요해요")
	case in.PlatformUserID == "":
		return platformerr.New(platformerr.CodeRequestInvalid,
			"대상 사용자가 필요해요")
	case in.EntitlementID == "":
		return platformerr.New(platformerr.CodeRequestInvalid,
			"대상 상품이 필요해요")
	case in.ActorLogin == "":
		return platformerr.New(platformerr.CodeRequestInvalid,
			"조작한 운영자 정보가 필요해요")
	case in.Reason == "":
		// 이유 없는 지급은 나중에 아무도 설명할 수 없다.
		return platformerr.New(platformerr.CodeRequestInvalid,
			"지급 사유가 필요해요")
	}
	return nil
}

// OperatorGrant는 운영자가 entitlement를 지급한다.
//
// 마켓 구매와 같은 원장에 source로 들어간다. platform이 operator다.
// 나중에 실제 구매가 들어와도 서로를 덮어쓰지 않고 OR로 합쳐진다.
func (l *Ledger) OperatorGrant(ctx context.Context, in OperatorInput) (OperatorResult, error) {
	return l.operatorApply(ctx, in, operatorGrants, domain.StateActive)
}

// OperatorRevoke는 운영자가 entitlement를 회수한다.
//
// 오지급 정정에 쓴다. 마켓 구매로 받은 것을 회수하면 사용자가
// 돈을 내고 산 물건을 잃으므로, 백오피스가 근거를 확인해야 한다.
func (l *Ledger) OperatorRevoke(ctx context.Context, in OperatorInput) (OperatorResult, error) {
	return l.operatorApply(ctx, in, operatorRevocations, domain.StateRevoked)
}

func (l *Ledger) operatorApply(
	ctx context.Context,
	in OperatorInput,
	collection string,
	state domain.State,
) (OperatorResult, error) {
	if err := in.validate(); err != nil {
		return OperatorResult{}, err
	}

	recorded, err := l.recordOperatorRequest(ctx, in, collection)
	if err != nil {
		return OperatorResult{}, err
	}

	if !recorded {
		// 이미 처리한 요청이다. 다시 지급하지 않는다.
		list, err := l.ListActive(ctx, in.PlatformUserID)
		if err != nil {
			return OperatorResult{}, err
		}
		return OperatorResult{Applied: false, Entitlements: orEmpty(list)}, nil
	}

	now := l.now().UTC()

	// canonicalId를 requestId로 삼는다.
	// 같은 요청이 만드는 orderKey가 항상 같아서 원장에서도 멱등이다.
	purchase := domain.VerifiedPurchase{
		Platform:        domain.PlatformOperator,
		ProductID:       in.EntitlementID,
		CanonicalID:     in.RequestID,
		ProviderOrderID: in.RequestID,
		PurchasedAt:     now,
		ObservedAt:      now,
		State:           state,
		Completion:      domain.CompletionNone,
	}

	res, err := l.Grant(ctx, GrantInput{
		PlatformUserID: in.PlatformUserID,
		EntitlementID:  in.EntitlementID,
		Purchase:       purchase,
	})
	if err != nil {
		return OperatorResult{}, err
	}

	return OperatorResult{Applied: true, Entitlements: orEmpty(res.Entitlements)}, nil
}

// recordOperatorRequest는 감사 원장에 기록한다.
//
// 이미 있으면 false를 준다. 멱등의 근거가 여기다.
func (l *Ledger) recordOperatorRequest(
	ctx context.Context,
	in OperatorInput,
	collection string,
) (bool, error) {
	created := false

	err := l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		created = false

		path, err := l.paths.operatorRecord(collection, in.RequestID)
		if err != nil {
			return err
		}

		exists, _, err := tx.Exists(path)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}

		created = true
		return tx.Create(path, operatorDoc{
			RequestID:      in.RequestID,
			PlatformUserID: in.PlatformUserID,
			EntitlementID:  in.EntitlementID,
			ActorLogin:     in.ActorLogin,
			Reason:         in.Reason,
			AppID:          in.AppID,
			CreatedAt:      l.now().UTC(),
		})
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

// OperatorRecord는 조회용 감사 기록이다.
type OperatorRecord struct {
	RequestID      string    `json:"requestId"`
	PlatformUserID string    `json:"platformUserId"`
	EntitlementID  string    `json:"entitlementId"`
	ActorLogin     string    `json:"actorLogin"`
	Reason         string    `json:"reason"`
	AppID          string    `json:"appId"`
	CreatedAt      time.Time `json:"createdAt"`
	Kind           string    `json:"kind"`
}

// ListOperatorGrants는 최근 운영자 지급을 읽는다.
func (l *Ledger) ListOperatorGrants(ctx context.Context, limit int) ([]OperatorRecord, error) {
	return l.listOperatorRecords(ctx, operatorGrants, "grant", limit)
}

// ListOperatorRevocations는 최근 운영자 회수를 읽는다.
func (l *Ledger) ListOperatorRevocations(ctx context.Context, limit int) ([]OperatorRecord, error) {
	return l.listOperatorRecords(ctx, operatorRevocations, "revoke", limit)
}

func (l *Ledger) listOperatorRecords(
	ctx context.Context,
	collection, kind string,
	limit int,
) ([]OperatorRecord, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}

	col, err := l.paths.operatorCollection(collection)
	if err != nil {
		return nil, err
	}

	iter, err := l.store.Query(ctx, col, func(q firestore.Query) firestore.Query {
		return q.OrderBy("createdAt", firestore.Desc).Limit(limit)
	})
	if err != nil {
		return nil, err
	}
	defer iter.Stop()

	out := make([]OperatorRecord, 0, limit)
	for {
		snap, err := iter.Next()
		if store.IsDone(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}

		var doc operatorDoc
		if err := snap.DataTo(&doc); err != nil {
			return nil, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"운영 기록을 읽지 못했어요")
		}

		out = append(out, OperatorRecord{
			RequestID:      doc.RequestID,
			PlatformUserID: doc.PlatformUserID,
			EntitlementID:  doc.EntitlementID,
			ActorLogin:     doc.ActorLogin,
			Reason:         doc.Reason,
			AppID:          doc.AppID,
			CreatedAt:      doc.CreatedAt,
			Kind:           kind,
		})
	}
}

// 조회 상한. 백오피스가 실수로 전체를 긁어가지 못하게 한다.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// orderSummary는 운영 화면에 보여줄 주문 요약이다.
//
// canonicalId와 마켓 계정 해시는 넣지 않는다. 운영자가 볼 이유가 없고
// 화면에 뜨면 스크린샷과 로그로 퍼진다.
type OrderSummary struct {
	OrderKey        string    `json:"orderKey"`
	PlatformUserID  string    `json:"platformUserId"`
	EntitlementID   string    `json:"entitlementId"`
	Platform        string    `json:"platform"`
	ProductID       string    `json:"productId"`
	ProviderOrderID string    `json:"providerOrderId"`
	State           string    `json:"state"`
	PurchasedAt     time.Time `json:"purchasedAt"`
	ObservedAt      time.Time `json:"observedAt"`
	Tombstone       bool      `json:"tombstone"`
}

// ListRecentOrders는 최근 주문을 읽는다.
//
// (observedAt) 단일 필드 인덱스로 동작한다.
func (l *Ledger) ListRecentOrders(ctx context.Context, limit int) ([]OrderSummary, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}

	col, err := l.paths.orders()
	if err != nil {
		return nil, err
	}

	iter, err := l.store.Query(ctx, col, func(q firestore.Query) firestore.Query {
		return q.OrderBy("observedAt", firestore.Desc).Limit(limit)
	})
	if err != nil {
		return nil, err
	}
	defer iter.Stop()

	out := make([]OrderSummary, 0, limit)
	for {
		snap, err := iter.Next()
		if store.IsDone(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}

		var doc orderDoc
		if err := snap.DataTo(&doc); err != nil {
			return nil, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"주문 원장을 읽지 못했어요")
		}

		out = append(out, OrderSummary{
			OrderKey:        snap.Ref.ID,
			PlatformUserID:  doc.PlatformUserID,
			EntitlementID:   doc.EntitlementID,
			Platform:        string(doc.Platform),
			ProductID:       doc.ProductID,
			ProviderOrderID: doc.ProviderOrderID,
			State:           string(doc.State),
			PurchasedAt:     doc.PurchasedAt,
			ObservedAt:      doc.ObservedAt,
			Tombstone:       doc.Tombstone,
		})
	}
}

// UserEntitlement는 사용자별 entitlement 상태다.
type UserEntitlement struct {
	EntitlementID string    `json:"entitlementId"`
	Active        bool      `json:"active"`
	UpdatedAt     time.Time `json:"updatedAt"`
	// Sources는 어느 경로로 받았는지다. 운영자가 원인을 추적할 때 쓴다.
	Sources []EntitlementSource `json:"sources"`
}

// EntitlementSource는 지급 근거 하나다.
type EntitlementSource struct {
	Platform  string    `json:"platform"`
	ProductID string    `json:"productId"`
	State     string    `json:"state"`
	OrderKey  string    `json:"orderKey"`
	Observed  time.Time `json:"observedAt"`
}

// ListUserEntitlements는 사용자의 entitlement를 전부 읽는다.
//
// 활성 여부와 무관하게 준다. 왜 없는지를 봐야 CS가 가능하다.
func (l *Ledger) ListUserEntitlements(ctx context.Context, puid string) ([]UserEntitlement, error) {
	if puid == "" {
		return nil, platformerr.New(platformerr.CodeRequestInvalid,
			"사용자 식별자가 필요해요")
	}

	col, err := l.paths.internalEntitlements(puid)
	if err != nil {
		return nil, err
	}

	iter, err := l.store.Query(ctx, col, nil)
	if err != nil {
		return nil, err
	}
	defer iter.Stop()

	out := make([]UserEntitlement, 0, 8)
	for {
		snap, err := iter.Next()
		if store.IsDone(err) {
			return out, nil
		}
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return out, nil
			}
			return nil, err
		}

		var doc entitlementDoc
		if err := snap.DataTo(&doc); err != nil {
			return nil, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"entitlement 원장을 읽지 못했어요")
		}

		sources := make([]EntitlementSource, 0, len(doc.Sources))
		for orderKey, src := range doc.Sources {
			sources = append(sources, EntitlementSource{
				Platform:  string(src.Platform),
				ProductID: src.ProductID,
				State:     string(src.State),
				OrderKey:  orderKey,
				Observed:  src.ObservedAt,
			})
		}

		out = append(out, UserEntitlement{
			EntitlementID: doc.EntitlementID,
			Active:        doc.Active,
			UpdatedAt:     doc.UpdatedAt,
			Sources:       sources,
		})
	}
}

// orEmpty는 nil 슬라이스를 빈 슬라이스로 만든다.
//
// JSON에서 null이 아니라 []로 나가야 백오피스가 length를 바로 쓴다.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
