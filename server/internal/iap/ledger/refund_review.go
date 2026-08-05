package ledger

import (
	"context"
	"errors"
	"regexp"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/refundreview"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

const (
	RefundReviewPending   = "pending"
	RefundReviewDecided   = "decided"
	RefundReviewResponded = "responded"
	RefundReviewExpired   = "expired"
	RefundReviewFailed    = "failed"

	RefundPreferenceDecline = "DECLINE"
	RefundPreferenceApprove = "APPROVE"
	RefundPreferenceNeutral = "NEUTRAL"

	RefundReasonVerifiedFulfillment     = "verified_fulfillment"
	RefundReasonCustomerRefundSupported = "customer_refund_supported"
	RefundReasonInsufficientEvidence    = "insufficient_evidence"
	RefundReasonInternalValidation      = "internal_validation"

	refundReviewLimitDefault = 50
	refundReviewLimitMax     = 100
	refundReviewDueWindow    = 24 * time.Hour
	refundReviewDueSoon      = time.Hour
	refundReviewBackoffMax   = 30 * time.Minute
)

var (
	refundReviewHashPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	refundReviewPackagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$`)
	refundReviewUUIDPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// PendingRefundReviewInput은 RTDN에서 검증한 환불 검토 알림이다.
type PendingRefundReviewInput struct {
	ReviewID     string
	AppID        string
	PackageName  string
	OrderIDHash  string
	Environment  string
	RefundReason int64
	ReceivedAt   time.Time
	Secret       refundreview.Envelope
}

// RefundReviewDecisionInput은 Google 외부 호출 전에 영구 확정할 command다.
type RefundReviewDecisionInput struct {
	RequestID             string
	ReviewID              string
	AppID                 string
	ExpectedEnvironment   string
	RefundPreference      string
	SampleContentProvided bool
	Reason                string
	ActorLogin            string
}

// RefundReviewDecisionResult는 Admin이 target binding을 대조할 응답이다.
type RefundReviewDecisionResult struct {
	Applied               bool
	RequestID             string
	ReviewID              string
	AppID                 string
	ExpectedEnvironment   string
	State                 string
	RefundPreference      string
	SampleContentProvided bool
}

// RefundReviewSummary는 Admin에 노출 가능한 PII-free projection이다.
type RefundReviewSummary struct {
	ReviewID              string
	AppID                 string
	Environment           string
	State                 string
	RefundReason          int64
	ReceivedAt            time.Time
	DueAt                 time.Time
	RequestID             string
	RefundPreference      string
	SampleContentProvided *bool
	DecisionReason        string
	DecidedAt             time.Time
	RespondedAt           time.Time
	FailedAt              time.Time
	ExpiredAt             time.Time
	LastErrorCode         string
}

// RefundReviewWork는 worker가 점유한 항목이다. 원문 비밀 대신 봉투만 가진다.
type RefundReviewWork struct {
	ReviewID              string
	LeaseID               string
	AttemptCount          int
	Binding               refundreview.Binding
	Secret                refundreview.Envelope
	RefundPreference      string
	SampleContentProvided bool
	DueAt                 time.Time
}

// RefundReviewHealth는 Admin health의 queue 요약이다.
type RefundReviewHealth struct {
	Pending int
	DueSoon int
	Failed  int
}

type refundReviewDoc struct {
	ReviewID     string `firestore:"reviewId"`
	AppID        string `firestore:"appId"`
	PackageName  string `firestore:"packageName"`
	OrderIDHash  string `firestore:"orderIdSha256"`
	Environment  string `firestore:"environment"`
	RefundReason int64  `firestore:"refundReason"`

	State      string    `firestore:"state"`
	ReceivedAt time.Time `firestore:"receivedAt"`
	DueAt      time.Time `firestore:"dueAt"`

	RequestID             string    `firestore:"requestId,omitempty"`
	RefundPreference      string    `firestore:"refundPreference,omitempty"`
	SampleContentProvided bool      `firestore:"sampleContentProvided,omitempty"`
	HasSampleContentValue bool      `firestore:"hasSampleContentValue,omitempty"`
	DecisionReason        string    `firestore:"decisionReason,omitempty"`
	ActorLogin            string    `firestore:"actorLogin,omitempty"`
	DecidedAt             time.Time `firestore:"decidedAt,omitempty"`

	AttemptCount   int       `firestore:"attemptCount"`
	NextAttemptAt  time.Time `firestore:"nextAttemptAt,omitempty"`
	LeaseID        string    `firestore:"leaseId,omitempty"`
	ClaimExpiresAt time.Time `firestore:"claimExpiresAt,omitempty"`
	RespondedAt    time.Time `firestore:"respondedAt,omitempty"`
	FailedAt       time.Time `firestore:"failedAt,omitempty"`
	ExpiredAt      time.Time `firestore:"expiredAt,omitempty"`
	LastErrorCode  string    `firestore:"lastErrorCode,omitempty"`

	Secret    *refundreview.Envelope `firestore:"secret,omitempty"`
	CreatedAt time.Time              `firestore:"createdAt"`
	UpdatedAt time.Time              `firestore:"updatedAt"`
}

type refundReviewDecisionDoc struct {
	RequestID             string    `firestore:"requestId"`
	ReviewID              string    `firestore:"reviewId"`
	AppID                 string    `firestore:"appId"`
	ExpectedEnvironment   string    `firestore:"expectedEnvironment"`
	RefundPreference      string    `firestore:"refundPreference"`
	SampleContentProvided bool      `firestore:"sampleContentProvided"`
	Reason                string    `firestore:"reason"`
	ActorLogin            string    `firestore:"actorLogin"`
	CreatedAt             time.Time `firestore:"createdAt"`
}

// ValidRefundReviewPreference는 Google 계약에 있는 세 값만 허용한다.
func ValidRefundReviewPreference(value string) bool {
	switch value {
	case RefundPreferenceDecline, RefundPreferenceApprove, RefundPreferenceNeutral:
		return true
	default:
		return false
	}
}

// ValidRefundReviewDecisionReason은 자유 서술 대신 고정 감사 사유만 허용한다.
func ValidRefundReviewDecisionReason(value string) bool {
	switch value {
	case RefundReasonVerifiedFulfillment, RefundReasonCustomerRefundSupported,
		RefundReasonInsufficientEvidence, RefundReasonInternalValidation:
		return true
	default:
		return false
	}
}

// ValidRefundReviewRequestID는 Backoffice가 만드는 canonical UUID만 허용한다.
func ValidRefundReviewRequestID(value string) bool {
	return refundReviewUUIDPattern.MatchString(value)
}

// RecordPendingRefundReview는 같은 pending token을 한 번만 원장에 기록한다.
func (l *Ledger) RecordPendingRefundReview(ctx context.Context, in PendingRefundReviewInput) error {
	if err := validatePendingRefundReviewInput(in); err != nil {
		return err
	}
	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		path, err := l.paths.pendingRefundReview(in.ReviewID)
		if err != nil {
			return err
		}
		exists, snap, err := tx.Exists(path)
		if err != nil {
			return err
		}
		if exists {
			var previous refundReviewDoc
			if err := snap.DataTo(&previous); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"환불 검토 원장을 읽지 못했어요")
			}
			if previous.ReviewID != in.ReviewID || previous.AppID != in.AppID ||
				previous.PackageName != in.PackageName || previous.OrderIDHash != in.OrderIDHash ||
				previous.Environment != in.Environment || previous.RefundReason != in.RefundReason {
				return platformerr.New(platformerr.CodeRefundReviewReplayMismatch,
					"같은 환불 token의 이전 알림과 내용이 달라요")
			}
			return nil
		}

		receivedAt := in.ReceivedAt.UTC()
		return tx.Create(path, refundReviewDoc{
			ReviewID: in.ReviewID, AppID: in.AppID, PackageName: in.PackageName,
			OrderIDHash: in.OrderIDHash, Environment: in.Environment,
			RefundReason: in.RefundReason, State: RefundReviewPending,
			ReceivedAt: receivedAt, DueAt: receivedAt.Add(refundReviewDueWindow),
			Secret: &in.Secret, CreatedAt: receivedAt, UpdatedAt: receivedAt,
		})
	})
}

func validatePendingRefundReviewInput(in PendingRefundReviewInput) error {
	if !refundReviewHashPattern.MatchString(in.ReviewID) ||
		!operatorAppIDPattern.MatchString(in.AppID) ||
		!refundReviewPackagePattern.MatchString(in.PackageName) ||
		!refundReviewHashPattern.MatchString(in.OrderIDHash) ||
		(in.Environment != string(lEnvSandbox) && in.Environment != string(lEnvProduction)) ||
		in.ReceivedAt.IsZero() || in.Secret.KeyID == "" || in.Secret.Nonce == "" || in.Secret.Ciphertext == "" {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"환불 검토 알림이 올바르지 않아요")
	}
	return nil
}

// domain import cycle을 만들지 않고 환경 문자열을 한 곳에서 오타 없이 쓴다.
const (
	lEnvSandbox    = "sandbox"
	lEnvProduction = "production"
)

// ListRefundReviews는 앱 범위의 안전한 환불 검토 projection을 준다.
func (l *Ledger) ListRefundReviews(ctx context.Context, appID, state string, limit int) ([]RefundReviewSummary, error) {
	if !operatorAppIDPattern.MatchString(appID) || (state != "" && !validRefundReviewState(state)) {
		return nil, platformerr.New(platformerr.CodeRequestInvalid,
			"환불 검토 조회 조건이 올바르지 않아요")
	}
	if limit <= 0 {
		limit = refundReviewLimitDefault
	}
	if limit > refundReviewLimitMax {
		limit = refundReviewLimitMax
	}
	col, err := l.paths.pendingRefundReviewCollection()
	if err != nil {
		return nil, err
	}
	iter, err := l.store.Query(ctx, col, func(q firestore.Query) firestore.Query {
		q = q.Where("appId", "==", appID)
		if state != "" {
			q = q.Where("state", "==", state)
		}
		return q.OrderBy("dueAt", firestore.Asc).Limit(limit)
	})
	if err != nil {
		return nil, err
	}
	defer iter.Stop()
	out := []RefundReviewSummary{}
	for {
		snap, err := iter.Next()
		if store.IsDone(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		var doc refundReviewDoc
		if err := snap.DataTo(&doc); err != nil {
			return nil, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"환불 검토 원장을 읽지 못했어요")
		}
		if err := validateRefundReviewDocAt(doc, snap.Ref.ID); err != nil {
			return nil, err
		}
		out = append(out, summarizeRefundReview(doc))
	}
}

// FindRefundReviewDecisionReplay는 mutable 앱 상태와 rate gate보다 먼저 exact retry를 찾는다.
func (l *Ledger) FindRefundReviewDecisionReplay(
	ctx context.Context,
	in RefundReviewDecisionInput,
) (RefundReviewDecisionResult, bool, error) {
	if err := validateRefundReviewDecisionInput(in); err != nil {
		return RefundReviewDecisionResult{}, false, err
	}
	decisionPath, err := l.paths.refundReviewDecision(in.RequestID)
	if err != nil {
		return RefundReviewDecisionResult{}, false, err
	}
	decisionSnap, exists, err := l.getOptional(ctx, decisionPath)
	if err != nil {
		return RefundReviewDecisionResult{}, false, err
	}
	if collision, err := l.otherAdminRequestExists(ctx, in.RequestID); err != nil {
		return RefundReviewDecisionResult{}, false, err
	} else if collision {
		return RefundReviewDecisionResult{}, false, platformerr.New(platformerr.CodeOperatorReplayMismatch,
			"requestId가 다른 운영 조작에 이미 사용됐어요")
	}
	if !exists {
		return RefundReviewDecisionResult{}, false, nil
	}
	var decision refundReviewDecisionDoc
	if err := decisionSnap.DataTo(&decision); err != nil {
		return RefundReviewDecisionResult{}, false, platformerr.Wrap(err,
			platformerr.CodeLedgerStateInvalid, "환불 검토 결정을 읽지 못했어요")
	}
	if !sameRefundReviewDecision(decision, in) {
		return RefundReviewDecisionResult{}, false, platformerr.New(
			platformerr.CodeOperatorReplayMismatch, "같은 requestId의 환불 검토 결정이 달라요")
	}
	review, err := l.getRefundReview(ctx, in.ReviewID)
	if err != nil {
		return RefundReviewDecisionResult{}, false, err
	}
	return refundReviewDecisionResult(review, false), true, nil
}

// DecideRefundReview는 결정 문서 create와 review 상태 전이를 한 트랜잭션에 묶는다.
func (l *Ledger) DecideRefundReview(ctx context.Context, in RefundReviewDecisionInput) (RefundReviewDecisionResult, error) {
	if err := validateRefundReviewDecisionInput(in); err != nil {
		return RefundReviewDecisionResult{}, err
	}
	var result RefundReviewDecisionResult
	err := l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		result = RefundReviewDecisionResult{}
		now := l.now().UTC()
		decisionPath, err := l.paths.refundReviewDecision(in.RequestID)
		if err != nil {
			return err
		}
		decisionExists, decisionSnap, err := tx.Exists(decisionPath)
		if err != nil {
			return err
		}
		collision, err := l.otherAdminRequestExistsTx(tx, in.RequestID)
		if err != nil {
			return err
		}
		if collision {
			return platformerr.New(platformerr.CodeOperatorReplayMismatch,
				"requestId가 다른 운영 조작에 이미 사용됐어요")
		}

		reviewPath, err := l.paths.pendingRefundReview(in.ReviewID)
		if err != nil {
			return err
		}
		reviewExists, reviewSnap, err := tx.Exists(reviewPath)
		if err != nil {
			return err
		}
		if !reviewExists {
			return platformerr.New(platformerr.CodeRefundReviewNotFound,
				"환불 검토 항목을 찾을 수 없어요")
		}
		var review refundReviewDoc
		if err := reviewSnap.DataTo(&review); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"환불 검토 원장을 읽지 못했어요")
		}
		if err := validateRefundReviewDocAt(review, in.ReviewID); err != nil {
			return err
		}

		if decisionExists {
			var previous refundReviewDecisionDoc
			if err := decisionSnap.DataTo(&previous); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"환불 검토 결정을 읽지 못했어요")
			}
			if !sameRefundReviewDecision(previous, in) {
				return platformerr.New(platformerr.CodeOperatorReplayMismatch,
					"같은 requestId의 환불 검토 결정이 달라요")
			}
			result = refundReviewDecisionResult(review, false)
			return nil
		}
		if review.AppID != in.AppID || review.Environment != in.ExpectedEnvironment {
			return platformerr.New(platformerr.CodeRefundReviewReplayMismatch,
				"환불 검토 대상 binding이 요청과 달라요")
		}
		if !review.DueAt.After(now) || review.State == RefundReviewExpired {
			return platformerr.New(platformerr.CodeRefundReviewExpired,
				"환불 검토 응답 기한이 지났어요")
		}
		if review.State != RefundReviewPending || review.RequestID != "" {
			return platformerr.New(platformerr.CodeRefundReviewAlreadyDecided,
				"환불 검토 의견이 이미 확정됐어요")
		}

		decision := refundReviewDecisionDoc{
			RequestID: in.RequestID, ReviewID: in.ReviewID, AppID: in.AppID,
			ExpectedEnvironment:   in.ExpectedEnvironment,
			RefundPreference:      in.RefundPreference,
			SampleContentProvided: in.SampleContentProvided,
			Reason:                in.Reason, ActorLogin: in.ActorLogin, CreatedAt: now,
		}
		review.State = RefundReviewDecided
		review.RequestID = in.RequestID
		review.RefundPreference = in.RefundPreference
		review.SampleContentProvided = in.SampleContentProvided
		review.HasSampleContentValue = true
		review.DecisionReason = in.Reason
		review.ActorLogin = in.ActorLogin
		review.DecidedAt = now
		review.NextAttemptAt = now
		review.UpdatedAt = now
		if err := tx.Create(decisionPath, decision); err != nil {
			return err
		}
		if err := tx.Set(reviewPath, review); err != nil {
			return err
		}
		result = refundReviewDecisionResult(review, true)
		return nil
	})
	return result, err
}

// SweepExpiredRefundReviews는 24시간이 지난 미응답 원장을 종결하고 비밀을 지운다.
func (l *Ledger) SweepExpiredRefundReviews(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > refundReviewLimitMax {
		limit = refundReviewLimitMax
	}
	expired := 0
	err := l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := l.now().UTC()
		col, err := l.paths.pendingRefundReviewCollection()
		if err != nil {
			return err
		}
		iter, err := tx.Query(col, func(q firestore.Query) firestore.Query {
			return q.Where("state", "in", []string{RefundReviewPending, RefundReviewDecided}).
				Where("dueAt", "<=", now).OrderBy("dueAt", firestore.Asc).Limit(limit)
		})
		if err != nil {
			return err
		}
		defer iter.Stop()
		var docs []struct {
			id  string
			doc refundReviewDoc
		}
		for {
			snap, err := iter.Next()
			if store.IsDone(err) {
				break
			}
			if err != nil {
				return err
			}
			var doc refundReviewDoc
			if err := snap.DataTo(&doc); err != nil {
				return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
					"만료할 환불 검토 원장을 읽지 못했어요")
			}
			if err := validateRefundReviewDocAt(doc, snap.Ref.ID); err != nil {
				return err
			}
			docs = append(docs, struct {
				id  string
				doc refundReviewDoc
			}{snap.Ref.ID, doc})
		}
		for _, item := range docs {
			// 다른 Cloud Run Job 실행이 이미 Google 호출 중이면 마감 시각을
			// 넘겼다는 이유로 lease와 봉투를 먼저 지우지 않는다. 외부 성공 뒤
			// Complete가 원장을 responded로 닫을 기회를 보존한다.
			if refundReviewLeaseActive(item.doc, now) {
				continue
			}
			path, err := l.paths.pendingRefundReview(item.id)
			if err != nil {
				return err
			}
			item.doc.State = RefundReviewExpired
			item.doc.ExpiredAt = now
			item.doc.Secret = nil
			item.doc.LeaseID = ""
			item.doc.ClaimExpiresAt = time.Time{}
			item.doc.NextAttemptAt = time.Time{}
			item.doc.UpdatedAt = now
			if err := tx.Set(path, item.doc); err != nil {
				return err
			}
			expired++
		}
		return nil
	})
	return expired, err
}

func refundReviewLeaseActive(doc refundReviewDoc, now time.Time) bool {
	return doc.LeaseID != "" && doc.ClaimExpiresAt.After(now)
}

// ClaimNextRefundReview는 제출할 결정을 lease로 한 건 점유한다.
func (l *Ledger) ClaimNextRefundReview(ctx context.Context) (RefundReviewWork, bool, error) {
	leaseID, err := newLeaseID()
	if err != nil {
		return RefundReviewWork{}, false, err
	}
	var work RefundReviewWork
	var found bool
	err = l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		work, found = RefundReviewWork{}, false
		now := l.now().UTC()
		col, err := l.paths.pendingRefundReviewCollection()
		if err != nil {
			return err
		}
		iter, err := tx.Query(col, func(q firestore.Query) firestore.Query {
			return q.Where("state", "==", RefundReviewDecided).
				Where("nextAttemptAt", "<=", now).
				OrderBy("nextAttemptAt", firestore.Asc).Limit(1)
		})
		if err != nil {
			return err
		}
		defer iter.Stop()
		snap, err := iter.Next()
		if store.IsDone(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var doc refundReviewDoc
		if err := snap.DataTo(&doc); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"환불 검토 원장을 읽지 못했어요")
		}
		if err := validateRefundReviewDocAt(doc, snap.Ref.ID); err != nil {
			return err
		}
		if !doc.DueAt.After(now) {
			// sweep와 claim 사이에도 시계는 흐른다. 마감 이후에는 Google에
			// 호출할 수 없도록 claim 자체에서 다시 fail-closed한다.
			doc.State = RefundReviewExpired
			doc.ExpiredAt = now
			doc.Secret = nil
			doc.LeaseID = ""
			doc.ClaimExpiresAt = time.Time{}
			doc.NextAttemptAt = time.Time{}
			doc.UpdatedAt = now
			path, pathErr := l.paths.pendingRefundReview(doc.ReviewID)
			if pathErr != nil {
				return pathErr
			}
			return tx.Set(path, doc)
		}
		if doc.Secret == nil || !doc.HasSampleContentValue || doc.RequestID == "" {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"제출할 환불 검토 결정이 완전하지 않아요")
		}
		path, err := l.paths.pendingRefundReview(doc.ReviewID)
		if err != nil {
			return err
		}
		doc.AttemptCount++
		doc.LeaseID = leaseID
		doc.ClaimExpiresAt = now.Add(leaseTTL)
		// worker가 죽으면 lease 만료 뒤 같은 immutable 결정을 다시 집는다.
		doc.NextAttemptAt = doc.ClaimExpiresAt
		doc.UpdatedAt = now
		if err := tx.Set(path, doc); err != nil {
			return err
		}
		work = refundReviewWork(doc)
		found = true
		return nil
	})
	return work, found, err
}

// CompleteRefundReview는 Google OK 뒤 원장을 종결하고 비밀을 제거한다.
func (l *Ledger) CompleteRefundReview(ctx context.Context, reviewID, leaseID string) error {
	return l.updateClaimedRefundReview(ctx, reviewID, leaseID, func(doc *refundReviewDoc, now time.Time) {
		doc.State = RefundReviewResponded
		doc.RespondedAt = now
		doc.Secret = nil
		doc.NextAttemptAt = time.Time{}
	})
}

// FailRefundReview는 같은 결정을 재시도하거나 영구 실패로 종결한다.
func (l *Ledger) FailRefundReview(
	ctx context.Context,
	reviewID, leaseID string,
	errCode platformerr.Code,
	retryable bool,
) error {
	return l.updateClaimedRefundReview(ctx, reviewID, leaseID, func(doc *refundReviewDoc, now time.Time) {
		doc.LastErrorCode = string(errCode)
		if retryable && now.Before(doc.DueAt) {
			doc.NextAttemptAt = now.Add(refundReviewBackoff(doc.AttemptCount))
			return
		}
		doc.State = RefundReviewFailed
		doc.FailedAt = now
		doc.Secret = nil
		doc.NextAttemptAt = time.Time{}
	})
}

func (l *Ledger) updateClaimedRefundReview(
	ctx context.Context,
	reviewID, leaseID string,
	mutate func(*refundReviewDoc, time.Time),
) error {
	if !refundReviewHashPattern.MatchString(reviewID) || leaseID == "" {
		return platformerr.New(platformerr.CodeRefundReviewClaimLost,
			"환불 검토 점유 정보가 올바르지 않아요")
	}
	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		path, err := l.paths.pendingRefundReview(reviewID)
		if err != nil {
			return err
		}
		exists, snap, err := tx.Exists(path)
		if err != nil {
			return err
		}
		if !exists {
			return platformerr.New(platformerr.CodeRefundReviewClaimLost,
				"환불 검토 원장을 찾을 수 없어요")
		}
		var doc refundReviewDoc
		if err := snap.DataTo(&doc); err != nil {
			return platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
				"환불 검토 원장을 읽지 못했어요")
		}
		if err := validateRefundReviewDocAt(doc, reviewID); err != nil {
			return err
		}
		if doc.State == RefundReviewResponded {
			return nil
		}
		if doc.State != RefundReviewDecided || doc.LeaseID != leaseID {
			return platformerr.New(platformerr.CodeRefundReviewClaimLost,
				"환불 검토 점유를 잃었어요")
		}
		now := l.now().UTC()
		mutate(&doc, now)
		doc.LeaseID = ""
		doc.ClaimExpiresAt = time.Time{}
		doc.UpdatedAt = now
		return tx.Set(path, doc)
	})
}

// RefundReviewHealth는 queue 전체의 운영 상태를 센다.
func (l *Ledger) RefundReviewHealth(ctx context.Context) (RefundReviewHealth, error) {
	col, err := l.paths.pendingRefundReviewCollection()
	if err != nil {
		return RefundReviewHealth{}, err
	}
	now := l.now().UTC()
	health := RefundReviewHealth{}
	queries := []struct {
		apply func(firestore.Query) firestore.Query
		set   func(int)
	}{
		{
			apply: func(q firestore.Query) firestore.Query {
				return q.Where("state", "in", []string{RefundReviewPending, RefundReviewDecided})
			},
			set: func(n int) { health.Pending = n },
		},
		{
			apply: func(q firestore.Query) firestore.Query {
				return q.Where("state", "in", []string{RefundReviewPending, RefundReviewDecided}).
					Where("dueAt", ">", now).Where("dueAt", "<=", now.Add(refundReviewDueSoon))
			},
			set: func(n int) { health.DueSoon = n },
		},
		{
			apply: func(q firestore.Query) firestore.Query { return q.Where("state", "==", RefundReviewFailed) },
			set:   func(n int) { health.Failed = n },
		},
	}
	for _, query := range queries {
		iter, err := l.store.Query(ctx, col, query.apply)
		if err != nil {
			return RefundReviewHealth{}, err
		}
		count := 0
		for {
			_, err := iter.Next()
			if store.IsDone(err) {
				break
			}
			if err != nil {
				iter.Stop()
				return RefundReviewHealth{}, err
			}
			count++
		}
		iter.Stop()
		query.set(count)
	}
	return health, nil
}

func (l *Ledger) getRefundReview(ctx context.Context, reviewID string) (refundReviewDoc, error) {
	path, err := l.paths.pendingRefundReview(reviewID)
	if err != nil {
		return refundReviewDoc{}, err
	}
	snap, err := l.store.Get(ctx, path)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return refundReviewDoc{}, platformerr.New(platformerr.CodeRefundReviewNotFound,
				"환불 검토 항목을 찾을 수 없어요")
		}
		return refundReviewDoc{}, err
	}
	var doc refundReviewDoc
	if err := snap.DataTo(&doc); err != nil {
		return refundReviewDoc{}, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
			"환불 검토 원장을 읽지 못했어요")
	}
	if err := validateRefundReviewDocAt(doc, reviewID); err != nil {
		return refundReviewDoc{}, err
	}
	return doc, nil
}

func (l *Ledger) otherAdminRequestExists(ctx context.Context, requestID string) (bool, error) {
	paths, err := l.otherAdminRequestPaths(requestID)
	if err != nil {
		return false, err
	}
	for _, path := range paths {
		if _, exists, err := l.getOptional(ctx, path); err != nil {
			return false, err
		} else if exists {
			return true, nil
		}
	}
	return false, nil
}

func (l *Ledger) otherAdminRequestExistsTx(tx *store.Tx, requestID string) (bool, error) {
	paths, err := l.otherAdminRequestPaths(requestID)
	if err != nil {
		return false, err
	}
	for _, path := range paths {
		exists, _, err := tx.Exists(path)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}

func (l *Ledger) otherAdminRequestPaths(requestID string) ([]fspath.Path, error) {
	builders := []func(string) (fspath.Path, error){
		l.paths.operatorGrant,
		l.paths.operatorRevocation,
		l.paths.sandboxResetRequest,
		l.paths.sandboxResetCompletion,
		l.paths.sandboxResetClosure,
	}
	paths := make([]fspath.Path, 0, len(builders))
	for _, build := range builders {
		path, err := build(requestID)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func validateRefundReviewDecisionInput(in RefundReviewDecisionInput) error {
	if !ValidRefundReviewRequestID(in.RequestID) ||
		!refundReviewHashPattern.MatchString(in.ReviewID) ||
		!operatorAppIDPattern.MatchString(in.AppID) ||
		(in.ExpectedEnvironment != lEnvSandbox && in.ExpectedEnvironment != lEnvProduction) ||
		!ValidRefundReviewPreference(in.RefundPreference) ||
		!ValidRefundReviewDecisionReason(in.Reason) ||
		!operatorActorPattern.MatchString(in.ActorLogin) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"환불 검토 결정이 올바르지 않아요")
	}
	return nil
}

func validateRefundReviewDoc(doc refundReviewDoc) error {
	if !refundReviewHashPattern.MatchString(doc.ReviewID) ||
		!operatorAppIDPattern.MatchString(doc.AppID) ||
		!refundReviewPackagePattern.MatchString(doc.PackageName) ||
		!refundReviewHashPattern.MatchString(doc.OrderIDHash) ||
		(doc.Environment != lEnvSandbox && doc.Environment != lEnvProduction) ||
		!validRefundReviewState(doc.State) || doc.ReceivedAt.IsZero() || doc.DueAt.IsZero() {
		return platformerr.New(platformerr.CodeLedgerStateInvalid,
			"환불 검토 원장 값이 올바르지 않아요")
	}
	return nil
}

func validateRefundReviewDocAt(doc refundReviewDoc, documentID string) error {
	if err := validateRefundReviewDoc(doc); err != nil {
		return err
	}
	if doc.ReviewID != documentID {
		return platformerr.New(platformerr.CodeLedgerStateInvalid,
			"환불 검토 원장 경로와 reviewId가 일치하지 않아요")
	}
	return nil
}

func validRefundReviewState(state string) bool {
	switch state {
	case RefundReviewPending, RefundReviewDecided, RefundReviewResponded,
		RefundReviewExpired, RefundReviewFailed:
		return true
	default:
		return false
	}
}

func sameRefundReviewDecision(doc refundReviewDecisionDoc, in RefundReviewDecisionInput) bool {
	return doc.RequestID == in.RequestID && doc.ReviewID == in.ReviewID &&
		doc.AppID == in.AppID && doc.ExpectedEnvironment == in.ExpectedEnvironment &&
		doc.RefundPreference == in.RefundPreference &&
		doc.SampleContentProvided == in.SampleContentProvided &&
		doc.Reason == in.Reason && doc.ActorLogin == in.ActorLogin
}

func refundReviewDecisionResult(doc refundReviewDoc, applied bool) RefundReviewDecisionResult {
	return RefundReviewDecisionResult{
		Applied: applied, RequestID: doc.RequestID, ReviewID: doc.ReviewID,
		AppID: doc.AppID, ExpectedEnvironment: doc.Environment, State: doc.State,
		RefundPreference:      doc.RefundPreference,
		SampleContentProvided: doc.SampleContentProvided,
	}
}

func summarizeRefundReview(doc refundReviewDoc) RefundReviewSummary {
	var sample *bool
	if doc.HasSampleContentValue {
		value := doc.SampleContentProvided
		sample = &value
	}
	return RefundReviewSummary{
		ReviewID: doc.ReviewID, AppID: doc.AppID, Environment: doc.Environment,
		State: doc.State, RefundReason: doc.RefundReason,
		ReceivedAt: doc.ReceivedAt, DueAt: doc.DueAt, RequestID: doc.RequestID,
		RefundPreference: doc.RefundPreference, SampleContentProvided: sample,
		DecisionReason: doc.DecisionReason, DecidedAt: doc.DecidedAt,
		RespondedAt: doc.RespondedAt, FailedAt: doc.FailedAt,
		ExpiredAt: doc.ExpiredAt, LastErrorCode: doc.LastErrorCode,
	}
}

func refundReviewWork(doc refundReviewDoc) RefundReviewWork {
	return RefundReviewWork{
		ReviewID: doc.ReviewID, LeaseID: doc.LeaseID, AttemptCount: doc.AttemptCount,
		Binding: refundreview.Binding{
			ReviewID: doc.ReviewID, AppID: doc.AppID, PackageName: doc.PackageName,
			OrderIDHash: doc.OrderIDHash, Environment: doc.Environment,
		},
		Secret: *doc.Secret, RefundPreference: doc.RefundPreference,
		SampleContentProvided: doc.SampleContentProvided, DueAt: doc.DueAt,
	}
}

func refundReviewBackoff(attempt int) time.Duration {
	d := backoffFor(attempt)
	if d > refundReviewBackoffMax {
		return refundReviewBackoffMax
	}
	return d
}
