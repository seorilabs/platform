package identity

import (
	"context"
	"errors"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/store"
)

const deletionCollection = "account_deletions"
const deletionReceipts = "account_deletion_receipts"
const deletionUsers = "account_deletion_users"

// 삭제 대상 식별자는 처리에 필요한 동안만 남긴다. 완료 시 제거하고
// 상태 확인용 해시 접수증과 토큰 재사용 방지 표시는 30일 후 정리한다.
type DeletionJob struct {
	UserMarkerHash            string     `firestore:"userMarkerHash,omitempty"`
	IdentityDeletedAt         time.Time  `firestore:"identityDeletedAt,omitempty"`
	GoogleDeletionRequestedAt *time.Time `firestore:"googleDeletionRequestedAt,omitempty"`
	FirebaseProjectID         string     `firestore:"firebaseProjectId"`
	GA4PropertyID             string     `firestore:"ga4PropertyId"`
	ServiceAccount            string     `firestore:"serviceAccount"`
	ID                        string     `firestore:"-"`
	AppID                     string     `firestore:"appId"`
	UID                       string     `firestore:"uid,omitempty"`
	PlatformUserID            string     `firestore:"platformUserId,omitempty"`
	ReceiptHash               string     `firestore:"receiptHash"`
	State                     string     `firestore:"state"`
	Step                      int        `firestore:"step"`
	RequestedAt               time.Time  `firestore:"requestedAt"`
	CompletedAt               *time.Time `firestore:"completedAt,omitempty"`
	GoogleAnalyticsDeletion   string     `firestore:"googleAnalyticsDeletion"`
	NextAttemptAt             time.Time  `firestore:"nextAttemptAt"`
	Lease                     string     `firestore:"lease,omitempty"`
	LeaseUntil                time.Time  `firestore:"leaseUntil"`
	ExpiresAt                 time.Time  `firestore:"expiresAt,omitempty"`
}
type deletionReceiptDoc struct {
	JobID     string    `firestore:"jobId"`
	ExpiresAt time.Time `firestore:"expiresAt,omitempty"`
}

func deletionPath(appID, uid string) fspath.Path {
	return deletionStorePath(deletionCollection + "/" + hashHex(appID+"\x00"+uid))
}
func deletionJobPath(id string) fspath.Path { return deletionStorePath(deletionCollection + "/" + id) }
func deletionReceiptPath(appID, receipt string) fspath.Path {
	return deletionStorePath(deletionReceipts + "/" + hashHex(appID+"\x00"+receipt))
}
func deletionUserPath(appID, puid string) fspath.Path {
	return deletionStorePath(deletionUsers + "/" + hashHex(appID+"\x00"+puid))
}
func (j DeletionJob) status() DeletionStatus {
	return DeletionStatus{j.State, j.RequestedAt, j.CompletedAt, j.GoogleAnalyticsDeletion}
}
func deletionDenied() error {
	return platformerr.New(platformerr.CodeAuthForbidden, "삭제를 요청한 계정이에요")
}

func (r *StoreRepository) AccountDeleting(ctx context.Context, appID, uid string) (bool, error) {
	snap, err := r.store.Get(ctx, deletionPath(appID, uid))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var job DeletionJob
	if err = snap.DataTo(&job); err != nil {
		return false, err
	}
	return job.ExpiresAt.IsZero() || r.now().Before(job.ExpiresAt), nil
}
func (r *StoreRepository) checkDeletionTx(tx *store.Tx, appID, uid string) error {
	exists, snap, err := tx.Exists(deletionPath(appID, uid))
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var j DeletionJob
	if err = snap.DataTo(&j); err != nil {
		return err
	}
	if j.ExpiresAt.IsZero() || r.now().Before(j.ExpiresAt) {
		return deletionDenied()
	}
	return nil
}

