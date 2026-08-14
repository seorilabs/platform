package ads

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

const (
	claimsCollection        = "ad_reward_claims"
	claimRequestsCollection = "ad_claim_requests"
	transactionsCollection  = "ad_transaction_replays"
	usageCollection         = "ad_usage"
	policyCollection        = "ad_policy_projections"
	grantsCollection        = "ad_suppression_grants"
	revocationsCollection   = "ad_suppression_revocations"
	healthDocument          = "ad_health/current"
	appHealthCollection     = "ad_app_health"
)

type StoreRepository struct{ store *store.Client }

func NewStoreRepository(st *store.Client) *StoreRepository { return &StoreRepository{store: st} }

type claimRequestDoc struct {
	ClaimID        string    `firestore:"claimId"`
	AppID          string    `firestore:"appId"`
	PlatformUserID string    `firestore:"platformUserId"`
	PlacementID    string    `firestore:"placementId"`
	Provider       string    `firestore:"provider"`
	ClientPlatform string    `firestore:"clientPlatform"`
	Reward         Reward    `firestore:"reward"`
	CreatedAt      time.Time `firestore:"createdAt"`
}
type transactionDoc struct {
	ClaimID, AppID, Provider string
	CreatedAt                time.Time
}
type usageDoc struct {
	Count           int       `firestore:"count"`
	LastConfirmedAt time.Time `firestore:"lastConfirmedAt"`
}
type policyDoc struct {
	AppID                string    `firestore:"appId"`
	PlatformUserID       string    `firestore:"platformUserId"`
	Active               bool      `firestore:"active"`
	ActiveGrantRequestID string    `firestore:"activeGrantRequestId"`
	UpdatedAt            time.Time `firestore:"updatedAt"`
}
type healthDoc struct {
	LastSSVSuccessAt      *time.Time `firestore:"lastSsvSuccessAt,omitempty"`
	InvalidSignatureCount int64      `firestore:"invalidSignatureCount"`
	PolicyFailureCount    int64      `firestore:"policyFailureCount"`
}
type appHealthDoc struct {
	AppID                 string     `firestore:"appId"`
	LastCallbackSuccessAt *time.Time `firestore:"lastCallbackSuccessAt,omitempty"`
	LastProbeSuccessAt    *time.Time `firestore:"lastProbeSuccessAt,omitempty"`
	InvalidSignatureCount int64      `firestore:"invalidSignatureCount"`
	PolicyFailureCount    int64      `firestore:"policyFailureCount"`
}

func path(raw string) (fspath.Path, error)     { return fspath.Parse(raw) }
func claimPath(id string) (fspath.Path, error) { return path(claimsCollection + "/" + id) }
func claimRequestPath(c Claim) (fspath.Path, error) {
	return path(claimRequestsCollection + "/" + hash(c.AppID+"\x00"+c.PlatformUserID+"\x00"+c.RequestID))
}
func transactionPath(transactionHash string) (fspath.Path, error) {
	return path(transactionsCollection + "/" + transactionHash)
}
func usagePath(in ConfirmInput, placement, date string) (fspath.Path, error) {
	return path(usageCollection + "/" + hash(in.AppID+"\x00"+in.PlatformUserID+"\x00"+placement+"\x00"+date))
}
func policyPath(appID, puid string) (fspath.Path, error) {
	return path(policyCollection + "/" + hash(appID+"\x00"+puid))
}
func appHealthPath(appID string) (fspath.Path, error) {
	return path(appHealthCollection + "/" + hash(appID))
}

