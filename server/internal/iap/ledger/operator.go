package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

var (
	operatorRequestIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	operatorActorPattern       = regexp.MustCompile(`^(?:[A-Za-z0-9-]{1,39}|oidc_sha256:[0-9a-f]{64})$`)
	operatorPUIDPattern        = regexp.MustCompile(`^pu_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	operatorAppIDPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	operatorEntitlementPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	operatorOrderKeyPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const (
	AdminReasonCustomerSupportCompensation = "customer_support_compensation"
	AdminReasonIncorrectGrantCorrection    = "incorrect_grant_correction"
	AdminReasonIncidentRecovery            = "incident_recovery"
	AdminReasonInternalValidation          = "internal_validation"
)

// ValidAdminMutationReason은 영구 감사 원장에 저장 가능한 고정 사유 코드만
// 허용한다. 자유 서술은 PII나 영수증·토큰이 섞일 수 있어 받지 않는다.
func ValidAdminMutationReason(reason string) bool {
	switch reason {
	case AdminReasonCustomerSupportCompensation,
		AdminReasonIncorrectGrantCorrection,
		AdminReasonIncidentRecovery,
		AdminReasonInternalValidation:
		return true
	default:
		return false
	}
}

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
	// GrantRequestID는 회수 대상 operator 지급의 requestId다.
	// 지급 레코드에서는 비어 있다.
	GrantRequestID string `firestore:"grantRequestId,omitempty"`

	// ActorLogin은 조작한 운영자다. 백오피스가 넘긴다.
	ActorLogin string `firestore:"actorLogin"`
	// Reason은 왜 했는지 분류하는 고정 코드다. 자유 서술은 받지 않는다.
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
	// GrantRequestID는 회수할 operator 지급의 requestId다.
	// 지급 요청에서는 비어 있어야 한다.
	GrantRequestID string
}

// OperatorResult는 조작 결과다.
type OperatorResult struct {
	// Applied가 false면 이미 처리된 요청이었다.
	Applied bool
	// Entitlements는 조작 후 활성 목록이다.
	Entitlements []string
}

// FindOperatorReplay는 신규 조작의 mutable precondition과 rate gate를 타기
// 전에 영구 감사 원장에서 exact retry를 찾는다. commit 뒤 응답만 유실된
// 요청은 앱 pause나 사용자 binding 변경과 무관하게 같은 결과를 읽을 수 있어야
// 한다. 신규 요청과의 경합은 실제 OperatorGrant/OperatorRevoke 트랜잭션이 다시
// 검증하므로 이 읽기는 mutation 권한을 부여하지 않는다.
func (l *Ledger) FindOperatorReplay(
	ctx context.Context,
	in OperatorInput,
	revoke bool,
) (OperatorResult, bool, error) {
	if err := in.validate(); err != nil {
		return OperatorResult{}, false, err
	}
	if revoke {
		if !operatorRequestIDPattern.MatchString(in.GrantRequestID) {
			return OperatorResult{}, false, platformerr.New(platformerr.CodeRequestInvalid,
				"회수할 지급 requestId가 필요해요")
		}
	} else if in.GrantRequestID != "" {
		return OperatorResult{}, false, platformerr.New(platformerr.CodeRequestInvalid,
			"지급 요청에는 grantRequestId를 넣을 수 없어요")
	}

	var recordPath, oppositePath fspath.Path
	var err error
	if revoke {
		recordPath, err = l.paths.operatorRevocation(in.RequestID)
		if err == nil {
			oppositePath, err = l.paths.operatorGrant(in.RequestID)
		}
	} else {
		recordPath, err = l.paths.operatorGrant(in.RequestID)
		if err == nil {
			oppositePath, err = l.paths.operatorRevocation(in.RequestID)
		}
	}
	if err != nil {
		return OperatorResult{}, false, err
	}
	resetPath, err := l.paths.sandboxResetRequest(in.RequestID)
	if err != nil {
		return OperatorResult{}, false, err
	}
	resetCompletionPath, err := l.paths.sandboxResetCompletion(in.RequestID)
	if err != nil {
		return OperatorResult{}, false, err
	}
	resetClosurePath, err := l.paths.sandboxResetClosure(in.RequestID)
	if err != nil {
		return OperatorResult{}, false, err
	}

	recordSnap, recordExists, err := l.getOptional(ctx, recordPath)
	if err != nil {
		return OperatorResult{}, false, err
	}
	_, oppositeExists, err := l.getOptional(ctx, oppositePath)
	if err != nil {
		return OperatorResult{}, false, err
	}
	_, resetExists, err := l.getOptional(ctx, resetPath)
	if err != nil {
		return OperatorResult{}, false, err
	}
	_, resetCompletionExists, err := l.getOptional(ctx, resetCompletionPath)
	if err != nil {
		return OperatorResult{}, false, err
	}
	_, resetClosureExists, err := l.getOptional(ctx, resetClosurePath)
	if err != nil {
		return OperatorResult{}, false, err
	}
	if oppositeExists || resetExists || resetCompletionExists || resetClosureExists {
		return OperatorResult{}, false, platformerr.New(platformerr.CodeOperatorReplayMismatch,
			"requestId가 다른 운영 조작에 이미 사용됐어요")
	}
	if !recordExists {
		return OperatorResult{}, false, nil
	}

	var prev operatorDoc
	if err := recordSnap.DataTo(&prev); err != nil {
		return OperatorResult{}, false, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
			"운영 기록을 읽지 못했어요")
	}
	if !sameOperatorRequest(prev, in) {
		return OperatorResult{}, false, platformerr.New(platformerr.CodeOperatorReplayMismatch,
			"같은 requestId의 이전 운영 요청과 내용이 달라요")
	}
	result, err := l.operatorResult(ctx, in.PlatformUserID, false)
	return result, err == nil, err
}

func (l *Ledger) getOptional(
	ctx context.Context,
	path fspath.Path,
) (*firestore.DocumentSnapshot, bool, error) {
	snap, err := l.store.Get(ctx, path)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return snap, true, nil
}

func (in OperatorInput) validate() error {
	switch {
	case !operatorRequestIDPattern.MatchString(in.RequestID):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"요청 식별자가 필요해요")
	case !operatorPUIDPattern.MatchString(in.PlatformUserID):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"대상 사용자가 필요해요")
	case !operatorEntitlementPattern.MatchString(in.EntitlementID):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"대상 상품이 필요해요")
	case !operatorActorPattern.MatchString(in.ActorLogin):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"조작한 운영자 정보가 필요해요")
	case !ValidAdminMutationReason(in.Reason):
		// 이유 없는 지급은 나중에 아무도 설명할 수 없다.
		return platformerr.New(platformerr.CodeRequestInvalid,
			"허용된 운영 사유 코드가 필요해요")
	case !operatorAppIDPattern.MatchString(in.AppID):
		return platformerr.New(platformerr.CodeRequestInvalid,
			"앱 식별자가 필요해요")
	}
	return nil
}

// OperatorGrant는 운영자가 entitlement를 지급한다.
//
// 마켓 구매와 같은 원장에 source로 들어간다. platform이 operator다.
// 나중에 실제 구매가 들어와도 서로를 덮어쓰지 않고 OR로 합쳐진다.
func (l *Ledger) OperatorGrant(ctx context.Context, in OperatorInput) (OperatorResult, error) {
	if err := in.validate(); err != nil {
		return OperatorResult{}, err
	}
	if in.GrantRequestID != "" {
		return OperatorResult{}, platformerr.New(platformerr.CodeRequestInvalid,
			"지급 요청에는 grantRequestId를 넣을 수 없어요")
	}

	applied := false
	err := l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		applied = false
		now := l.now().UTC()
		recordPath, err := l.paths.operatorGrant(in.RequestID)
		if err != nil {
			return err
		}
		exists, snap, err := tx.Exists(recordPath)
		if err != nil {
			return err
		}
		oppositePath, err := l.paths.operatorRevocation(in.RequestID)
		if err != nil {
			return err
		}
		oppositeExists, _, err := tx.Exists(oppositePath)
		if err != nil {
			return err
		}
		resetPath, err := l.paths.sandboxResetRequest(in.RequestID)
		if err != nil {
			return err
		}
		resetExists, _, err := tx.Exists(resetPath)
		if err != nil {
			return err
		}
		resetCompletionPath, err := l.paths.sandboxResetCompletion(in.RequestID)
		if err != nil {
			return err
		}
		resetCompletionExists, _, err := tx.Exists(resetCompletionPath)
		if err != nil {
			return err
		}
		resetClosurePath, err := l.paths.sandboxResetClosure(in.RequestID)
		if err != nil {
			return err
		}
		resetClosureExists, _, err := tx.Exists(resetClosurePath)
		if err != nil {
			return err
		}
		if oppositeExists || resetExists || resetCompletionExists || resetClosureExists {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"requestId가 다른 운영 조작에 이미 사용됐어요")
		}
		if exists {
			var prev operatorDoc
			if err := snap.DataTo(&prev); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"운영 지급 기록을 읽지 못했어요")
			}
			if !sameOperatorRequest(prev, in) {
				return platformerr.New(platformerr.CodeOperatorReplayMismatch,
					"같은 requestId의 이전 지급 요청과 내용이 달라요")
			}
			return nil
		}

		orderKey := domain.OrderKey(domain.PlatformOperator, in.RequestID)
		orderPath, err := l.paths.order(orderKey)
		if err != nil {
			return err
		}
		orderExists, _, err := tx.Exists(orderPath)
		if err != nil {
			return err
		}
		if orderExists {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"운영 지급 주문과 감사 기록이 일치하지 않아요")
		}

		intPath, err := l.paths.internalEntitlement(in.PlatformUserID, in.EntitlementID)
		if err != nil {
			return err
		}
		ent, err := l.readEntitlement(tx, intPath, in.EntitlementID)
		if err != nil {
			return err
		}

		purchase := operatorPurchase(in.RequestID, in.EntitlementID, domain.StateActive, now)
		if err := tx.Create(orderPath, orderDoc{
			PlatformUserID:  in.PlatformUserID,
			EntitlementID:   in.EntitlementID,
			Platform:        purchase.Platform,
			ProductID:       purchase.ProductID,
			ProviderOrderID: purchase.ProviderOrderID,
			State:           purchase.State,
			PurchasedAt:     now,
			ObservedAt:      now,
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			return err
		}
		ent.Sources[orderKey] = domain.Source{
			Platform:    domain.PlatformOperator,
			ProductID:   in.EntitlementID,
			State:       domain.StateActive,
			PurchasedAt: now,
			ObservedAt:  now,
			UpdatedAt:   now,
		}
		if err := l.writeEntitlement(tx, in.PlatformUserID, ent, now); err != nil {
			return err
		}
		if err := tx.Create(recordPath, newOperatorDoc(in, now)); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return OperatorResult{}, err
	}
	return l.operatorResult(ctx, in.PlatformUserID, applied)
}

// OperatorRevoke는 운영자가 entitlement를 회수한다.
//
// 오지급 정정에 쓴다. 마켓 구매로 받은 것을 회수하면 사용자가
// 돈을 내고 산 물건을 잃으므로, 백오피스가 근거를 확인해야 한다.
func (l *Ledger) OperatorRevoke(ctx context.Context, in OperatorInput) (OperatorResult, error) {
	if err := in.validate(); err != nil {
		return OperatorResult{}, err
	}
	if !operatorRequestIDPattern.MatchString(in.GrantRequestID) {
		return OperatorResult{}, platformerr.New(platformerr.CodeRequestInvalid,
			"회수할 지급 requestId가 필요해요")
	}

	applied := false
	err := l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		applied = false
		now := l.now().UTC()
		revokePath, err := l.paths.operatorRevocation(in.RequestID)
		if err != nil {
			return err
		}
		exists, snap, err := tx.Exists(revokePath)
		if err != nil {
			return err
		}
		requestGrantPath, err := l.paths.operatorGrant(in.RequestID)
		if err != nil {
			return err
		}
		requestGrantExists, _, err := tx.Exists(requestGrantPath)
		if err != nil {
			return err
		}
		resetPath, err := l.paths.sandboxResetRequest(in.RequestID)
		if err != nil {
			return err
		}
		resetExists, _, err := tx.Exists(resetPath)
		if err != nil {
			return err
		}
		resetCompletionPath, err := l.paths.sandboxResetCompletion(in.RequestID)
		if err != nil {
			return err
		}
		resetCompletionExists, _, err := tx.Exists(resetCompletionPath)
		if err != nil {
			return err
		}
		resetClosurePath, err := l.paths.sandboxResetClosure(in.RequestID)
		if err != nil {
			return err
		}
		resetClosureExists, _, err := tx.Exists(resetClosurePath)
		if err != nil {
			return err
		}
		if requestGrantExists || resetExists || resetCompletionExists || resetClosureExists {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"requestId가 다른 운영 조작에 이미 사용됐어요")
		}
		if exists {
			var prev operatorDoc
			if err := snap.DataTo(&prev); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"운영 회수 기록을 읽지 못했어요")
			}
			if !sameOperatorRequest(prev, in) {
				return platformerr.New(platformerr.CodeOperatorReplayMismatch,
					"같은 requestId의 이전 회수 요청과 내용이 달라요")
			}
			return nil
		}

		grantPath, err := l.paths.operatorGrant(in.GrantRequestID)
		if err != nil {
			return err
		}
		grantExists, grantSnap, err := tx.Exists(grantPath)
		if err != nil {
			return err
		}
		if !grantExists {
			return platformerr.New(platformerr.CodePurchaseNotFound,
				"회수할 운영자 지급을 찾을 수 없어요")
		}
		var grant operatorDoc
		if err := grantSnap.DataTo(&grant); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"운영 지급 기록을 읽지 못했어요")
		}
		if grant.AppID != in.AppID || grant.PlatformUserID != in.PlatformUserID ||
			grant.EntitlementID != in.EntitlementID {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"회수 대상 지급과 사용자·앱·상품이 달라요")
		}

		orderKey := domain.OrderKey(domain.PlatformOperator, in.GrantRequestID)
		orderPath, err := l.paths.order(orderKey)
		if err != nil {
			return err
		}
		orderExists, orderSnap, err := tx.Exists(orderPath)
		if err != nil {
			return err
		}
		if !orderExists {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"운영 지급 주문을 찾을 수 없어요")
		}
		var order orderDoc
		if err := orderSnap.DataTo(&order); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"운영 지급 주문을 읽지 못했어요")
		}
		if order.Platform != domain.PlatformOperator || order.PlatformUserID != in.PlatformUserID ||
			order.EntitlementID != in.EntitlementID || order.ProductID != in.EntitlementID {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"운영 지급 주문이 회수 요청과 달라요")
		}
		if order.State != domain.StateActive {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"운영 지급이 이미 비활성 상태예요")
		}

		intPath, err := l.paths.internalEntitlement(in.PlatformUserID, in.EntitlementID)
		if err != nil {
			return err
		}
		ent, err := l.readEntitlement(tx, intPath, in.EntitlementID)
		if err != nil {
			return err
		}
		src, ok := ent.Sources[orderKey]
		if !ok || src.Platform != domain.PlatformOperator || src.State != domain.StateActive {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"회수할 operator source가 활성 상태가 아니에요")
		}

		order.State = domain.StateRevoked
		order.ObservedAt = now
		order.UpdatedAt = now
		if err := tx.Set(orderPath, order); err != nil {
			return err
		}
		src.State = domain.StateRevoked
		src.ObservedAt = now
		src.UpdatedAt = now
		ent.Sources[orderKey] = src
		if err := l.writeEntitlement(tx, in.PlatformUserID, ent, now); err != nil {
			return err
		}
		if err := tx.Create(revokePath, newOperatorDoc(in, now)); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return OperatorResult{}, err
	}
	return l.operatorResult(ctx, in.PlatformUserID, applied)
}

func operatorPurchase(requestID, entitlementID string, state domain.State, now time.Time) domain.VerifiedPurchase {
	return domain.VerifiedPurchase{
		Platform:        domain.PlatformOperator,
		ProductID:       entitlementID,
		CanonicalID:     requestID,
		ProviderOrderID: requestID,
		PurchasedAt:     now,
		ObservedAt:      now,
		State:           state,
		Completion:      domain.CompletionNone,
	}
}

func newOperatorDoc(in OperatorInput, now time.Time) operatorDoc {
	return operatorDoc{
		RequestID:      in.RequestID,
		GrantRequestID: in.GrantRequestID,
		PlatformUserID: in.PlatformUserID,
		EntitlementID:  in.EntitlementID,
		ActorLogin:     in.ActorLogin,
		Reason:         in.Reason,
		AppID:          in.AppID,
		CreatedAt:      now,
	}
}

func sameOperatorRequest(doc operatorDoc, in OperatorInput) bool {
	return doc.RequestID == in.RequestID &&
		doc.GrantRequestID == in.GrantRequestID &&
		doc.PlatformUserID == in.PlatformUserID &&
		doc.EntitlementID == in.EntitlementID &&
		doc.ActorLogin == in.ActorLogin &&
		doc.Reason == in.Reason &&
		doc.AppID == in.AppID
}

func (l *Ledger) operatorResult(ctx context.Context, puid string, applied bool) (OperatorResult, error) {
	list, err := l.ListActive(ctx, puid)
	if err != nil {
		return OperatorResult{}, err
	}
	return OperatorResult{Applied: applied, Entitlements: orEmpty(list)}, nil
}

// Admin mutation 한도는 인증된 OIDC principal 기준이다. X-Seori-Actor는
// 증명되지 않은 헤더라 키로 쓰면 공격자가 값을 바꿔 우회할 수 있다.
const (
	adminMutationsPerMinute = 5
	adminMutationsPerHour   = 20
	adminMutationsPerDay    = 50
)

type adminMutationLimitDoc struct {
	MinuteStart time.Time `firestore:"minuteStart"`
	MinuteCount int       `firestore:"minuteCount"`
	HourStart   time.Time `firestore:"hourStart"`
	HourCount   int       `firestore:"hourCount"`
	DayStart    time.Time `firestore:"dayStart"`
	DayCount    int       `firestore:"dayCount"`
	UpdatedAt   time.Time `firestore:"updatedAt"`
}

// CheckAdminMutationRate는 모든 replica가 공유하는 Firestore durable gate다.
// 실제 조작이 뒤에서 실패해도 quota는 되돌리지 않는다. 보안 한도는
// fail-open보다 보수적으로 소모되는 편이 안전하다.
func (l *Ledger) CheckAdminMutationRate(ctx context.Context, principal string) error {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return platformerr.New(platformerr.CodeAuthForbidden,
			"인증된 조작 주체가 필요해요")
	}
	sum := sha256.Sum256([]byte(principal))
	path, err := l.paths.adminMutationLimit(hex.EncodeToString(sum[:]))
	if err != nil {
		return err
	}

	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := l.now().UTC()
		minute := now.Truncate(time.Minute)
		hour := now.Truncate(time.Hour)
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

		var doc adminMutationLimitDoc
		exists, snap, err := tx.Exists(path)
		if err != nil {
			return err
		}
		if exists {
			if err := snap.DataTo(&doc); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"운영 조작 한도를 읽지 못했어요")
			}
		}
		if !doc.MinuteStart.Equal(minute) {
			doc.MinuteStart, doc.MinuteCount = minute, 0
		}
		if !doc.HourStart.Equal(hour) {
			doc.HourStart, doc.HourCount = hour, 0
		}
		if !doc.DayStart.Equal(day) {
			doc.DayStart, doc.DayCount = day, 0
		}
		if doc.MinuteCount >= adminMutationsPerMinute ||
			doc.HourCount >= adminMutationsPerHour ||
			doc.DayCount >= adminMutationsPerDay {
			return platformerr.New(platformerr.CodeRateLimited,
				"운영 조작 한도를 초과했어요")
		}
		doc.MinuteCount++
		doc.HourCount++
		doc.DayCount++
		doc.UpdatedAt = now
		return tx.Set(path, doc)
	})
}

// OperatorRecord는 조회용 감사 기록이다.
type OperatorRecord struct {
	RequestID      string    `json:"requestId"`
	GrantRequestID string    `json:"grantRequestId,omitempty"`
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
		// 자유 서술 reason, 이메일 원문 actor, 계약 형식 밖의 식별자가 있는
		// 레거시 레코드는 마이그레이션 전까지 fail-closed해 브라우저로
		// 노출하지 않는다.
		if !validOperatorRecord(doc, kind) {
			return nil, platformerr.New(platformerr.CodeLedgerStateInvalid,
				"운영 기록에 노출할 수 없는 감사 값이 있어요")
		}

		out = append(out, OperatorRecord{
			RequestID:      doc.RequestID,
			GrantRequestID: doc.GrantRequestID,
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

func validOperatorRecord(doc operatorDoc, kind string) bool {
	if !operatorRequestIDPattern.MatchString(doc.RequestID) ||
		!operatorPUIDPattern.MatchString(doc.PlatformUserID) ||
		!operatorEntitlementPattern.MatchString(doc.EntitlementID) ||
		!operatorActorPattern.MatchString(doc.ActorLogin) ||
		!ValidAdminMutationReason(doc.Reason) ||
		!operatorAppIDPattern.MatchString(doc.AppID) || doc.CreatedAt.IsZero() {
		return false
	}
	switch kind {
	case "grant":
		return doc.GrantRequestID == ""
	case "revoke":
		return operatorRequestIDPattern.MatchString(doc.GrantRequestID)
	default:
		return false
	}
}

// 조회 상한. 백오피스가 실수로 전체를 긁어가지 못하게 한다.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// OrderSummary는 Admin 응답을 구성하는 데 필요한 내부 주문 요약이다.
//
// providerOrderId, canonicalId와 마켓 계정 해시는 읽지 않는다. 운영자가 볼
// 이유가 없고 응답 객체에 실리면 서버 컴포넌트 payload와 로그로 퍼진다.
type OrderSummary struct {
	OrderKey       string    `json:"orderKey"`
	PlatformUserID string    `json:"platformUserId"`
	EntitlementID  string    `json:"entitlementId"`
	Platform       string    `json:"platform"`
	ProductID      string    `json:"productId"`
	State          string    `json:"state"`
	PurchasedAt    time.Time `json:"purchasedAt"`
	ObservedAt     time.Time `json:"observedAt"`
	Tombstone      bool      `json:"tombstone"`
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
			OrderKey:       snap.Ref.ID,
			PlatformUserID: doc.PlatformUserID,
			EntitlementID:  doc.EntitlementID,
			Platform:       string(doc.Platform),
			ProductID:      doc.ProductID,
			State:          string(doc.State),
			PurchasedAt:    doc.PurchasedAt,
			ObservedAt:     doc.ObservedAt,
			Tombstone:      doc.Tombstone,
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