// CheckAccountActive는 광고 쓰기와 같은 트랜잭션에서 검사한다.
// 접수 뒤 늦게 도착한 SSV나 광고 요청이 데이터를 다시 만들지 못하게 한다.
func (r *StoreRepository) CheckAccountActive(tx *store.Tx, appID, puid string) error {
	exists, snap, err := tx.Exists(deletionUserPath(appID, puid))
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var marker deletionReceiptDoc
	if err = snap.DataTo(&marker); err != nil {
		return err
	}
	if marker.ExpiresAt.IsZero() || r.now().Before(marker.ExpiresAt) {
		return deletionDenied()
	}
	return nil
}
func (r *StoreRepository) BeginDeletion(ctx context.Context, app registry.App, uid, receipt string) (DeletionStatus, error) {
	appID := app.AppID
	p := deletionPath(appID, uid)
	rp := deletionReceiptPath(appID, receipt)
	idPath, err := identityPath(appID, uid)
	if err != nil {
		return DeletionStatus{}, err
	}
	var job DeletionJob
	err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		exists, snap, err := tx.Exists(p)
		if err != nil {
			return err
		}
		if exists {
			if err = snap.DataTo(&job); err != nil {
				return err
			}
			if job.ReceiptHash != hashHex(appID+"\x00"+receipt) {
				return platformerr.New(platformerr.CodeRequestInvalid, "기존 삭제 접수증으로 상태를 확인해 주세요")
			}
			return nil
		}
		receiptExists, _, err := tx.Exists(rp)
		if err != nil {
			return err
		}
		if receiptExists {
			return platformerr.New(platformerr.CodeRequestInvalid, "사용할 수 없는 접수증이에요")
		}
		exists, snap, err = tx.Exists(idPath)
		if err != nil {
			return err
		}
		puid := ""
		if exists {
			var mapping identityDoc
			if err = snap.DataTo(&mapping); err != nil {
				return err
			}
			puid = mapping.PlatformUserID
		}
		job = DeletionJob{UserMarkerHash: hashHex(appID + "\x00" + puid), FirebaseProjectID: app.FirebaseProjectID, GA4PropertyID: app.GA4.PropertyID, ServiceAccount: app.FirebaseCustomTokenServiceAccount, AppID: appID, UID: uid, PlatformUserID: puid, ReceiptHash: hashHex(appID + "\x00" + receipt), State: "processing", RequestedAt: r.now().UTC(), NextAttemptAt: r.now(), GoogleAnalyticsDeletion: "not_requested"}
		id := hashHex(appID + "\x00" + uid)
		if err = tx.Set(p, job); err != nil {
			return err
		}
		if err = tx.Set(rp, deletionReceiptDoc{JobID: id}); err != nil {
			return err
		}
		if puid != "" {
			return tx.Set(deletionUserPath(appID, puid), deletionReceiptDoc{JobID: id})
		}
		return nil
	})
	return job.status(), err
}
func (r *StoreRepository) DeletionStatus(ctx context.Context, appID, receipt string) (DeletionStatus, error) {
	snap, err := r.store.Get(ctx, deletionReceiptPath(appID, receipt))
	if errors.Is(err, store.ErrNotFound) {
		return DeletionStatus{}, deletionDenied()
	}
	if err != nil {
		return DeletionStatus{}, err
	}
	var record deletionReceiptDoc
	if err = snap.DataTo(&record); err != nil {
		return DeletionStatus{}, err
	}
	if !record.ExpiresAt.IsZero() && !r.now().Before(record.ExpiresAt) {
		return DeletionStatus{}, deletionDenied()
	}
	snap, err = r.store.Get(ctx, deletionJobPath(record.JobID))
	if err != nil {
		return DeletionStatus{}, err
	}
	var j DeletionJob
	if err = snap.DataTo(&j); err != nil {
		return DeletionStatus{}, err
	}
	if j.AppID != appID {
		return DeletionStatus{}, deletionDenied()
	}
	return j.status(), nil
}

