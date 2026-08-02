package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

// Firestore 경로.
//
//	identities/{app_id}__{firebase_uid}  → platform_user_id 매핑
//	users/{platform_user_id}             → 사용자 문서
//	refresh_tokens/{sha256(token)}       → 갱신 토큰
//
// identities 문서 ID를 복합키로 만드는 이유는 쿼리가 아니라 직접 읽기로
// 끝내기 위해서다. 인덱스가 필요 없고 읽기 1회로 해결된다.
const (
	identitiesCollection = "identities"
	usersCollection      = "users"
	refreshCollection    = "refresh_tokens"
)

type identityDoc struct {
	PlatformUserID string    `firestore:"platformUserId"`
	AppID          string    `firestore:"appId"`
	AppUserID      string    `firestore:"appUserId"`
	Anonymous      bool      `firestore:"anonymous"`
	FirstSeenAt    time.Time `firestore:"firstSeenAt"`
	LastSeenAt     time.Time `firestore:"lastSeenAt"`
}

type userDoc struct {
	AppID       string    `firestore:"appId"`
	AppUserID   string    `firestore:"appUserId"`
	Anonymous   bool      `firestore:"anonymous"`
	CreatedAt   time.Time `firestore:"createdAt"`
	LastSeenAt  time.Time `firestore:"lastSeenAt"`
	SupportCode string    `firestore:"supportCode"`
}

// SupportUser는 Admin API에 노출해도 되는 PII 없는 사용자 요약이다.
// AppUserID는 Firebase UID일 수 있으므로 의도적으로 포함하지 않는다.
type SupportUser struct {
	PlatformUserID string    `json:"platformUserId"`
	AppID          string    `json:"appId"`
	SupportCode    string    `json:"supportCode"`
	IsAnonymous    bool      `json:"isAnonymous"`
	CreatedAt      time.Time `json:"createdAt"`
	LastSeenAt     time.Time `json:"lastSeenAt"`
}

type refreshDoc struct {
	PlatformUserID string    `firestore:"platformUserId"`
	AppID          string    `firestore:"appId"`
	AppUserID      string    `firestore:"appUserId"`
	Anonymous      bool      `firestore:"anonymous"`
	ExpiresAt      time.Time `firestore:"expiresAt"`
	CreatedAt      time.Time `firestore:"createdAt"`
}

// StoreRepository는 Firestore 기반 identity 저장소다.
type StoreRepository struct {
	store *store.Client
	now   func() time.Time
}

// NewStoreRepository는 저장소를 만든다.
func NewStoreRepository(s *store.Client) *StoreRepository {
	return &StoreRepository{store: s, now: time.Now}
}

// WithClock은 시계를 주입한다. 테스트용이다.
func (r *StoreRepository) WithClock(now func() time.Time) *StoreRepository {
	r.now = now
	return r
}

func identityPath(appID, uid string) (fspath.Path, error) {
	// 복합키에 슬래시가 섞이면 경로 구조가 깨진다.
	// fspath가 세그먼트 문자를 검사하므로 여기서 따로 막지 않아도
	// Parse가 거부한다. 다만 uid는 외부 입력이라 해시로 안전하게 만든다.
	return fspath.Parse(identitiesCollection + "/" + appID + "__" + hashHex(uid))
}

func userPath(puid string) (fspath.Path, error) {
	return fspath.Parse(usersCollection + "/" + puid)
}