func (r *StoreRepository) CreateClaim(ctx context.Context, c Claim, dailyLimit, cooldownSeconds int) (Claim, error) {
	cp, err := claimPath(c.ClaimID)
	if err != nil {
		return Claim{}, err
	}
	rp, err := claimRequestPath(c)
	if err != nil {
		return Claim{}, err
	}
	result := c
	err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		exists, snap, err := tx.Exists(rp)
		if err != nil {
			return err
		}
		if exists {
			var replay claimRequestDoc
			if err := snap.DataTo(&replay); err != nil {
				return err
			}
			if replay.AppID != c.AppID || replay.PlatformUserID != c.PlatformUserID ||
				replay.PlacementID != c.PlacementID || replay.Provider != c.Provider ||
				replay.ClientPlatform != c.ClientPlatform || replay.Reward != c.Reward {
				return platformerr.New(platformerr.CodeClaimReplayMismatch, "claim 재시도 내용이 달라요")
			}
			existingPath, err := claimPath(replay.ClaimID)
			if err != nil {
				return err
			}
			existing, err := tx.Get(existingPath)
			if err != nil {
				return err
			}
			return existing.DataTo(&result)
		}
		up, err := usagePath(ConfirmInput{AppID: c.AppID, PlatformUserID: c.PlatformUserID}, c.PlacementID, c.CreatedAt.UTC().Format("2006-01-02"))
		if err != nil {
			return err
		}
		usageExists, usageSnap, err := tx.Exists(up)
		if err != nil {
			return err
		}
		if usageExists {
			var usage usageDoc
			if err := usageSnap.DataTo(&usage); err != nil {
				return err
			}
			if usage.Count >= dailyLimit {
				return platformerr.New(platformerr.CodeAdDailyLimit, "오늘 받을 수 있는 광고 보상을 모두 받았어요")
			}
			if !usage.LastConfirmedAt.IsZero() && c.CreatedAt.Sub(usage.LastConfirmedAt) < time.Duration(cooldownSeconds)*time.Second {
				return platformerr.New(platformerr.CodeAdCooldown, "잠시 후 다시 시도해 주세요")
			}
		}
		if err := tx.Create(cp, c); err != nil {
			return err
		}
		return tx.Create(rp, claimRequestDoc{
			ClaimID: c.ClaimID, AppID: c.AppID, PlatformUserID: c.PlatformUserID,
			PlacementID: c.PlacementID, Provider: c.Provider, ClientPlatform: c.ClientPlatform,
			Reward: c.Reward, CreatedAt: c.CreatedAt,
		})
	})
	if err != nil {
		return Claim{}, wrapStore(err, "보상 claim을 만들지 못했어요")
	}
	return result, nil
}

func (r *StoreRepository) GetClaim(ctx context.Context, id string) (Claim, error) {
	p, err := claimPath(id)
	if err != nil {
		return Claim{}, platformerr.New(platformerr.CodeRequestInvalid, "claim ID가 올바르지 않아요")
	}
	snap, err := r.store.Get(ctx, p)
	if errors.Is(err, store.ErrNotFound) {
		return Claim{}, platformerr.New(platformerr.CodeClaimNotFound, "보상 claim을 찾을 수 없어요")
	}
	if err != nil {
		return Claim{}, wrapStore(err, "보상 claim을 읽지 못했어요")
	}
	var claim Claim
	if err := snap.DataTo(&claim); err != nil {
		return Claim{}, wrapStore(err, "보상 claim을 읽지 못했어요")
	}
	return claim, nil
}

func (r *StoreRepository) ConfirmClaim(ctx context.Context, in ConfirmInput) (Claim, error) {
	cp, err := claimPath(in.ClaimID)
	if err != nil {
		return Claim{}, err
	}
	tp, err := transactionPath(in.TransactionHash)
	if err != nil {
		return Claim{}, err
	}
	var result Claim
	err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		claimSnap, err := tx.Get(cp)
		if errors.Is(err, store.ErrNotFound) {
			return platformerr.New(platformerr.CodeClaimNotFound, "보상 claim을 찾을 수 없어요")
		}
		if err != nil {
			return err
		}
		if err := claimSnap.DataTo(&result); err != nil {
			return err
		}
		if result.AppID != in.AppID || result.PlatformUserID != in.PlatformUserID || result.Provider != in.Provider {
			return platformerr.New(platformerr.CodeClaimOwnershipMismatch, "보상 claim 소유자가 일치하지 않아요")
		}
		if result.State == StateConfirmed || result.State == StateDelivered {
			if result.TransactionHash != in.TransactionHash || result.Assurance != in.Assurance {
				return platformerr.New(platformerr.CodeClaimReplayMismatch, "이미 다른 확인으로 처리된 claim이에요")
			}
			return nil
		}
		if result.State != StateAccepted {
			return platformerr.New(platformerr.CodeClaimStateInvalid, "확인할 수 없는 claim 상태예요")
		}
		if !in.Now.Before(result.ExpiresAt) {
			return platformerr.New(platformerr.CodeClaimExpired, "보상 claim이 만료됐어요")
		}
		replayExists, replaySnap, err := tx.Exists(tp)
		if err != nil {
			return err
		}
		if replayExists {
			var replay transactionDoc
			if err := replaySnap.DataTo(&replay); err != nil {
				return err
			}
			if replay.ClaimID != in.ClaimID {
				return platformerr.New(platformerr.CodeClaimTransactionReplayed, "이미 사용된 광고 transaction이에요")
			}
		}
		up, err := usagePath(in, result.PlacementID, in.Now.UTC().Format("2006-01-02"))
		if err != nil {
			return err
		}
		usageExists, usageSnap, err := tx.Exists(up)
		if err != nil {
			return err
		}
		usage := usageDoc{}
		if usageExists {
			if err := usageSnap.DataTo(&usage); err != nil {
				return err
			}
		}
		if usage.Count >= in.DailyLimit {
			return platformerr.New(platformerr.CodeAdDailyLimit, "오늘 받을 수 있는 광고 보상을 모두 받았어요")
		}
		if !usage.LastConfirmedAt.IsZero() && in.Now.Sub(usage.LastConfirmedAt) < time.Duration(in.CooldownSeconds)*time.Second {
			return platformerr.New(platformerr.CodeAdCooldown, "잠시 후 다시 시도해 주세요")
		}
		result.State = StateConfirmed
		result.Assurance = in.Assurance
		result.TransactionHash = in.TransactionHash
		confirmed := in.Now
		result.ConfirmedAt = &confirmed
		usage.Count++
		usage.LastConfirmedAt = in.Now
		if err := tx.Set(cp, result); err != nil {
			return err
		}
		if !replayExists {
			if err := tx.Create(tp, transactionDoc{ClaimID: in.ClaimID, AppID: in.AppID, Provider: in.Provider, CreatedAt: in.Now}); err != nil {
				return err
			}
		}
		return tx.Set(up, usage)
	})
	if err != nil {
		return Claim{}, wrapStore(err, "보상 claim을 확인하지 못했어요")
	}
	return result, nil
}

