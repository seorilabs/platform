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

func (s *Service) RequestAccountDeletion(ctx context.Context, appID, token, receipt string) (DeletionStatus, error) {
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
	return s.deletions.BeginDeletion(ctx, app, claims.UID, receipt)
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
	AppID           string `json:"appId"`
	FirebaseIDToken string `json:"firebaseIdToken"`
	ReceiptToken    string `json:"receiptToken"`
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
	result, err := h.svc.RequestAccountDeletion(r.Context(), appID, req.FirebaseIDToken, req.ReceiptToken)
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