func (r *StoreRepository) ClaimDeletions(ctx context.Context, limit int) ([]DeletionJob, error) {
	iter, err := r.store.Query(ctx, deletionStorePath(deletionCollection), func(q firestore.Query) firestore.Query {
		return q.Where("nextAttemptAt", "<=", r.now()).OrderBy("nextAttemptAt", firestore.Asc).Limit(limit)
	})
	if err != nil {
		return nil, err
	}
	defer iter.Stop()
	jobs := []DeletionJob{}
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		id := snap.Ref.ID
		lease, err := NewRefreshToken()
		if err != nil {
			return nil, err
		}
		claimed := false
		var j DeletionJob
		err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
			current, err := tx.Get(deletionJobPath(id))
			if err != nil {
				return err
			}
			if err = current.DataTo(&j); err != nil {
				return err
			}
			claimed = false
			if j.State == "completed" && !j.ExpiresAt.IsZero() && !r.now().Before(j.ExpiresAt) {
				for _, p := range []fspath.Path{deletionStorePath(deletionReceipts + "/" + j.ReceiptHash), deletionStorePath(deletionUsers + "/" + j.UserMarkerHash), deletionJobPath(id)} {
					if err = tx.Delete(p); err != nil {
						return err
					}
				}
				return nil
			}
			if j.State != "processing" || j.LeaseUntil.After(r.now()) || j.NextAttemptAt.After(r.now()) {
				return nil
			}
			j.Lease = lease
			j.LeaseUntil = r.now().Add(2 * time.Minute)
			j.NextAttemptAt = j.LeaseUntil
			claimed = true
			return tx.Set(deletionJobPath(id), j)
		})
		if err != nil {
			return nil, err
		}
		if claimed {
			j.ID = id
			jobs = append(jobs, j)
		}
	}
	return jobs, nil
}

// AdvanceDeletion은 lease 소유자만 진행 상태를 쓸 수 있다.
// 만료된 워커 응답이 새 시도를 되돌리지 않도록 외부 삭제도 멱등 처리한다.
func (r *StoreRepository) AdvanceDeletion(ctx context.Context, j DeletionJob, success bool) error {
	return r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		p := deletionJobPath(j.ID)
		snap, err := tx.Get(p)
		if err != nil {
			return err
		}
		var current DeletionJob
		if err = snap.DataTo(&current); err != nil {
			return err
		}
		if current.Lease != j.Lease || !current.LeaseUntil.After(r.now()) {
			return errors.New("identity: deletion lease expired")
		}
		current.Lease = ""
		current.LeaseUntil = time.Time{}
		current.NextAttemptAt = r.now().Add(5 * time.Minute)
		if success {
			current.Step++
			if current.Step == 1 {
				current.IdentityDeletedAt = r.now().UTC()
			}
			current.GoogleDeletionRequestedAt = j.GoogleDeletionRequestedAt
			current.NextAttemptAt = r.now()
			if current.Step == 3 {
				current.NextAttemptAt = current.IdentityDeletedAt.Add(4 * 24 * time.Hour)
			}
			current.GoogleAnalyticsDeletion = j.GoogleAnalyticsDeletion
		}
		if current.Step == 4 {
			now := r.now().UTC()
			expiry := now.Add(30 * 24 * time.Hour)
			current.State = "completed"
			current.CompletedAt = &now
			current.ExpiresAt = expiry
			current.NextAttemptAt = expiry
			receiptPath := deletionStorePath(deletionReceipts + "/" + current.ReceiptHash)
			if err = tx.Set(receiptPath, deletionReceiptDoc{JobID: j.ID, ExpiresAt: expiry}); err != nil {
				return err
			}
			if current.PlatformUserID != "" {
				if err = tx.Set(deletionUserPath(current.AppID, current.PlatformUserID), deletionReceiptDoc{JobID: j.ID, ExpiresAt: expiry}); err != nil {
					return err
				}
			}
			current.UID = ""
			current.PlatformUserID = ""
		}
		return tx.Set(p, current)
	})
}

// 정적 이름과 서버 해시로만 호출한다. 사용자 입력을 경로에 넣지 않는다.
func deletionStorePath(raw string) fspath.Path {
	p, err := fspath.Parse(raw)
	if err != nil {
		panic(err)
	}
	return p
}