func (r *StoreRepository) AcknowledgeClaim(ctx context.Context, id, appID, puid string, now time.Time) (Claim, error) {
	p, err := claimPath(id)
	if err != nil {
		return Claim{}, err
	}
	var result Claim
	err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		snap, err := tx.Get(p)
		if errors.Is(err, store.ErrNotFound) {
			return platformerr.New(platformerr.CodeClaimNotFound, "보상 claim을 찾을 수 없어요")
		}
		if err != nil {
			return err
		}
		if err := snap.DataTo(&result); err != nil {
			return err
		}
		if result.AppID != appID || result.PlatformUserID != puid {
			return platformerr.New(platformerr.CodeClaimOwnershipMismatch, "보상 claim 소유자가 일치하지 않아요")
		}
		if result.State == StateDelivered {
			return nil
		}
		if result.State != StateConfirmed {
			return platformerr.New(platformerr.CodeClaimStateInvalid, "확인되지 않은 claim은 정산 완료할 수 없어요")
		}
		result.State = StateDelivered
		result.AcknowledgedAt = &now
		return tx.Set(p, result)
	})
	if err != nil {
		return Claim{}, wrapStore(err, "보상 정산을 기록하지 못했어요")
	}
	return result, nil
}

func (r *StoreRepository) ExpireClaim(ctx context.Context, id string, now time.Time) (Claim, error) {
	p, err := claimPath(id)
	if err != nil {
		return Claim{}, err
	}
	var result Claim
	err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		snap, err := tx.Get(p)
		if err != nil {
			return err
		}
		if err := snap.DataTo(&result); err != nil {
			return err
		}
		if result.State != StateAccepted {
			return nil
		}
		if now.Before(result.ExpiresAt) {
			return nil
		}
		result.State = StateExpired
		return tx.Set(p, result)
	})
	if err != nil {
		return Claim{}, wrapStore(err, "보상 claim 만료를 기록하지 못했어요")
	}
	return result, nil
}

func (r *StoreRepository) OperatorSuppressed(ctx context.Context, appID, puid string) (bool, error) {
	p, err := policyPath(appID, puid)
	if err != nil {
		return false, err
	}
	snap, err := r.store.Get(ctx, p)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, wrapStore(err, "광고 차단 상태를 읽지 못했어요")
	}
	var doc policyDoc
	if err := snap.DataTo(&doc); err != nil {
		return false, err
	}
	if doc.AppID != appID || doc.PlatformUserID != puid {
		return false, platformerr.New(platformerr.CodeLedgerStateInvalid, "광고 정책 projection이 올바르지 않아요")
	}
	return doc.Active, nil
}

