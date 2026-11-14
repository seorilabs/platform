package identity

import (
	"context"
	"time"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

const reviewAttemptsPerMinute = 120

type reviewLimitDoc struct {
	Window int64     `firestore:"window"`
	Count  int       `firestore:"count"`
	TTLAt  time.Time `firestore:"ttlAt"`
}
type reviewUIDDoc struct {
	AppID string `firestore:"appId"`
	UID   string `firestore:"uid"`
}

func reviewPath(collection, appID string) (fspath.Path, error) {
	return fspath.Parse(collection + "/" + hashHex(appID))
}

func (r *StoreRepository) AllowReviewAttempt(ctx context.Context, appID string, now time.Time) (bool, error) {
	p, err := reviewPath("auth_review_limits", appID)
	if err != nil {
		return false, err
	}
	allowed := false
	err = r.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		allowed = false
		exists, snap, err := tx.Exists(p)
		if err != nil {
			return err
		}
		d := reviewLimitDoc{Window: now.Unix() / 60, TTLAt: now.Add(2 * time.Minute)}
		if exists {
			var old reviewLimitDoc
			if err = snap.DataTo(&old); err != nil {
				return err
			}
			if old.Window == d.Window {
				d.Count = old.Count
			}
		}
		if d.Count >= reviewAttemptsPerMinute {
			return nil
		}
		d.Count++
		allowed = true
		return tx.Set(p, d)
	})
	return allowed, err
}

func (r *StoreRepository) ResolveReviewUID(ctx context.Context, appID, candidate string) (string, error) {
	if !isReviewUID(appID, candidate) {
		return "", platformerr.New(platformerr.CodeRequestInvalid, "체험 계정 정보가 올바르지 않아요")
	}
	p, err := reviewPath("auth_review_identities", appID)
	if err != nil {
		return "", err
	}
	uid := ""
	err = r.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		uid = candidate
		exists, snap, err := tx.Exists(p)
		if err != nil {
			return err
		}
		if exists {
			var d reviewUIDDoc
			if err = snap.DataTo(&d); err != nil {
				return err
			}
			if d.AppID != appID || !isReviewUID(appID, d.UID) {
				return platformerr.New(platformerr.CodeLedgerStateInvalid, "체험 계정 기록이 올바르지 않아요")
			}
			// Only a deletion marker rotates the demo identity. An ordinary operator
			// block is never evaded by creating a replacement account.
			deleting, _, err := tx.Exists(deletionPath(appID, d.UID))
			if err != nil {
				return err
			}
			if !deleting {
				uid = d.UID
				return nil
			}
		}
		return tx.Set(p, reviewUIDDoc{AppID: appID, UID: candidate})
	})
	return uid, err
}
