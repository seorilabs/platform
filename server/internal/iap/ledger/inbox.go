package ledger

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode"

	"cloud.google.com/go/firestore"
	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
	"google.golang.org/api/iterator"
)

type InboxReward struct {
	Kind          string `json:"kind" firestore:"kind"`
	EntitlementID string `json:"entitlementId" firestore:"entitlementId"`
	Quantity      int    `json:"quantity" firestore:"quantity"`
}
type InboxMessage struct {
	ID        string        `json:"id" firestore:"id"`
	Title     string        `json:"title" firestore:"title"`
	Body      string        `json:"body" firestore:"body"`
	Rewards   []InboxReward `json:"rewards" firestore:"rewards"`
	IssuedAt  int64         `json:"issuedAt" firestore:"issuedAt"`
	ExpiresAt int64         `json:"expiresAt" firestore:"expiresAt"`
	ReadAt    int64         `json:"readAt" firestore:"readAt"`
	ClaimedAt int64         `json:"claimedAt" firestore:"claimedAt"`
}
type InboxIssue struct {
	RequestID      string        `json:"requestId" firestore:"requestId"`
	PlatformUserID string        `json:"platformUserId" firestore:"platformUserId"`
	Title          string        `json:"title" firestore:"title"`
	Body           string        `json:"body" firestore:"body"`
	Rewards        []InboxReward `json:"rewards" firestore:"rewards"`
	ExpiresAt      int64         `json:"expiresAt" firestore:"expiresAt"`
	Reason         string        `json:"reason" firestore:"reason"`
	Actor          string        `json:"-" firestore:"actor"`
}
type inboxIssueDoc struct {
	AppID   string       `firestore:"appId"`
	Input   InboxIssue   `firestore:"input"`
	Message InboxMessage `firestore:"message"`
}
type InboxPage struct {
	Messages   []InboxMessage `json:"messages"`
	NextCursor string         `json:"nextCursor"`
}