func (r *StoreRepository) GrantSuppression(ctx context.Context, record SuppressionRecord) (SuppressionResult, error) {
	return r.suppression(ctx, record, false)
}
func (r *StoreRepository) RevokeSuppression(ctx context.Context, record SuppressionRecord) (SuppressionResult, error) {
	return r.suppression(ctx, record, true)
}
func (r *StoreRepository) suppression(ctx context.Context, record SuppressionRecord, revoke bool) (SuppressionResult, error) {
	collection := grantsCollection
	if revoke {
		collection = revocationsCollection
	}
	rp, err := path(collection + "/" + record.RequestID)
	if err != nil {
		return SuppressionResult{}, err
	}
	pp, err := policyPath(record.AppID, record.PlatformUserID)
	if err != nil {
		return SuppressionResult{}, err
	}
	var out SuppressionResult
	err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		exists, snap, err := tx.Exists(rp)
		if err != nil {
			return err
		}
		if exists {
			var existing SuppressionRecord
			if err := snap.DataTo(&existing); err != nil {
				return err
			}
			if existing.AppID != record.AppID || existing.PlatformUserID != record.PlatformUserID || existing.GrantRequestID != record.GrantRequestID || existing.Reason != record.Reason {
				return platformerr.New(platformerr.CodeOperatorReplayMismatch, "같은 requestId의 광고 차단 요청 내용이 달라요")
			}
			out = SuppressionResult{Applied: existing.Applied, RequestID: existing.RequestID, ActiveGrantRequestID: existing.GrantRequestID}
			if !revoke && existing.Applied {
				out.ActiveGrantRequestID = existing.RequestID
			}
			return nil
		}
		projectionExists, projectionSnap, err := tx.Exists(pp)
		if err != nil {
			return err
		}
		projection := policyDoc{AppID: record.AppID, PlatformUserID: record.PlatformUserID}
		if projectionExists {
			if err := projectionSnap.DataTo(&projection); err != nil {
				return err
			}
		}
		if !revoke {
			record.Applied = !projection.Active
			if projection.Active {
				record.GrantRequestID = projection.ActiveGrantRequestID
			}
			if record.Applied {
				projection.Active = true
				projection.ActiveGrantRequestID = record.RequestID
				projection.UpdatedAt = record.CreatedAt
			}
			out = SuppressionResult{Applied: record.Applied, RequestID: record.RequestID, ActiveGrantRequestID: projection.ActiveGrantRequestID}
		}
		if revoke {
			if record.GrantRequestID == "" || !projection.Active || projection.ActiveGrantRequestID != record.GrantRequestID {
				return platformerr.New(platformerr.CodeSuppressionGrantMismatch, "회수할 active 운영자 차단이 일치하지 않아요")
			}
			record.Applied = true
			projection.Active = false
			projection.UpdatedAt = record.CreatedAt
			out = SuppressionResult{Applied: true, RequestID: record.RequestID}
		}
		if err := tx.Create(rp, record); err != nil {
			return err
		}
		return tx.Set(pp, projection)
	})
	if err != nil {
		return SuppressionResult{}, wrapStore(err, "운영자 광고 차단을 변경하지 못했어요")
	}
	return out, nil
}

