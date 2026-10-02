package identity

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// DeletionRepository는 인증을 소유하는 identity가 소비한다. 원문 접수증은
// 디스크에 보관하지 않고 앱과 묶은 해시만 저장한다.
type DeletionRepository interface {
	BeginDeletion(context.Context, registry.App, string, string) (DeletionStatus, error)
	DeletionStatus(context.Context, string, string) (DeletionStatus, error)
	AccountDeleting(context.Context, string, string) (bool, error)
	// LinkedProviders는 Firebase uid의 외부 계정 연결(공급자 → subject 해시)을 돌려준다.
	LinkedProviders(context.Context, string, string) (map[string]string, error)
}

// ProviderAuthorization은 계정 삭제 때 공급자 쪽 승인을 철회하려고 앱이 다시 받은 증명이다.
// 지금은 Apple authorization code만 받는다.
type ProviderAuthorization struct {
	Provider          string `json:"provider"`
	AuthorizationCode string `json:"authorizationCode"`
}

type DeletionStatus struct {
	State                   string     `json:"state"`
	RequestedAt             time.Time  `json:"requestedAt"`
	CompletedAt             *time.Time `json:"completedAt,omitempty"`
	GoogleAnalyticsDeletion string     `json:"googleAnalyticsDeletion"`
}

var receiptPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s *Service) WithAccountDeletions(repo DeletionRepository) *Service {
	s.deletions = repo
	return s
}

// WithAppleRevoker는 Apple 연결 계정을 삭제할 때 Sign in with Apple 승인을 철회하게 한다.
func (s *Service) WithAppleRevoker(revoker AppleAuthorizationRevoker) *Service {
	s.appleRevoker = revoker
	return s
}

func (s *Service) RequestAccountDeletion(ctx context.Context, appID, token, receipt string, auth *ProviderAuthorization) (DeletionStatus, error) {
	if !receiptPattern.MatchString(receipt) || strings.TrimSpace(token) == "" || len(token) > 4096 {
		return DeletionStatus{}, platformerr.New(platformerr.CodeRequestInvalid, "삭제 접수 정보를 확인해 주세요")
	}
	app, err := s.registry.GetUsable(ctx, appID)
	if err != nil {
		return DeletionStatus{}, err
	}
	if !app.FeatureEnabled("account_deletion") || s.deletions == nil {
		return DeletionStatus{}, platformerr.New(platformerr.CodeAuthForbidden, "이 앱은 계정 삭제 접수를 지원하지 않아요")
	}
	// UID나 프로젝트를 클라이언트에게 받지 않는다. 삭제 중 차단은 여기서
	// 검사하지 않아야 접수 응답을 잃은 동일 사용자가 재시도할 수 있다.
	claims, err := s.verifier.Verify(ctx, token, app)
	if err != nil {
		return DeletionStatus{}, err
	}
	// 같은 접수증 재시도는 공급자 재승인 없이 현재 상태를 돌려준다(응답 유실 복구).
	if status, err := s.deletions.DeletionStatus(ctx, app.AppID, receipt); err == nil {
		return status, nil
	}
	if err := s.revokeLinkedProviders(ctx, app, claims.UID, auth); err != nil {
		return DeletionStatus{}, err
	}
	return s.deletions.BeginDeletion(ctx, app, claims.UID, receipt)
}

// revokeLinkedProviders는 삭제 접수 전에 공급자 쪽 승인을 끊는다. Apple은 App Store 지침
// 5.1.1(v)에 따라 REST API로 철회해야 하고, 연결 때 토큰을 보관하지 않으므로 앱이 삭제 직전에
// 다시 받은 authorization code로 교환한 refresh token을 바로 철회한다. Google 연결은 ID token만
// 검증했고 공급자 쪽에 남는 승인이 없어 연결 자료 삭제(워커)로 충분하다.
func (s *Service) revokeLinkedProviders(ctx context.Context, app registry.App, uid string, auth *ProviderAuthorization) error {
	linked, err := s.deletions.LinkedProviders(ctx, app.AppID, uid)
	if err != nil {
		return err
	}
	appleHash, hasApple := linked["apple"]
	if !hasApple {
		return nil
	}
	if auth == nil || auth.Provider != "apple" || strings.TrimSpace(auth.AuthorizationCode) == "" {
		return platformerr.New(platformerr.CodeAccountReauthRequired, "Apple 계정을 한 번 더 확인해 주세요")
	}
	if s.appleRevoker == nil || !s.appleRevoker.Configured(app.AppID) {
		return platformerr.New(platformerr.CodePlatformUnavailable, "Apple 계정 처리가 준비되지 않았어요")
	}
	grant, err := s.appleRevoker.Exchange(ctx, app, auth.AuthorizationCode)
	if err != nil {
		return err
	}
	// 다른 Apple ID로 승인했어도 방금 만든 승인은 남기지 않는다.
	if err := s.appleRevoker.Revoke(ctx, app, grant.RefreshToken); err != nil {
		return err
	}
	if hashHex(grant.Subject) != appleHash {
		return platformerr.New(platformerr.CodeAuthForbidden, "연결한 Apple 계정으로 확인해 주세요")
	}
	return nil
}

func (s *Service) AccountDeletionStatus(ctx context.Context, appID, receipt string) (DeletionStatus, error) {
	if !receiptPattern.MatchString(receipt) || appID == "" {
		return DeletionStatus{}, platformerr.New(platformerr.CodeRequestInvalid, "삭제 접수 정보를 확인해 주세요")
	}
	if s.deletions == nil {
		return DeletionStatus{}, platformerr.New(platformerr.CodeAuthForbidden, "삭제 상태를 확인할 수 없어요")
	}
	// 앱 일시중지나 Firebase 계정 삭제 뒤에도 접수증은 읽을 수 있어야 한다.
	return s.deletions.DeletionStatus(ctx, appID, receipt)
}

type accountDeletionRequest struct {
	AppID                 string                 `json:"appId"`
	FirebaseIDToken       string                 `json:"firebaseIdToken"`
	ReceiptToken          string                 `json:"receiptToken"`
	ProviderAuthorization *ProviderAuthorization `json:"providerAuthorization,omitempty"`
}

func (h *Handler) requestAccountDeletion(w http.ResponseWriter, r *http.Request) error {
	var req accountDeletionRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	appID, err := resolveAppID(r, req.AppID)
	if err != nil {
		return err
	}
	if err = h.svc.VerifyAppCheck(r.Context(), appID, r.Header.Get("X-Firebase-AppCheck")); err != nil {
		return err
	}
	result, err := h.svc.RequestAccountDeletion(r.Context(), appID, req.FirebaseIDToken, req.ReceiptToken, req.ProviderAuthorization)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteOK(w, http.StatusAccepted, result)
	return nil
}
func (h *Handler) accountDeletionStatus(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		AppID        string `json:"appId"`
		ReceiptToken string `json:"receiptToken"`
	}
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	appID, err := resolveAppID(r, req.AppID)
	if err != nil {
		return err
	}
	result, err := h.svc.AccountDeletionStatus(r.Context(), appID, req.ReceiptToken)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}