func inboxError(code platformerr.Code, message string) error { return platformerr.New(code, message) }
func cleanInboxText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len([]rune(s)) <= max && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' })
}
func (in InboxIssue) Validate(now time.Time) error {
	if !operatorRequestIDPattern.MatchString(in.RequestID) || !operatorPUIDPattern.MatchString(in.PlatformUserID) || !operatorActorPattern.MatchString(in.Actor) || !ValidAdminMutationReason(in.Reason) || !cleanInboxText(in.Title, 120) || !cleanInboxText(in.Body, 4000) || len(in.Rewards) > 10 || in.ExpiresAt < 0 {
		return inboxError(platformerr.CodeRequestInvalid, "우편 발행 내용이 올바르지 않아요")
	}
	seen := map[string]bool{}
	for _, reward := range in.Rewards {
		if reward.Kind != "entitlement" || reward.Quantity != 1 || !operatorEntitlementPattern.MatchString(reward.EntitlementID) || seen[reward.EntitlementID] {
			return inboxError(platformerr.CodeRequestInvalid, "지원하지 않는 보상 또는 중복 보상이에요")
		}
		seen[reward.EntitlementID] = true
	}
	return nil
}
func (l *Ledger) inboxCollection(puid string) (fspath.Path, error) {
	if !operatorAppIDPattern.MatchString(l.appID) || !operatorPUIDPattern.MatchString(puid) {
		return fspath.Path{}, inboxError(platformerr.CodeRequestInvalid, "우편 대상이 올바르지 않아요")
	}
	// 앱 범위가 없는 legacy 원장도 우편만큼은 항상 앱/사용자 아래에 격리한다.
	return l.paths.parse("inbox_apps/" + l.appID + "/users/" + puid + "/messages")
}
func (l *Ledger) inboxPath(puid, id string) (fspath.Path, error) {
	if !operatorRequestIDPattern.MatchString(id) {
		return fspath.Path{}, inboxError(platformerr.CodeRequestInvalid, "우편 식별자가 올바르지 않아요")
	}
	p, e := l.inboxCollection(puid)
	if e != nil {
		return p, e
	}
	return fspath.Parse(p.String() + "/" + id)
}
func (l *Ledger) inboxRecord(collection, puid, id string) (fspath.Path, error) {
	if _, err := l.inboxPath(puid, id); err != nil {
		return fspath.Path{}, err
	}
	return l.paths.parse(collection + "/" + economyDigest(l.appID+"\x00"+puid+"\x00"+id))
}
func (l *Ledger) IssueInbox(ctx context.Context, in InboxIssue) (InboxMessage, error) {
	if err := in.Validate(l.now()); err != nil {
		return InboxMessage{}, err
	}
	if in.Rewards == nil {
		in.Rewards = []InboxReward{}
	}
	p, err := l.inboxPath(in.PlatformUserID, in.RequestID)
	if err != nil {
		return InboxMessage{}, err
	}
	// requestId는 앱 전체에서 유일하다. 타 사용자로 재발행하면 exact replay가 아니다.
	audit, err := l.paths.parse("inbox_issues/" + economyDigest(l.appID+"\x00"+in.RequestID))
	if err != nil {
		return InboxMessage{}, err
	}
	var result InboxMessage
	err = l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		result = InboxMessage{}
		exists, snap, e := tx.Exists(audit)
		if e != nil {
			return e
		}
		if exists {
			var prev inboxIssueDoc
			if e = snap.DataTo(&prev); e != nil {
				return e
			}
			if prev.AppID != l.appID || !reflect.DeepEqual(prev.Input, in) {
				return inboxError(platformerr.CodeOperatorReplayMismatch, "이 requestId는 다른 발행에 사용됐어요")
			}
			result = prev.Message
			return nil
		}
		now := l.now().Unix()
		if in.ExpiresAt != 0 && in.ExpiresAt <= now {
			return inboxError(platformerr.CodeRequestInvalid, "만료 시각이 지났어요")
		}
		result = InboxMessage{ID: in.RequestID, Title: in.Title, Body: in.Body, Rewards: in.Rewards, IssuedAt: now, ExpiresAt: in.ExpiresAt}
		if e = tx.Create(p, result); e != nil {
			return e
		}
		return tx.Create(audit, inboxIssueDoc{AppID: l.appID, Input: in, Message: result})
	})
	return result, err
}
func (l *Ledger) ListInbox(ctx context.Context, puid, cursor string) (InboxPage, error) {
	p, e := l.inboxCollection(puid)
	if e != nil {
		return InboxPage{}, e
	}
	if cursor != "" && !operatorRequestIDPattern.MatchString(cursor) {
		return InboxPage{}, inboxError(platformerr.CodeRequestInvalid, "cursor가 올바르지 않아요")
	}
	it, e := l.store.Query(ctx, p, func(q firestore.Query) firestore.Query {
		q = q.OrderBy(firestore.DocumentID, firestore.Asc).Limit(51)
		if cursor != "" {
			q = q.StartAfter(cursor)
		}
		return q
	})
	if e != nil {
		return InboxPage{}, e
	}
	defer it.Stop()
	out := InboxPage{Messages: []InboxMessage{}}
	for {
		snap, e := it.Next()
		if errors.Is(e, iterator.Done) {
			break
		}
		if e != nil {
			return out, e
		}
		if len(out.Messages) == 50 {
			out.NextCursor = out.Messages[49].ID
			break
		}
		var message InboxMessage
		if e = snap.DataTo(&message); e != nil {
			return out, e
		}
		out.Messages = append(out.Messages, message)
	}
	return out, nil
}
func (l *Ledger) ReadInbox(ctx context.Context, puid, id string) (InboxMessage, error) {
	return l.mutateInbox(ctx, puid, id, false)
}
func (l *Ledger) ClaimInbox(ctx context.Context, puid, id string) (InboxMessage, error) {
	return l.mutateInbox(ctx, puid, id, true)
}
func (l *Ledger) mutateInbox(ctx context.Context, puid, id string, claim bool) (InboxMessage, error) {
	p, e := l.inboxPath(puid, id)
	if e != nil {
		return InboxMessage{}, e
	}
	receipt, e := l.inboxRecord("inbox_claims", puid, id)
	if e != nil {
		return InboxMessage{}, e
	}
	var message InboxMessage
	e = l.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		message = InboxMessage{}
		exists, snap, e := tx.Exists(p)
		if e != nil {
			return e
		}
		if !exists {
			return inboxError(platformerr.CodePurchaseNotFound, "우편을 찾을 수 없어요")
		}
		if e = snap.DataTo(&message); e != nil {
			return e
		}
		now := l.now().UTC()
		if !claim {
			if message.ReadAt == 0 {
				message.ReadAt = now.Unix()
				return tx.Set(p, message)
			}
			return nil
		}
		// 이미 커밋한 요청은 만료 후 재요청에도 성공한다. 응답 유실은 지급 취소가 아니다.
		if message.ClaimedAt != 0 {
			return nil
		}
		if message.ExpiresAt != 0 && message.ExpiresAt <= now.Unix() {
			return inboxError(platformerr.CodeRequestInvalid, "우편이 만료됐어요")
		}
		ents := make([]entitlementDoc, 0, len(message.Rewards))
		for _, reward := range message.Rewards {
			if reward.Kind != "entitlement" || reward.Quantity != 1 {
				return inboxError(platformerr.CodeLedgerStateInvalid, "우편 보상 계약이 올바르지 않아요")
			}
			ep, e := l.paths.internalEntitlement(puid, reward.EntitlementID)
			if e != nil {
				return e
			}
			ent, e := l.readEntitlement(tx, ep, reward.EntitlementID)
			if e != nil {
				return e
			}
			ents = append(ents, ent)
		}
		// Firestore는 모든 read가 write보다 먼저 와야 한다. 보상별 read를 위에서 모은다.
		for _, ent := range ents {
			key := economyDigest("inbox\x00" + l.appID + "\x00" + puid + "\x00" + id + "\x00" + ent.EntitlementID)
			ent.Sources[key] = domain.Source{Platform: domain.PlatformOperator, ProductID: ent.EntitlementID, State: domain.StateActive, PurchasedAt: now, ObservedAt: now, UpdatedAt: now}
			if e = l.writeEntitlement(tx, puid, ent, now); e != nil {
				return e
			}
		}
		message.ClaimedAt = now.Unix()
		if e = tx.Create(receipt, message); e != nil {
			return e
		}
		return tx.Set(p, message)
	})
	return message, e
}
