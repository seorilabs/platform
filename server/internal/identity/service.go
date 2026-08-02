package identity

import (
	"context"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// CredentialKind는 클라이언트가 제시하는 자격증명 종류다.
type CredentialKind string

const (
	// KindFirebaseIDToken은 앱별 Firebase Auth의 ID 토큰이다. 기본 경로다.
	KindFirebaseIDToken CredentialKind = "firebase-id-token"
	// KindAITLogin은 AppsInToss appLogin 토큰이다.
	//
	// 토스 서버 검증 API 존재 여부가 아직 확인되지 않았다.
	// lizard-tycoon에서도 aitUserKey claim 발급 경로가 미구현이다.
	KindAITLogin CredentialKind = "ait-login"
	// KindAnonymous는 getAnonymousKey 해시다.
	//
	// bearer 자격증명이 아니라 클라이언트가 아무 값이나 보낼 수 있다.
	// 즉 타인 사칭이 가능하므로 IAP와 푸시 토큰 등록에서 금지한다.
	KindAnonymous CredentialKind = "anonymous"
)

// Credential은 세션 교환에 쓰는 자격증명이다.
type Credential struct {
	Kind  CredentialKind
	Value string
}

// TokenVerifier는 Firebase ID 토큰을 검증한다.
//
// 인터페이스를 여기에 두는 이유는 Service가 소비자이기 때문이다.
// FirebaseVerifier가 구현하고 테스트는 fake를 쓴다.
type TokenVerifier interface {
	Verify(ctx context.Context, token string, app registry.App) (Claims, error)
}

// UserRepository는 identity 저장소다.
type UserRepository interface {
	// EnsureUser는 (appID, uid)에 대응하는 platform_user_id를 돌려준다.
	//
	// 없으면 만들고 있으면 기존 것을 쓴다. 동시 호출에도 하나만 만들어야 한다.
	// 여러 개가 만들어지면 같은 사람의 결제 원장이 갈라진다.
	EnsureUser(ctx context.Context, appID, uid string, anonymous bool) (string, error)

	// SaveRefresh는 갱신 토큰을 저장한다. 원문이 아니라 해시로 저장한다.
	SaveRefresh(ctx context.Context, token string, sess Session, expiresAt time.Time) error
	// LoadRefresh는 갱신 토큰으로 세션을 복원한다.
	LoadRefresh(ctx context.Context, token string) (Session, error)
	// DeleteRefresh는 갱신 토큰을 폐기한다.
	DeleteRefresh(ctx context.Context, token string) error

	// DeleteUser는 사용자를 삭제한다.
	//
	// IAP 원장 문서는 지우지 않는다. 불변식 5다.
	// 소유자 참조만 끊고 원장은 감사 대상으로 남긴다.
	DeleteUser(ctx context.Context, appID, uid, platformUserID string) error
}

// Result는 세션 교환 결과다.
type Result struct {
	PlatformToken  string
	RefreshToken   string
	PlatformUserID string
	// SupportCode는 유저가 문의에 첨부하는 식별자다.
	//
	// 앱이 Firebase uid를 화면에 보여주면 CS가 그걸로 우리 원장을 찾을
	// 수 없다. 플랫폼은 앱의 uid를 조회 키로 두지 않고, PII도 저장하지
	// 않아 이메일 검색이 성립하지 않는다. ADR 0005다.
	SupportCode string
	AppUserID   string
	IsAnonymous bool
	ExpiresIn   int
	ExpiresAt   time.Time
}

// Service는 세션 교환 유스케이스다.
type Service struct {
	registry   *registry.Registry
	verifier   TokenVerifier
	users      UserRepository
	issuer     *SessionIssuer
	refreshTTL time.Duration
	now        func() time.Time
}

// NewService는 서비스를 만든다.
func NewService(
	reg *registry.Registry,
	verifier TokenVerifier,
	users UserRepository,
	issuer *SessionIssuer,
) *Service {
	return &Service{
		registry:   reg,
		verifier:   verifier,
		users:      users,
		issuer:     issuer,
		refreshTTL: DefaultRefreshTTL,
		now:        time.Now,
	}
}

// WithClock은 시계를 주입한다. 테스트용이다.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// CreateSession은 자격증명을 플랫폼 세션으로 교환한다.
func (s *Service) CreateSession(ctx context.Context, appID string, cred Credential) (Result, error) {
	app, err := s.registry.GetUsable(ctx, appID)
	if err != nil {
		return Result{}, err
	}

	uid, anonymous, err := s.resolveIdentity(ctx, app, cred)
	if err != nil {
		return Result{}, err
	}

	puid, err := s.users.EnsureUser(ctx, app.AppID, uid, anonymous)
	if err != nil {
		return Result{}, err
	}

	return s.issue(ctx, Session{
		PlatformUserID: puid,
		AppID:          app.AppID,
		AppUserID:      uid,
		IsAnonymous:    anonymous,
	})
}

// resolveIdentity는 자격증명에서 앱 사용자 식별자를 얻는다.
func (s *Service) resolveIdentity(
	ctx context.Context,
	app registry.App,
	cred Credential,
) (uid string, anonymous bool, err error) {
	value := strings.TrimSpace(cred.Value)
	if value == "" {
		return "", false, platformerr.New(platformerr.CodeAuthRequired, "자격증명이 필요해요")
	}

	switch cred.Kind {
	case KindFirebaseIDToken:
		claims, err := s.verifier.Verify(ctx, value, app)
		if err != nil {
			return "", false, err
		}
		// Firebase 익명 로그인은 여기서 익명으로 치지 않는다.
		//
		// 이 플래그가 막는 것은 "사칭 가능한 신원"이지 "계정이 없는
		// 사용자"가 아니다. Firebase 익명 계정도 서명된 ID 토큰을 받고
		// 우리가 서명·aud·iss·exp를 전부 검증한다. 다른 사람의 uid를
		// 주장할 수 없다는 점에서 이메일 계정과 같다.
		//
		// 반대로 KindAnonymous는 클라이언트가 아무 값이나 보낼 수 있는
		// 해시라 사칭이 된다. 둘은 이름만 같고 성질이 다르다.
		//
		// 실제로 이걸 묶어 두면 lizard-tycoon은 결제가 하나도 되지 않는다.
		// 전 사용자가 Firebase 익명 계정이기 때문이다.
		return claims.UID, false, nil

	case KindAnonymous:
		// 사칭 가능한 신원이다. 여기서 막지 않고 세션에 표시만 한다.
		// IAP 같은 민감 경로가 EnsureNotAnonymous로 거부한다.
		// 이렇게 하는 이유는 RemoteConfig 조회와 이벤트 로그는
		// 익명으로도 허용해야 하기 때문이다.
		if app.UIDBlocked(value) {
			return "", false, platformerr.New(platformerr.CodeUserBlocked, "이용이 제한된 계정이에요")
		}
		return "anon:" + value, true, nil

	case KindAITLogin:
		// 토스 서버 검증 API가 확인되지 않았다. 확인 전에는 받지 않는다.
		// 검증 없이 받으면 클라이언트가 보낸 값을 그대로 신뢰하게 된다.
		return "", false, platformerr.New(platformerr.CodeAuthInvalid,
			"아직 지원하지 않는 로그인 방식이에요")

	default:
		return "", false, platformerr.Newf(platformerr.CodeRequestInvalid,
			"알 수 없는 자격증명 종류예요: %s", cred.Kind)
	}
}

// Refresh는 갱신 토큰으로 새 세션을 발급한다.
//
// 쓰인 갱신 토큰은 폐기하고 새로 발급한다. 회전이다.
// 유출된 토큰이 무기한 쓰이는 걸 막는다.
func (s *Service) Refresh(ctx context.Context, appID, refreshToken string) (Result, error) {
	app, err := s.registry.GetUsable(ctx, appID)
	if err != nil {
		return Result{}, err
	}

	sess, err := s.users.LoadRefresh(ctx, refreshToken)
	if err != nil {
		return Result{}, err
	}

	// 다른 앱의 갱신 토큰으로 이 앱의 세션을 받을 수 없다.
	if sess.AppID != app.AppID {
		return Result{}, platformerr.New(platformerr.CodeRefreshInvalid, "갱신 토큰이 올바르지 않아요")
	}
	if app.UIDBlocked(sess.AppUserID) {
		return Result{}, platformerr.New(platformerr.CodeUserBlocked, "이용이 제한된 계정이에요")
	}

	// 회전. 실패해도 새 토큰 발급은 진행한다.
	// 옛 토큰이 남는 것보다 사용자가 로그아웃되는 게 더 나쁘다.
	_ = s.users.DeleteRefresh(ctx, refreshToken)

	return s.issue(ctx, sess)
}

func (s *Service) issue(ctx context.Context, sess Session) (Result, error) {
	token, exp, err := s.issuer.Issue(sess)
	if err != nil {
		return Result{}, platformerr.Wrap(err, platformerr.CodeInternal, "세션을 만들지 못했어요")
	}

	refresh, err := NewRefreshToken()
	if err != nil {
		return Result{}, platformerr.Wrap(err, platformerr.CodeInternal, "세션을 만들지 못했어요")
	}

	refreshExp := s.now().Add(s.refreshTTL)
	if err := s.users.SaveRefresh(ctx, refresh, sess, refreshExp); err != nil {
		return Result{}, err
	}

	// 저장된 값을 다시 읽지 않고 여기서 만든다.
	//
	// EnsureUser가 저장할 때와 같은 NewSupportCode를 쓴다. 조합 지점이
	// 하나뿐이라 갈라질 수 없다. refreshDoc에 필드를 더하는 방법도 있지만
	// 이미 발급된 갱신 토큰에는 값이 없어, 재로그인 전까지 빈 코드가 나간다.
	return Result{
		PlatformToken:  token,
		RefreshToken:   refresh,
		PlatformUserID: sess.PlatformUserID,
		SupportCode:    NewSupportCode(sess.AppID, sess.PlatformUserID),
		AppUserID:      sess.AppUserID,
		IsAnonymous:    sess.IsAnonymous,
		ExpiresIn:      int(s.issuer.TTL().Seconds()),
		ExpiresAt:      exp,
	}, nil
}

// Authenticate는 세션 토큰을 검증한다. 인증이 필요한 핸들러가 쓴다.
func (s *Service) Authenticate(ctx context.Context, appID, sessionToken string) (Session, error) {
	app, err := s.registry.GetUsable(ctx, appID)
	if err != nil {
		return Session{}, err
	}

	sess, err := s.issuer.Verify(sessionToken, app.AppID)
	if err != nil {
		return Session{}, err
	}

	// 세션 발급 후 차단됐을 수 있다. 세션 수명이 revocation 지연의 상한이지만
	// 레지스트리 차단은 즉시 반영한다.
	if app.UIDBlocked(sess.AppUserID) {
		return Session{}, platformerr.New(platformerr.CodeUserBlocked, "이용이 제한된 계정이에요")
	}
	return sess, nil
}

// DeleteCurrentUser는 사용자를 삭제한다.
//
// 앱이 계정을 삭제할 때 부른다. PII를 저장하지 않더라도 삭제 경로는 있어야 한다.
// ADR 0005 참고.
func (s *Service) DeleteCurrentUser(ctx context.Context, sess Session) error {
	return s.users.DeleteUser(ctx, sess.AppID, sess.AppUserID, sess.PlatformUserID)
}