func refreshPath(token string) (fspath.Path, error) {
	// 갱신 토큰 원문을 저장하지 않는다. 유출 시 그대로 쓸 수 있게 된다.
	return fspath.Parse(refreshCollection + "/" + hashHex(token))
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// EnsureUser는 (appID, uid)에 대응하는 platform_user_id를 돌려준다.
//
// 동시 호출에도 하나만 만든다. 트랜잭션 안에서 존재를 확인하고
// 없을 때만 만든다. Firestore가 충돌 시 트랜잭션을 다시 실행하므로
// 두 번째 시도에서는 이미 있는 걸 발견한다.
//
// 여러 개가 만들어지면 같은 사람의 결제 원장이 갈라진다.
func (r *StoreRepository) EnsureUser(
	ctx context.Context,
	appID, uid string,
	anonymous bool,
) (string, error) {
	idPath, err := identityPath(appID, uid)
	if err != nil {
		return "", platformerr.Wrap(err, platformerr.CodeInternal, "사용자를 확인하지 못했어요")
	}

	var result string

	err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		now := r.now()

		exists, snap, err := tx.Exists(idPath)
		if err != nil {
			return err
		}

		if exists {
			var doc identityDoc
			if err := snap.DataTo(&doc); err != nil {
				return fmt.Errorf("identity: 문서 변환 실패: %w", err)
			}
			if doc.PlatformUserID == "" {
				return errors.New("identity: 매핑에 platform_user_id가 없다")
			}
			result = doc.PlatformUserID

			uPath, err := userPath(result)
			if err != nil {
				return err
			}
			uExists, uSnap, err := tx.Exists(uPath)
			if err != nil {
				return err
			}
			if !uExists {
				return errors.New("identity: 사용자 문서가 없다")
			}
			var user userDoc
			if err := uSnap.DataTo(&user); err != nil {
				return fmt.Errorf("identity: 사용자 문서 변환 실패: %w", err)
			}

			// identity 매핑과 PII 없는 운영 조회 문서의 lastSeenAt을 같은
			// 트랜잭션에서 갱신한다.
			doc.LastSeenAt = now
			user.LastSeenAt = now
			if err := tx.Set(idPath, doc); err != nil {
				return err
			}
			return tx.Set(uPath, user)
		}

		puid, err := NewPlatformUserID()
		if err != nil {
			return err
		}
		result = puid

		uPath, err := userPath(puid)
		if err != nil {
			return err
		}

		if err := tx.Set(idPath, identityDoc{
			PlatformUserID: puid,
			AppID:          appID,
			AppUserID:      uid,
			Anonymous:      anonymous,
			FirstSeenAt:    now,
			LastSeenAt:     now,
		}); err != nil {
			return err
		}

		return tx.Set(uPath, userDoc{
			AppID:       appID,
			AppUserID:   uid,
			Anonymous:   anonymous,
			CreatedAt:   now,
			LastSeenAt:  now,
			SupportCode: SupportCode(supportPrefix(appID), puid),
		})
	})
	if err != nil {
		return "", platformerr.Wrap(err, platformerr.CodeInternal, "사용자를 확인하지 못했어요")
	}
	return result, nil
}

// LookupSupportUser는 platformUserId 또는 정확한 supportCode로 사용자를
// 찾는다. 이메일·이름·전화번호·Firebase UID는 반환하지 않는다.
func (r *StoreRepository) LookupSupportUser(ctx context.Context, reference string) (SupportUser, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" || len(reference) > 64 {
		return SupportUser{}, platformerr.New(platformerr.CodeRequestInvalid,
			"사용자 조회 값이 올바르지 않아요")
	}

	if strings.HasPrefix(reference, "pu_") {
		p, err := userPath(reference)
		if err != nil {
			return SupportUser{}, platformerr.New(platformerr.CodeRequestInvalid,
				"사용자 조회 값이 올바르지 않아요")
		}
		snap, err := r.store.Get(ctx, p)
		if errors.Is(err, store.ErrNotFound) {
			return SupportUser{}, platformerr.New(platformerr.CodeUserNotFound,
				"사용자를 찾을 수 없어요")
		}
		if err != nil {
			return SupportUser{}, platformerr.Wrap(err, platformerr.CodeInternal,
				"사용자를 조회하지 못했어요")
		}
		return supportUserFromSnapshot(reference, snap)
	}

	code := strings.ToUpper(reference)
	col, err := fspath.Parse(usersCollection)
	if err != nil {
		return SupportUser{}, platformerr.Wrap(err, platformerr.CodeInternal,
			"사용자를 조회하지 못했어요")
	}
	iter, err := r.store.Query(ctx, col, func(q firestore.Query) firestore.Query {
		return q.Where("supportCode", "==", code).Limit(2)
	})
	if err != nil {
		return SupportUser{}, platformerr.Wrap(err, platformerr.CodeInternal,
			"사용자를 조회하지 못했어요")
	}
	defer iter.Stop()

	first, err := iter.Next()
	if store.IsDone(err) {
		return SupportUser{}, platformerr.New(platformerr.CodeUserNotFound,
			"사용자를 찾을 수 없어요")
	}
	if err != nil {
		return SupportUser{}, platformerr.Wrap(err, platformerr.CodeInternal,
			"사용자를 조회하지 못했어요")
	}
	if _, err := iter.Next(); !store.IsDone(err) {
		if err != nil {
			return SupportUser{}, platformerr.Wrap(err, platformerr.CodeInternal,
				"사용자를 조회하지 못했어요")
		}
		return SupportUser{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"지원 코드가 여러 사용자와 연결돼 있어요")
	}
	return supportUserFromSnapshot(first.Ref.ID, first)
}

