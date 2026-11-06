package ledger

import (
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

// 기존 양수 잔액은 이번 출시에서 자동 이관하지 않는다(SC-LEGACY-SPLIT-20261106-v1).
// 운영 hold 문서를 읽기만 하며 생성·증액·수량 보정 경로는 제공하지 않는다.
type saltLegacyHold struct {
	PlatformUserID string `firestore:"platformUserId"`
	SealCap        int64  `firestore:"sealCap"`
}

func (l *Ledger) verifySaltLegacy(tx *store.Tx, puid string, requested int64) error {
	if requested > 0 {
		return saltError("기존 양수 인장은 보존하고 별도 이관 확인이 필요해요")
	}
	p, err := l.saltPath("salt_compass_legacy", puid)
	if err != nil {
		return err
	}
	exists, snap, err := tx.Exists(p)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var hold saltLegacyHold
	if err := snap.DataTo(&hold); err != nil {
		return err
	}
	if hold.PlatformUserID != puid || hold.SealCap < 0 {
		return platformerr.New(platformerr.CodeLedgerStateInvalid, "기존 인장 기준값을 확인해 주세요")
	}
	if hold.SealCap > 0 {
		return saltError("기존 양수 인장과 현재 저장의 확인이 필요해요")
	}
	return nil
}
