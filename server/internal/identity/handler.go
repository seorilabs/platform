package identity

import (
	"context"
	"net/http"
	"time"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// AppHeader는 어느 앱의 요청인지 고르는 힌트다.
//
// 권한이 아니다. 헤더를 바꿔도 토큰의 aud 불일치로 거부된다.
// Obsidian 프로젝트/platform/03-architecture/identity.md 참고.
const AppHeader = "X-Seori-App"

// Handler는 identity HTTP 핸들러다.
type Handler struct {
	svc         *Service
	kakaoUnlink *KakaoUnlinkWebhookConfig
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// WithKakaoUnlinkWebhook은 카카오 연결 해제 webhook을 활성화한다.
func (h *Handler) WithKakaoUnlinkWebhook(config KakaoUnlinkWebhookConfig) *Handler {
	config.AdminKey = append([]byte(nil), config.AdminKey...)
	h.kakaoUnlink = &config
	return h
}

// Register는 라우트를 등록한다.
//
// 표준 net/http의 ServeMux 패턴 라우팅을 쓴다.
// Go 1.22+가 메서드와 경로 변수를 지원하므로 외부 라우터가 불필요하다.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/session", httpx.Wrap(h.createSession))
	mux.HandleFunc("POST /v1/auth/firebase-custom-token", httpx.Wrap(h.createFirebaseCustomToken))
	mux.HandleFunc("POST /v1/auth/account-link-challenges", httpx.Wrap(h.createAccountLinkChallenge))
	mux.HandleFunc("POST /v1/auth/account-links", httpx.Wrap(h.createAccountLink))
	if h.kakaoUnlink != nil {
		mux.HandleFunc("POST /v1/auth/webhooks/kakao/unlink", httpx.Wrap(h.kakaoUnlinkWebhook))
	}
	mux.HandleFunc("DELETE /v1/auth/firebase-account", httpx.Wrap(h.deleteFirebaseAccount))
	mux.HandleFunc("POST /v1/auth/refresh", httpx.Wrap(h.refresh))
	mux.HandleFunc("DELETE /v1/users/me", httpx.Wrap(h.deleteMe))
}

type deleteFirebaseAccountRequest struct {
	AppID           string `json:"appId"`
	FirebaseIDToken string `json:"firebaseIdToken"`
}

func (h *Handler) deleteFirebaseAccount(w http.ResponseWriter, r *http.Request) error {
	var req deleteFirebaseAccountRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if req.AppID == "" || req.FirebaseIDToken == "" {
		return platformerr.New(
			platformerr.CodeRequestInvalid,
			"appId와 Firebase token이 필요해요",
		)
	}
	appID, err := resolveAppID(r, req.AppID)
	if err != nil {
		return err
	}
	if err := h.svc.VerifyAppCheck(
		r.Context(),
		appID,
		r.Header.Get("X-Firebase-AppCheck"),
	); err != nil {
		return err
	}
	if err := h.svc.DeleteFirebaseAccount(
		r.Context(),
		appID,
		req.FirebaseIDToken,
	); err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, map[string]bool{"deleted": true})
	return nil
}

type createFirebaseCustomTokenRequest struct {
	AppID                   string `json:"appId"`
	ExistingFirebaseIDToken string `json:"existingFirebaseIdToken,omitempty"`
}

type firebaseCustomTokenResponse struct {
	FirebaseCustomToken string `json:"firebaseCustomToken"`
	AppUserID           string `json:"appUserId"`
}

func (h *Handler) createFirebaseCustomToken(w http.ResponseWriter, r *http.Request) error {
	var req createFirebaseCustomTokenRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if req.AppID == "" {
		return platformerr.New(platformerr.CodeRequestInvalid, "appId가 필요해요")
	}
	appID, err := resolveAppID(r, req.AppID)
	if err != nil {
		return err
	}
	if err := h.svc.VerifyAppCheck(
		r.Context(),
		appID,
		r.Header.Get("X-Firebase-AppCheck"),
	); err != nil {
		return err
	}
	result, err := h.svc.CreateFirebaseCustomToken(
		r.Context(),
		appID,
		req.ExistingFirebaseIDToken,
	)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, firebaseCustomTokenResponse{
		FirebaseCustomToken: result.FirebaseCustomToken,
		AppUserID:           result.AppUserID,
	})
	return nil
}