func (r *StoreRepository) SuppressionHistory(ctx context.Context, appID, puid string, limit int) ([]SuppressionRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	records := make([]SuppressionRecord, 0)
	for _, collection := range []string{grantsCollection, revocationsCollection} {
		p, _ := path(collection)
		iter, err := r.store.Query(ctx, p, func(q firestore.Query) firestore.Query {
			return q.Where("appId", "==", appID).Where("platformUserId", "==", puid).Limit(limit)
		})
		if err != nil {
			return nil, wrapStore(err, "광고 차단 감사 이력을 읽지 못했어요")
		}
		for {
			snap, err := iter.Next()
			if store.IsDone(err) {
				break
			}
			if err != nil {
				iter.Stop()
				return nil, wrapStore(err, "광고 차단 감사 이력을 읽지 못했어요")
			}
			var rec SuppressionRecord
			if err := snap.DataTo(&rec); err != nil {
				iter.Stop()
				return nil, err
			}
			records = append(records, rec)
		}
		iter.Stop()
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (r *StoreRepository) ListClaims(ctx context.Context, filter ClaimFilter) ([]Claim, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	p, _ := path(claimsCollection)
	iter, err := r.store.Query(ctx, p, func(q firestore.Query) firestore.Query { return q.OrderBy("createdAt", firestore.Desc).Limit(500) })
	if err != nil {
		return nil, wrapStore(err, "보상 claim을 조회하지 못했어요")
	}
	defer iter.Stop()
	out := make([]Claim, 0, limit)
	for len(out) < limit {
		snap, err := iter.Next()
		if store.IsDone(err) {
			break
		}
		if err != nil {
			return nil, wrapStore(err, "보상 claim을 조회하지 못했어요")
		}
		var c Claim
		if err := snap.DataTo(&c); err != nil {
			return nil, err
		}
		if filter.AppID != "" && c.AppID != filter.AppID || filter.Provider != "" && c.Provider != filter.Provider || filter.State != "" && string(c.State) != filter.State || filter.Assurance != "" && string(c.Assurance) != filter.Assurance || filter.Placement != "" && c.PlacementID != filter.Placement {
			continue
		}
		if filter.Reference != "" && c.ClaimID != filter.Reference && c.SupportCode != strings.ToUpper(filter.Reference) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *StoreRepository) Health(ctx context.Context, now time.Time) (Health, error) {
	p, _ := path(healthDocument)
	doc := healthDoc{}
	snap, err := r.store.Get(ctx, p)
	if err == nil {
		if err := snap.DataTo(&doc); err != nil {
			return Health{}, err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return Health{}, wrapStore(err, "광고 서비스 상태를 읽지 못했어요")
	}
	cp, _ := path(claimsCollection)
	count, err := r.store.Count(ctx, cp, func(q firestore.Query) firestore.Query {
		return q.Where("state", "==", string(StateAccepted)).Where("createdAt", "<=", now.Add(-24*time.Hour))
	})
	if err != nil {
		return Health{}, wrapStore(err, "오래된 claim을 집계하지 못했어요")
	}
	return Health{Status: "ok", LastSSVSuccessAt: doc.LastSSVSuccessAt, InvalidSignatureCount: doc.InvalidSignatureCount, StalePendingCount: count, PolicyFailureCount: doc.PolicyFailureCount, CheckedAt: now}, nil
}

func (r *StoreRepository) AppHealth(ctx context.Context, appID string, now time.Time) (AppHealth, error) {
	p, _ := appHealthPath(appID)
	doc := appHealthDoc{AppID: appID}
	snap, err := r.store.Get(ctx, p)
	if err == nil {
		if err := snap.DataTo(&doc); err != nil {
			return AppHealth{}, err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return AppHealth{}, wrapStore(err, "앱 광고 서비스 상태를 읽지 못했어요")
	}
	cp, _ := path(claimsCollection)
	count, err := r.store.Count(ctx, cp, func(q firestore.Query) firestore.Query {
		return q.Where("appId", "==", appID).
			Where("state", "==", string(StateAccepted)).
			Where("createdAt", "<=", now.Add(-24*time.Hour))
	})
	if err != nil {
		return AppHealth{}, wrapStore(err, "앱의 오래된 claim을 집계하지 못했어요")
	}
	return AppHealth{
		AppID: appID, Status: "ok", LastCallbackSuccessAt: doc.LastCallbackSuccessAt,
		LastProbeSuccessAt: doc.LastProbeSuccessAt, InvalidSignatureCount: doc.InvalidSignatureCount,
		StalePendingCount: count, PolicyFailureCount: doc.PolicyFailureCount, CheckedAt: now,
	}, nil
}

func (r *StoreRepository) RecordSSVResult(ctx context.Context, appID string, event SSVEvent, now time.Time) error {
	p, _ := path(healthDocument)
	data := map[string]any{}
	appData := map[string]any{"appId": appID}
	switch event {
	case SSVCallbackSuccess:
		data["lastSsvSuccessAt"] = now
		appData["lastCallbackSuccessAt"] = now
	case SSVProbeSuccess:
		data["lastSsvSuccessAt"] = now
		appData["lastProbeSuccessAt"] = now
	case SSVSignatureInvalid:
		data["invalidSignatureCount"] = firestore.Increment(1)
		appData["invalidSignatureCount"] = firestore.Increment(1)
	default:
		return platformerr.New(platformerr.CodeInternal, "알 수 없는 SSV 상태예요")
	}
	if err := r.store.Set(ctx, p, data, firestore.MergeAll); err != nil {
		return err
	}
	appPath, _ := appHealthPath(appID)
	return r.store.Set(ctx, appPath, appData, firestore.MergeAll)
}
func (r *StoreRepository) RecordPolicyFailure(ctx context.Context, appID string) error {
	p, _ := path(healthDocument)
	if err := r.store.Set(ctx, p, map[string]any{"policyFailureCount": firestore.Increment(1)}, firestore.MergeAll); err != nil {
		return err
	}
	appPath, _ := appHealthPath(appID)
	return r.store.Set(ctx, appPath, map[string]any{
		"appId": appID, "policyFailureCount": firestore.Increment(1),
	}, firestore.MergeAll)
}

func wrapStore(err error, message string) error {
	var pErr *platformerr.Error
	if errors.As(err, &pErr) {
		return err
	}
	return platformerr.Wrap(err, platformerr.CodeInternal, message)
}
