package ledger

import (
	"context"
	"regexp"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

// SaltLegacyBalance는 활성화 전 저장을 보존하기 위한 일회 운영 기준값이다.
// 과거 사용자 writable 저장의 동결은 획득 진위의 증명이 아니다. 공개 API에는
// 이 문서의 생성·증액 경로가 없고 운영 importer만 create-only로 기록한다.
type SaltLegacyBalance struct {
	PlatformUserID string    `json:"platformUserId" firestore:"platformUserId"`
	SealCap        int64     `json:"sealCap" firestore:"sealCap"`
	SourceDigest   string    `json:"sourceDigest" firestore:"sourceDigest"`
	SourceKind     string    `json:"sourceKind" firestore:"sourceKind"`
	CapturedAt     time.Time `json:"capturedAt" firestore:"capturedAt"`
}

var saltDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (b SaltLegacyBalance) valid() bool {
	return b.SealCap >= 0 && b.SealCap <= 1000000 && saltDigestPattern.MatchString(b.SourceDigest) &&
		(b.SourceKind == "pre-iap-cloud-snapshot" || b.SourceKind == "pre-update-device-snapshot") && !b.CapturedAt.IsZero()
}

func (b SaltLegacyBalance) Validate() error {
	if !operatorPUIDPattern.MatchString(b.PlatformUserID) || !b.valid() {
		return saltError("이관 기준값과 원본 증거를 확인해 주세요")
	}
	return nil
}

// ImportSaltLegacy는 기존 정산을 바꾸지 않는다. 같은 snapshot 재시도만 멱등 성공한다.
// 운영 credential과 비공개 원본을 쓰는 cmd/saltmigrate에서만 호출한다(불변식 5·8).
func (l *Ledger) ImportSaltLegacy(ctx context.Context, b SaltLegacyBalance) error {
	if l.appID != saltAppID || b.Validate() != nil || b.CapturedAt.After(l.now().Add(time.Minute)) {
		return saltError("이관 기준값과 원본 증거를 확인해 주세요")
	}
	p, err := l.saltPath("salt_compass_legacy", b.PlatformUserID)
	if err != nil {
		return err
	}
	sp, err := l.saltStatePath(b.PlatformUserID)
	if err != nil {
		return err
	}
	return l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		exists, snap, err := tx.Exists(p)
		if err != nil {
			return err
		}
		if exists {
			var prior SaltLegacyBalance
			if err := snap.DataTo(&prior); err != nil {
				return err
			}
			if prior.PlatformUserID != b.PlatformUserID || prior.SealCap != b.SealCap || prior.SourceDigest != b.SourceDigest || prior.SourceKind != b.SourceKind || !prior.CapturedAt.Equal(b.CapturedAt) {
				return platformerr.New(platformerr.CodePurchaseReplayMismatch, "이미 동결한 이관 기준값을 바꿀 수 없어요")
			}
			return nil
		}
		exists, snap, err = tx.Exists(sp)
		if err != nil {
			return err
		}
		if exists {
			var state saltState
			if err := snap.DataTo(&state); err != nil {
				return err
			}
			if state.Initialized {
				return saltError("이미 연결된 지갑에는 기준값을 추가하지 않아요")
			}
		}
		return tx.Create(p, b)
	})
}

func (l *Ledger) verifySaltLegacy(tx *store.Tx, puid string, requested int64) error {
	p, err := l.saltPath("salt_compass_legacy", puid)
	if err != nil {
		return err
	}
	exists, snap, err := tx.Exists(p)
	if err != nil {
		return err
	}
	if !exists {
		return saltError("기존 인장 기록의 확인이 필요해요")
	}
	var baseline SaltLegacyBalance
	if err := snap.DataTo(&baseline); err != nil {
		return err
	}
	if baseline.PlatformUserID != puid || !baseline.valid() {
		return platformerr.New(platformerr.CodeLedgerStateInvalid, "이관 기준값의 원본을 확인해 주세요")
	}
	if requested > baseline.SealCap {
		return saltError("기존 인장 수량과 이관 기준값이 맞지 않아요")
	}
	// 동결 후 오프라인에서 쓴 인장을 되살리지 않는다. 감소한 local 값만 반영한다.
	return nil
}