type createSessionRequest struct {
	AppID      string `json:"appId"`
	Credential struct {
		Kind     string `json:"kind"`
		Value    string `json:"value"`
		Referrer string `json:"referrer,omitempty"`
	} `json:"credential"`
}

type sessionResponse struct {
	PlatformToken  string `json:"platformToken"`
	RefreshToken   string `json:"refreshToken"`
	PlatformUserID string `json:"platformUserId"`
	// 앱이 설정 화면에 보여줄 식별자다. Firebase uid를 보여주면 CS가
	// 그걸로 우리 원장을 찾을 수 없다.
	SupportCode     string `json:"supportCode"`
	AppUserID       string `json:"appUserId,omitempty"`
	IsAnonymous     bool   `json:"isAnonymous"`
	IsLinkedAccount bool   `json:"isLinkedAccount"`
	ExpiresIn       int    `json:"expiresIn"`
	ServerTimeUnix  int64  `json:"serverTimeUnix"`
}

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) error {
	var req createSessionRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}

	// 헤더와 본문이 모두 앱을 지정할 수 있다. 어긋나면 거부한다.
	// 둘 중 하나를 조용히 우선하면 클라이언트가 어느 쪽이 쓰였는지 모른다.
	appID, err := resolveAppID(r, req.AppID)
	if err != nil {
		return err
	}

	res, err := h.svc.CreateSession(r.Context(), appID, Credential{
		Kind:     CredentialKind(req.Credential.Kind),
		Value:    req.Credential.Value,
		Referrer: req.Credential.Referrer,
	})
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, h.sessionResponse(res))
	return nil
}

type createAccountLinkChallengeRequest struct {
	Provider string `json:"provider"`
}

