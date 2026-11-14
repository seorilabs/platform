package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// ReviewAccessRepository keeps rate limits and the current demo identity in the
// same app/environment boundary as normal identity records. No code is stored.
type ReviewAccessRepository interface {
	AllowReviewAttempt(context.Context, string, time.Time) (bool, error)
	ResolveReviewUID(context.Context, string, string) (string, error)
}

func (s *Service) WithReviewAccessRepository(repo ReviewAccessRepository) *Service {
	s.reviewAccess = repo
	return s
}

func reviewPrefix(appID string) string { return "review_" + hashHex(appID)[:16] + "_" }

func isReviewUID(appID, uid string) bool {
	prefix := reviewPrefix(appID)
	return strings.HasPrefix(uid, prefix) && len(uid) == len(prefix)+64 && isSHA256(strings.TrimPrefix(uid, prefix))
}

// A reserved UID can only come from our app-scoped Firebase signer. A malformed
// or disabled review identity must never fall through to an ordinary account.
func reviewIdentity(app registry.App, uid string) (bool, error) {
	if !strings.HasPrefix(uid, "review_") {
		return false, nil
	}
	if !isReviewUID(app.AppID, uid) || app.Auth.ReviewAccessCodeSHA256 == "" || app.FeatureEnabled("iap") {
		return false, platformerr.New(platformerr.CodeAuthForbidden, "체험 계정을 사용할 수 없어요")
	}
	return true, nil
}

func validateReviewClaims(app registry.App, claims Claims) error {
	if !strings.HasPrefix(claims.UID, "review_") {
		return nil
	}
	if claims.SignInProvider != "custom" || claims.PlatformAppID != app.AppID || claims.PlatformGuest == nil || *claims.PlatformGuest {
		return platformerr.New(platformerr.CodeAuthForbidden, "체험 인증 정보를 확인해 주세요")
	}
	_, err := reviewIdentity(app, claims.UID)
	return err
}

func forbidReviewLink(uid string) error {
	if strings.HasPrefix(uid, "review_") {
		return platformerr.New(platformerr.CodeAuthForbidden, "체험 공간에서는 개인 계정을 연결할 수 없어요")
	}
	return nil
}

func normalizedReviewCode(code string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "", "\t", "", "\n", "", "\r", "").Replace(code))
}

func (s *Service) CreateReviewAccess(ctx context.Context, appID, code, attestation string) (FirebaseCustomTokenResult, error) {
	app, err := s.registry.GetUsable(ctx, appID)
	if err != nil {
		return FirebaseCustomTokenResult{}, err
	}
	if app.Auth.ReviewAccessCodeSHA256 == "" || !app.FeatureEnabled("firebase_custom_token_bridge") || app.FeatureEnabled("iap") {
		return FirebaseCustomTokenResult{}, platformerr.New(platformerr.CodeAuthForbidden, "이 앱은 체험 코드 접근을 사용하지 않아요")
	}
	if s.reviewAccess == nil || s.customTokens == nil {
		return FirebaseCustomTokenResult{}, platformerr.New(platformerr.CodePlatformUnavailable, "체험 연결을 준비하고 있어요")
	}
	if err = s.VerifyAccountLinkAppCheck(ctx, appID, attestation); err != nil {
		return FirebaseCustomTokenResult{}, err
	}
	if len(code) == 0 || len(code) > 128 {
		return FirebaseCustomTokenResult{}, platformerr.New(platformerr.CodeAuthInvalid, "체험 코드를 확인해 주세요")
	}
	allowed, err := s.reviewAccess.AllowReviewAttempt(ctx, appID, s.now().UTC())
	if err != nil {
		return FirebaseCustomTokenResult{}, err
	}
	if !allowed {
		return FirebaseCustomTokenResult{}, platformerr.New(platformerr.CodeRateLimited, "잠시 후 체험 코드를 다시 입력해 주세요")
	}
	digest := sha256.Sum256([]byte(normalizedReviewCode(code)))
	want, decodeErr := hex.DecodeString(app.Auth.ReviewAccessCodeSHA256)
	if decodeErr != nil || subtle.ConstantTimeCompare(digest[:], want) != 1 {
		return FirebaseCustomTokenResult{}, platformerr.New(platformerr.CodeAuthInvalid, "체험 코드를 확인해 주세요")
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return FirebaseCustomTokenResult{}, platformerr.New(platformerr.CodePlatformUnavailable, "체험 연결을 시작할 수 없어요")
	}
	uid, err := s.reviewAccess.ResolveReviewUID(ctx, appID, reviewPrefix(appID)+hex.EncodeToString(random))
	if err != nil {
		return FirebaseCustomTokenResult{}, err
	}
	if !isReviewUID(appID, uid) {
		return FirebaseCustomTokenResult{}, platformerr.New(platformerr.CodeLedgerStateInvalid, "체험 계정 정보가 올바르지 않아요")
	}
	if err = s.ensureNotBlocked(ctx, appID, uid); err != nil {
		return FirebaseCustomTokenResult{}, err
	}
	// Do not write a fake Google provider link. Review identity is distinct and
	// cannot be linked to a personal provider or enabled for paid apps.
	token, err := s.customTokens.Mint(ctx, app, uid, false)
	if err != nil {
		return FirebaseCustomTokenResult{}, platformerr.New(platformerr.CodePlatformUnavailable, "체험 인증을 완료하지 못했어요")
	}
	return FirebaseCustomTokenResult{FirebaseCustomToken: token, AppUserID: uid}, nil
}

func (h *Handler) createReviewAccess(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		AppID string `json:"appId"`
		Code  string `json:"code"`
	}
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	appID, err := resolveAppID(r, req.AppID)
	if err != nil {
		return err
	}
	result, err := h.svc.CreateReviewAccess(r.Context(), appID, req.Code, r.Header.Get("X-Firebase-AppCheck"))
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, firebaseCustomTokenResponse{result.FirebaseCustomToken, result.AppUserID})
	return nil
}
