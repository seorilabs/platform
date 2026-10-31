package identity

import (
	"context"
	"net/http"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// CreateSessionFirebaseCustomToken은 이미 검증된 세션 소유자만 Firebase로 잇는다.
// IAP role이 mTLS로 검증한 AIT 세션도 같은 서명 경계를 쓰므로 API role에
// 앱별 mTLS 개인키를 복제할 필요가 없다. UID를 요청 본문에서 받지 않는다.
func (s *Service) CreateSessionFirebaseCustomToken(ctx context.Context, sess Session) (FirebaseCustomTokenResult, error) {
	if err := sess.EnsureNotAnonymous(); err != nil {
		return FirebaseCustomTokenResult{}, err
	}
	app, err := s.registry.GetUsable(ctx, sess.AppID)
	if err != nil {
		return FirebaseCustomTokenResult{}, err
	}
	if !app.FeatureEnabled("firebase_custom_token_bridge") {
		return FirebaseCustomTokenResult{}, platformerr.New(platformerr.CodeAuthForbidden, "이 앱은 custom token bridge를 사용하지 않아요")
	}
	if s.customTokens == nil {
		return FirebaseCustomTokenResult{}, platformerr.New(platformerr.CodePlatformUnavailable, "인증 bridge를 사용할 수 없어요")
	}
	if err := s.ensureNotBlocked(ctx, app.AppID, sess.AppUserID); err != nil {
		return FirebaseCustomTokenResult{}, err
	}
	token, err := s.customTokens.Mint(ctx, app, sess.AppUserID, !sess.IsLinkedAccount)
	if err != nil {
		return FirebaseCustomTokenResult{}, platformerr.Wrap(err, platformerr.CodePlatformUnavailable, "Firebase 인증 토큰을 만들지 못했어요")
	}
	return FirebaseCustomTokenResult{FirebaseCustomToken: token, AppUserID: sess.AppUserID, PlatformUserID: sess.PlatformUserID}, nil
}

func (h *Handler) createSessionFirebaseCustomToken(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.Authenticate(r)
	if err != nil {
		return err
	}
	if r.ContentLength != 0 {
		var body struct{}
		if err := httpx.DecodeStrict(w, r, &body); err != nil {
			return err
		}
	}
	if err := h.svc.VerifyAppCheck(r.Context(), sess.AppID, r.Header.Get("X-Firebase-AppCheck")); err != nil {
		return err
	}
	res, err := h.svc.CreateSessionFirebaseCustomToken(r.Context(), sess)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, firebaseCustomTokenResponse{FirebaseCustomToken: res.FirebaseCustomToken, AppUserID: res.AppUserID})
	return nil
}