type accountLinkChallengeResponse struct {
	Provider  string    `json:"provider"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (h *Handler) createAccountLinkChallenge(w http.ResponseWriter, r *http.Request) error {
	var req createAccountLinkChallengeRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if req.Provider == "" {
		return platformerr.New(platformerr.CodeRequestInvalid, "로그인 공급자가 필요해요")
	}
	sess, err := h.Authenticate(r)
	if err != nil {
		return err
	}
	if err := h.VerifyAppCheck(
		r.Context(), sess.AppID, r.Header.Get("X-Firebase-AppCheck"),
	); err != nil {
		return err
	}
	challenge, err := h.svc.BeginAccountLink(r.Context(), sess, req.Provider)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusCreated, accountLinkChallengeResponse(challenge))
	return nil
}

type createAccountLinkRequest struct {
	Provider string `json:"provider"`
	IDToken  string `json:"idToken"`
	Nonce    string `json:"nonce"`
}

type accountLinkResponse struct {
	Session             sessionResponse `json:"session"`
	FirebaseCustomToken string          `json:"firebaseCustomToken"`
	Provider            string          `json:"provider"`
	Restored            bool            `json:"restored"`
}

func (h *Handler) createAccountLink(w http.ResponseWriter, r *http.Request) error {
	var req createAccountLinkRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if req.Provider == "" || req.IDToken == "" || req.Nonce == "" {
		return platformerr.New(platformerr.CodeRequestInvalid, "로그인 정보가 필요해요")
	}
	sess, err := h.Authenticate(r)
	if err != nil {
		return err
	}
	if err := h.VerifyAppCheck(
		r.Context(), sess.AppID, r.Header.Get("X-Firebase-AppCheck"),
	); err != nil {
		return err
	}
	result, err := h.svc.CompleteAccountLink(
		r.Context(), sess, req.Provider, req.IDToken, req.Nonce,
	)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, accountLinkResponse{
		Session: h.sessionResponse(result.Session), FirebaseCustomToken: result.FirebaseCustomToken,
		Provider: result.Provider, Restored: result.Restored,
	})
	return nil
}

// RegisterSession은 별도 역할이 세션 교환과 갱신만 열 때 쓴다.
// platform-ads는 AppsInToss mTLS 자격증명을 갖지만 Firebase custom token
// 서명 권한은 갖지 않으므로 전체 Register를 사용하지 않는다.
func (h *Handler) RegisterSession(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/session", httpx.Wrap(h.createSession))
	mux.HandleFunc("POST /v1/auth/refresh", httpx.Wrap(h.refresh))
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) error {
	var req refreshRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if req.RefreshToken == "" {
		return platformerr.New(platformerr.CodeRefreshInvalid, "갱신 토큰이 필요해요")
	}

	appID, err := httpx.Header(r, AppHeader, platformerr.CodeRequestInvalid)
	if err != nil {
		return err
	}

	res, err := h.svc.Refresh(r.Context(), appID, req.RefreshToken)
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, h.sessionResponse(res))
	return nil
}

func (h *Handler) sessionResponse(res Result) sessionResponse {
	return sessionResponse{
		PlatformToken: res.PlatformToken, RefreshToken: res.RefreshToken,
		PlatformUserID: res.PlatformUserID, SupportCode: res.SupportCode, AppUserID: res.AppUserID,
		IsAnonymous: res.IsAnonymous, IsLinkedAccount: res.IsLinkedAccount,
		ExpiresIn: res.ExpiresIn, ServerTimeUnix: h.svc.now().Unix(),
	}
}

func (h *Handler) deleteMe(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.Authenticate(r)
	if err != nil {
		return err
	}
	if err := h.svc.DeleteCurrentUser(r.Context(), sess); err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, map[string]bool{"deleted": true})
	return nil
}

// Authenticate는 요청에서 세션을 꺼낸다. 다른 패키지의 핸들러도 쓴다.
func (h *Handler) Authenticate(r *http.Request) (Session, error) {
	appID, err := httpx.Header(r, AppHeader, platformerr.CodeRequestInvalid)
	if err != nil {
		return Session{}, err
	}
	token, err := httpx.BearerToken(r)
	if err != nil {
		return Session{}, err
	}
	return h.svc.Authenticate(r.Context(), appID, token)
}

// VerifyAppCheck는 인증된 기능 핸들러가 동일한 App Check 검증 경계를
// 재사용하게 한다. 검증 구현이나 레지스트리 세부사항은 identity 밖으로
// 노출하지 않는다.
func (h *Handler) VerifyAppCheck(ctx context.Context, appID, token string) error {
	return h.svc.VerifyAppCheck(ctx, appID, token)
}

// resolveAppID는 헤더와 본문의 앱 식별자를 대조한다.
func resolveAppID(r *http.Request, bodyAppID string) (string, error) {
	headerAppID, err := httpx.Header(r, AppHeader, platformerr.CodeRequestInvalid)
	if err != nil {
		return "", err
	}
	if bodyAppID != "" && bodyAppID != headerAppID {
		return "", platformerr.New(platformerr.CodeRequestInvalid,
			"헤더와 본문의 앱이 서로 달라요")
	}
	return headerAppID, nil
}

// contextKey는 세션을 컨텍스트에 넣을 때 쓴다.
type contextKey int

const sessionKey contextKey = iota

// WithSession은 컨텍스트에 세션을 넣는다.
func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey, s)
}

// SessionOf는 컨텍스트에서 세션을 꺼낸다.
func SessionOf(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey).(Session)
	return s, ok
}

// RequireSession은 인증을 강제하는 미들웨어다.
//
// 통과하면 세션이 컨텍스트에 들어가므로 핸들러가 SessionOf로 꺼낸다.
func (h *Handler) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := h.Authenticate(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := httpx.WithAppID(WithSession(r.Context(), sess), sess.AppID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