func supportUserFromSnapshot(puid string, snap *firestore.DocumentSnapshot) (SupportUser, error) {
	var doc userDoc
	if err := snap.DataTo(&doc); err != nil {
		return SupportUser{}, platformerr.Wrap(err, platformerr.CodeInternal,
			"사용자를 조회하지 못했어요")
	}
	if puid == "" || doc.AppID == "" || doc.SupportCode == "" {
		return SupportUser{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"사용자 문서가 올바르지 않아요")
	}
	return SupportUser{
		PlatformUserID: puid,
		AppID:          doc.AppID,
		SupportCode:    doc.SupportCode,
		IsAnonymous:    doc.Anonymous,
		CreatedAt:      doc.CreatedAt,
		LastSeenAt:     doc.LastSeenAt,
	}, nil
}

// supportPrefix는 app_id에서 지원 코드 접두사를 만든다.
// lizard-tycoon → LT
func supportPrefix(appID string) string {
	var out []rune
	take := true
	for _, r := range appID {
		if r == '-' || r == '_' {
			take = true
			continue
		}
		if take && r >= 'a' && r <= 'z' {
			out = append(out, r-32) // 대문자로
			take = false
		}
	}
	if len(out) == 0 {
		return "XX"
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return string(out)
}

func (r *StoreRepository) SaveRefresh(
	ctx context.Context,
	token string,
	sess Session,
	expiresAt time.Time,
) error {
	p, err := refreshPath(token)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "세션을 저장하지 못했어요")
	}

	err = r.store.Set(ctx, p, refreshDoc{
		PlatformUserID: sess.PlatformUserID,
		AppID:          sess.AppID,
		AppUserID:      sess.AppUserID,
		Anonymous:      sess.IsAnonymous,
		ExpiresAt:      expiresAt,
		CreatedAt:      r.now(),
	})
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "세션을 저장하지 못했어요")
	}
	return nil
}

func (r *StoreRepository) LoadRefresh(ctx context.Context, token string) (Session, error) {
	p, err := refreshPath(token)
	if err != nil {
		return Session{}, platformerr.New(platformerr.CodeRefreshInvalid, "갱신 토큰이 올바르지 않아요")
	}

	snap, err := r.store.Get(ctx, p)
	if errors.Is(err, store.ErrNotFound) {
		return Session{}, platformerr.New(platformerr.CodeRefreshInvalid, "갱신 토큰이 올바르지 않아요")
	}
	if err != nil {
		return Session{}, platformerr.Wrap(err, platformerr.CodeInternal, "세션을 불러오지 못했어요")
	}

	var doc refreshDoc
	if err := snap.DataTo(&doc); err != nil {
		return Session{}, platformerr.Wrap(err, platformerr.CodeInternal, "세션을 불러오지 못했어요")
	}

	if r.now().After(doc.ExpiresAt) {
		// 만료된 토큰은 지운다. TTL 정책이 있어도 즉시 정리하는 편이 낫다.
		_ = r.store.Delete(ctx, p)
		return Session{}, platformerr.New(platformerr.CodeRefreshInvalid, "갱신 토큰이 만료됐어요")
	}

	return Session{
		PlatformUserID: doc.PlatformUserID,
		AppID:          doc.AppID,
		AppUserID:      doc.AppUserID,
		IsAnonymous:    doc.Anonymous,
	}, nil
}

func (r *StoreRepository) DeleteRefresh(ctx context.Context, token string) error {
	p, err := refreshPath(token)
	if err != nil {
		return nil // 잘못된 토큰은 지울 것도 없다
	}
	if err := r.store.Delete(ctx, p); err != nil && !errors.Is(err, store.ErrNotFound) {
		return platformerr.Wrap(err, platformerr.CodeInternal, "세션을 정리하지 못했어요")
	}
	return nil
}

// DeleteUser는 identity 매핑과 사용자 문서를 지운다.
//
// IAP 원장은 건드리지 않는다. 불변식 5다.
// 원장은 감사 대상이고 환불 처리 같은 후속 작업이 남아 있을 수 있다.
// 소유자 참조가 끊긴 원장은 tombstone으로 남는다.
func (r *StoreRepository) DeleteUser(ctx context.Context, appID, uid, puid string) error {
	idPath, err := identityPath(appID, uid)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "삭제하지 못했어요")
	}
	uPath, err := userPath(puid)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "삭제하지 못했어요")
	}

	err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		if err := tx.Delete(idPath); err != nil {
			return err
		}
		return tx.Delete(uPath)
	})
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "삭제하지 못했어요")
	}
	return nil
}
