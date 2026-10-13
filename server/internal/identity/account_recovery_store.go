package identity

import (
	"context"
	"time"

	"github.com/seorilabs/platform/server/internal/blocklist"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

func (r *StoreRepository) CreateAccountLinkRecoveryChallenge(ctx context.Context, appID, platformUserID, provider, nonce, expectedAppUserID string, expiresAt time.Time) error {
	path, err := accountChallengePath(nonce)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "로그인 요청을 저장하지 못했어요")
	}
	return r.store.Set(ctx, path, accountChallengeDoc{Purpose: "recovery", ExpectedAppUserID: expectedAppUserID,
		AppID: appID, PlatformUserID: platformUserID, Provider: provider, ExpiresAt: expiresAt.UTC(),
		CreatedAt: r.now().UTC(), TTLAt: expiresAt.UTC().Add(24 * time.Hour)})
}

func (r *StoreRepository) checkRecoveryAccessTx(tx *store.Tx, appID, uid, puid string) error {
	if err := r.checkDeletionTx(tx, appID, uid); err != nil {
		return err
	}
	if err := r.CheckAccountActive(tx, appID, puid); err != nil {
		return err
	}
	blocked, err := blocklist.BlockedTx(tx, appID, uid)
	if err != nil {
		return err
	}
	if blocked {
		return platformerr.New(platformerr.CodeUserBlocked, "이용이 제한된 계정이에요")
	}
	return nil
}

// RecoverAccount는 이미 연결된 대상만 검증한다. 모든 재전송에서도 현재 매핑을
// 읽으므로 소비한 challenge가 해제된 공급자 연결을 되살릴 수 없다.
func (r *StoreRepository) RecoverAccount(ctx context.Context, appID, currentPlatformUserID, provider, subject, nonce, expectedAppUserID string, now time.Time) (ConnectedAccount, error) {
	challengePath, err := accountChallengePath(nonce)
	if err != nil {
		return ConnectedAccount{}, platformerr.New(platformerr.CodeAuthInvalid, "로그인 요청이 올바르지 않아요")
	}
	subjectHash := hashHex(subject)
	providerPath, err := providerIdentityPath(appID, provider, subjectHash)
	if err != nil {
		return ConnectedAccount{}, platformerr.New(platformerr.CodeAuthInvalid, "로그인 정보가 올바르지 않아요")
	}
	currentPath, err := userPath(currentPlatformUserID)
	if err != nil {
		return ConnectedAccount{}, platformerr.New(platformerr.CodeAuthInvalid, "현재 사용자가 올바르지 않아요")
	}
	var result ConnectedAccount
	err = r.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		exists, snap, err := tx.Exists(challengePath)
		if err != nil {
			return err
		}
		if !exists {
			return platformerr.New(platformerr.CodeAuthInvalid, "로그인 요청을 찾을 수 없어요")
		}
		var challenge accountChallengeDoc
		if err := snap.DataTo(&challenge); err != nil {
			return err
		}
		if challenge.Purpose != "recovery" || challenge.AppID != appID || challenge.PlatformUserID != currentPlatformUserID || challenge.Provider != provider || challenge.ExpectedAppUserID != expectedAppUserID || !now.Before(challenge.ExpiresAt) {
			return platformerr.New(platformerr.CodeAuthInvalid, "복구 요청이 일치하지 않거나 만료됐어요")
		}
		exists, snap, err = tx.Exists(currentPath)
		if err != nil {
			return err
		}
		if !exists {
			return platformerr.New(platformerr.CodeUserNotFound, "현재 사용자를 찾을 수 없어요")
		}
		var current userDoc
		if err := snap.DataTo(&current); err != nil {
			return err
		}
		if current.AppID != appID || current.AppUserID == "" {
			return platformerr.New(platformerr.CodeAccountLinkConflict, "현재 계정 정보가 일치하지 않아요")
		}
		exists, snap, err = tx.Exists(providerPath)
		if err != nil {
			return err
		}
		if !exists {
			return platformerr.New(platformerr.CodeAccountLinkConflict, "원래 연결한 계정으로 확인해 주세요")
		}
		var mapping providerIdentityDoc
		if err := snap.DataTo(&mapping); err != nil {
			return err
		}
		if mapping.AppID != appID || mapping.Provider != provider || mapping.SubjectHash != subjectHash || mapping.PlatformUserID == "" {
			return platformerr.New(platformerr.CodeAccountLinkConflict, "계정 매핑이 일치하지 않아요")
		}
		targetPath, err := userPath(mapping.PlatformUserID)
		if err != nil {
			return err
		}
		exists, snap, err = tx.Exists(targetPath)
		if err != nil {
			return err
		}
		if !exists {
			return platformerr.New(platformerr.CodeUserNotFound, "연결된 사용자를 찾을 수 없어요")
		}
		var target userDoc
		if err := snap.DataTo(&target); err != nil {
			return err
		}
		if target.AppID != appID || target.AppUserID != expectedAppUserID || target.LinkedProviders[provider] != subjectHash {
			return platformerr.New(platformerr.CodeAccountLinkConflict, "원래 연결한 계정으로 확인해 주세요")
		}
		if currentPlatformUserID != mapping.PlatformUserID && len(current.LinkedProviders) > 0 {
			return platformerr.New(platformerr.CodeAccountLinkConflict, "다른 연결 계정으로 전환할 수 없어요")
		}
		if !challenge.ConsumedAt.IsZero() && (challenge.SubjectHash != subjectHash || challenge.TargetUserID != mapping.PlatformUserID) {
			return platformerr.New(platformerr.CodeAccountLinkConflict, "다른 계정이 사용한 복구 요청이에요")
		}
		if err := r.checkRecoveryAccessTx(tx, appID, current.AppUserID, currentPlatformUserID); err != nil {
			return err
		}
		if err := r.checkRecoveryAccessTx(tx, appID, target.AppUserID, mapping.PlatformUserID); err != nil {
			return err
		}
		if appID == "bloomhand" && currentPlatformUserID != mapping.PlatformUserID {
			if err := ledger.CheckGuestAccountSwitch(tx, appID, currentPlatformUserID); err != nil {
				return err
			}
		}
		challenge.ConsumedAt = now
		challenge.SubjectHash = subjectHash
		challenge.TargetUserID = mapping.PlatformUserID
		// 같은 proof 재전송도 검증한 문서들의 commit 시점과 직렬화한다.
		if err := tx.Set(challengePath, challenge); err != nil {
			return err
		}
		result = ConnectedAccount{PlatformUserID: mapping.PlatformUserID, AppUserID: target.AppUserID, Provider: provider, Restored: currentPlatformUserID != mapping.PlatformUserID}
		return nil
	})
	if err != nil {
		return ConnectedAccount{}, err
	}
	return result, nil
}
