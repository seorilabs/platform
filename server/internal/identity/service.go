package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"slices"
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
	// KindAITLogin은 AppsInToss appLogin의 일회용 authorization code다.
	KindAITLogin CredentialKind = "ait-login"
	// KindAnonymous는 getAnonymousKey 해시다.
	//
	// bearer 자격증명이 아니라 클라이언트가 아무 값이나 보낼 수 있다.
	// 즉 타인 사칭이 가능하므로 IAP와 푸시 토큰 등록에서 금지한다.
	KindAnonymous CredentialKind = "anonymous"
)

// Credential은 세션 교환에 쓰는 자격증명이다.
type Credential struct {
	Kind     CredentialKind
	Value    string
	Referrer string
}

// TokenVerifier는 Firebase ID 토큰을 검증한다.
//
// 인터페이스를 여기에 두는 이유는 Service가 소비자이기 때문이다.
// FirebaseVerifier가 구현하고 테스트는 fake를 쓴다.
type TokenVerifier interface {
	Verify(ctx context.Context, token string, app registry.App) (Claims, error)
}

// Blocklist는 앱별 차단 계정 조회 포트다.
//
// 소비자인 이 패키지가 정의한다. blocklist.Service가 구현한다.
// 차단 목록이 레지스트리에서 빠져나온 이유는 ADR 0026에 있다.
type Blocklist interface {
	Blocked(ctx context.Context, appID, uid string) (bool, error)
}

// AITLoginVerifier는 일회용 appLogin authorization code를 mTLS로 교환한다.
// 반환값은 원문 userKey가 아니라 이미 SHA-256 처리된 앱 사용자 ID다.
type AITLoginVerifier interface {
	Verify(ctx context.Context, authorizationCode, referrer string) (hashedUserID string, err error)
}

// AppCheckVerifier는 Firebase App Check token을 앱 프로젝트에 묶어 검증한다.
// 인터페이스는 검증을 소비하는 Service 쪽에 둔다.
type AppCheckVerifier interface {
	Verify(ctx context.Context, token, firebaseProjectID string) error
}

// ClientInfo는 요청 헤더가 알려 주는 실행 환경이다.
//
// 권한이 아니라 관측값이다. 클라이언트가 값을 고르므로 신뢰 경계 안에서는 쓰지
// 않고 운영 이벤트에만 싣는다. 헤더를 보내지 않는 구버전 SDK가 있어 전부 선택이다.
type ClientInfo struct {
	// AppVersion은 X-Seori-AppVer다. 예 `1.2.4`.
	AppVersion string
	// Runtime은 X-Seori-Runtime이다. 예 `godot-native-android`, `ait-rn`, `web`.
	Runtime string
	// SDK는 X-Seori-Sdk다. 예 `gd/0.6.8`, `ts/0.4.0`.
	SDK string
}

// AppVersionObserver는 (앱, 런타임, 버전) 조합을 처음 본 순간을 한 번만 기록한다.
//
// 마켓 업로드도 태그도 아닌 "그 빌드로 실제 세션이 처음 열린 시각"이라
// 새 버전의 실유입 개시를 가른다.
type AppVersionObserver interface {
	ObserveAppVersion(ctx context.Context, appID string, client ClientInfo) error
}

// NewIdentity는 자격증명에서 확인한, 계정을 만들 때 원장과 운영 이벤트에 함께
// 남길 사실이다.
type NewIdentity struct {
	// UID는 앱 사용자 식별자다.
	UID string
	// Anonymous는 클라이언트가 값을 고를 수 있어 사칭이 되는 신원인지다.
	Anonymous bool
	// AuthType은 계정이 만들어진 인증 경로다.
	// firebase, firebase_bridge, apps_in_toss, anonymous 중 하나다.
	AuthType string
	// Referrer는 AppsInToss 로그인의 DEFAULT/SANDBOX 구분이고 다른 자격증명에서는
	// 비어 있다. 운영 이벤트에서 실서비스 유입과 샌드박스 테스트를 가른다.
	Referrer string
	// SignInProvider는 Firebase ID token의 sign_in_provider다. google.com,
	// apple.com, anonymous 같은 값이고 AuthType이 가리는 실제 로그인 수단이다.
	//
	// platform이 uid를 새로 만드는 bridge 게스트 경로에는 아직 로그인이 없어
	// 비어 있다. 없는 사실을 지어내지 않으려고 그때는 이벤트에도 싣지 않는다.
	SignInProvider string
	// Client는 이 계정을 만든 요청의 실행 환경이다. 헤더를 보내지 않는 구버전
	// 클라이언트에서는 비어 있다.
	Client ClientInfo
}

// UserRepository는 identity 저장소다.
type UserRepository interface {
	// EnsureUser는 (appID, identity.UID)에 대응하는 platform_user_id를 돌려준다.
	//
	// 없으면 만들고 있으면 기존 것을 쓴다. 동시 호출에도 하나만 만들어야 한다.
	// 여러 개가 만들어지면 같은 사람의 결제 원장이 갈라진다.
	EnsureUser(ctx context.Context, appID string, identity NewIdentity) (string, error)
	// LookupUser는 삭제 같은 멱등 경로에서 기존 매핑만 읽는다.
	// 매핑이 없을 때 새 사용자를 만들면 안 된다.
	LookupUser(ctx context.Context, appID, uid string) (platformUserID string, found bool, err error)

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
	SupportCode     string
	AppUserID       string
	IsAnonymous     bool
	IsLinkedAccount bool
	ExpiresIn       int
	ExpiresAt       time.Time
}

// FirebaseCustomTokenResult는 custom token bridge 응답이다.
// 토큰은 클라이언트가 즉시 Firebase signInWithCustomToken에 쓰고 저장하지 않는다.
type FirebaseCustomTokenResult struct {
	FirebaseCustomToken string
	AppUserID           string
	PlatformUserID      string
}

// Service는 세션 교환 유스케이스다.
type Service struct {
	registry         *registry.Registry
	verifier         TokenVerifier
	blocklist        Blocklist
	aitLogin         map[string]AITLoginVerifier
	users            UserRepository
	issuer           *SessionIssuer
	customTokens     CustomTokenIssuer
	appCheck         AppCheckVerifier
	accounts         AccountRepository
	accountProviders map[string]AccountProvider
	appVersions      AppVersionObserver
	refreshTTL       time.Duration
	now              func() time.Time
}

// WithAITLoginVerifiers는 appID별 AppsInToss 로그인 검증기를 등록한다.
//
// 검증기는 미니앱마다 다른 mTLS 인증서를 쥔다. 하나를 모든 앱에 쓰면 토스가
// 다른 미니앱의 인가코드로 보고 거부한다. 그래서 앱을 키로 들고 있는다.
func (s *Service) WithAITLoginVerifiers(verifiers map[string]AITLoginVerifier) *Service {
	s.aitLogin = verifiers
	return s
}

// NewService는 서비스를 만든다.
// blocklist는 선택 인자가 아니다. nil이면 차단이 조용히 풀린 채
// 배포되고, 그 상태는 로그에도 남지 않는다.
func NewService(
	reg *registry.Registry,
	verifier TokenVerifier,
	users UserRepository,
	issuer *SessionIssuer,
	blocked Blocklist,
) *Service {
	return &Service{
		registry:   reg,
		verifier:   verifier,
		users:      users,
		issuer:     issuer,
		blocklist:  blocked,
		refreshTTL: DefaultRefreshTTL,
		now:        time.Now,
	}
}

// ensureNotBlocked는 차단된 계정을 거른다.
//
// 조회 자체가 실패하면 그 에러를 그대로 올린다. 차단 여부를 모른 채
// 통과시키면 차단이 무의미해진다.
func (s *Service) ensureNotBlocked(ctx context.Context, appID, uid string) error {
	blocked, err := s.blocklist.Blocked(ctx, appID, uid)
	if err != nil {
		return err
	}
	if blocked {
		return platformerr.New(platformerr.CodeUserBlocked, "이용이 제한된 계정이에요")
	}
	return nil
}

// WithCustomTokenIssuer는 API role에만 custom token 원격 서명기를 연결한다.
func (s *Service) WithCustomTokenIssuer(issuer CustomTokenIssuer) *Service {
	s.customTokens = issuer
	return s
}

// WithAppVersionObserver는 세션 경로의 앱 버전 최초 관측을 연결한다.
//
// 연결하지 않으면 관측만 꺼지고 세션 발급은 그대로 동작한다.
func (s *Service) WithAppVersionObserver(observer AppVersionObserver) *Service {
	s.appVersions = observer
	return s
}

// observeAppVersion은 실패해도 요청을 막지 않는다.
//
// 이건 관측이지 인증이 아니다. Firestore 한 번 흔들렸다고 로그인이 막히면
// 얻는 것보다 잃는 게 크다. 대신 조용히 넘기지 않고 로그를 남긴다.
func (s *Service) observeAppVersion(ctx context.Context, appID string, client ClientInfo) {
	if s.appVersions == nil || client.AppVersion == "" {
		return
	}
	if err := s.appVersions.ObserveAppVersion(ctx, appID, client); err != nil {
		slog.WarnContext(ctx, "앱 버전 최초 관측 실패. 세션은 계속한다",
			"app_id", appID, "app_version", client.AppVersion, "err", err)
	}
}

// WithAppCheckVerifier는 공개 bootstrap 경로의 앱 증명을 연결한다.
func (s *Service) WithAppCheckVerifier(verifier AppCheckVerifier) *Service {
	s.appCheck = verifier
	return s
}

// VerifyAppCheck는 앱별 원장 스위치가 켜진 경우에만 App Check를 강제한다.
// 구버전 클라이언트를 끊지 않고 앱별로 관측 후 전환하기 위한 경계다.
func (s *Service) VerifyAppCheck(ctx context.Context, appID, token string) error {
	app, err := s.registry.GetUsable(ctx, appID)
	if err != nil {
		return err
	}
	if !app.RequireAppCheck {
		return nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return platformerr.New(
			platformerr.CodeAppCheckRequired,
			"앱 확인 token이 필요해요",
		)
	}
	if len(token) > 8192 {
		return platformerr.New(
			platformerr.CodeAppCheckInvalid,
			"앱 확인 token이 올바르지 않아요",
		)
	}
	if s.appCheck == nil {
		return platformerr.New(
			platformerr.CodePlatformUnavailable,
			"앱 확인 서비스를 사용할 수 없어요",
		)
	}
	if err := s.appCheck.Verify(ctx, token, app.FirebaseProjectID); err != nil {
		return platformerr.Wrap(
			err,
			platformerr.CodeAppCheckInvalid,
			"앱 확인 token이 올바르지 않아요",
		)
	}
	return nil
}

// CreateFirebaseCustomToken은 platform 신원을 Firebase custom token으로 잇는다.
//
// 기존 Firebase ID token이 있으면 검증한 uid를 그대로 써서 기존 Firestore 소유권을
// 보존한다. 토큰이 없으면 uid를 서버에서 생성한다. 클라이언트가 uid를 직접 고르는
// kind=anonymous 경로를 Firebase 권한 부여에 재사용하지 않는 것이 핵심이다.
func (s *Service) CreateFirebaseCustomToken(
	ctx context.Context,
	appID string,
	existingFirebaseIDToken string,
	client ClientInfo,
) (FirebaseCustomTokenResult, error) {
	app, err := s.registry.GetUsable(ctx, appID)
	if err != nil {
		return FirebaseCustomTokenResult{}, err
	}
	if !app.FeatureEnabled("firebase_custom_token_bridge") {
		return FirebaseCustomTokenResult{}, platformerr.New(
			platformerr.CodeAuthForbidden,
			"이 앱은 custom token bridge를 사용하지 않아요",
		)
	}
	if s.customTokens == nil {
		return FirebaseCustomTokenResult{}, platformerr.New(
			platformerr.CodePlatformUnavailable,
			"인증 bridge를 사용할 수 없어요",
		)
	}

	var uid, signInProvider string
	platformGuest := false
	if token := strings.TrimSpace(existingFirebaseIDToken); token != "" {
		if len(token) > 4096 {
			return FirebaseCustomTokenResult{}, platformerr.New(
				platformerr.CodeRequestInvalid,
				"기존 Firebase token이 너무 길어요",
			)
		}
		claims, verifyErr := s.verifier.Verify(ctx, token, app)
		if verifyErr != nil {
			return FirebaseCustomTokenResult{}, verifyErr
		}
		uid = claims.UID
		signInProvider = claims.SignInProvider
	} else {
		platformGuest = true
		uid, err = NewFirebaseBridgeUserID()
		if err != nil {
			return FirebaseCustomTokenResult{}, platformerr.Wrap(
				err,
				platformerr.CodePlatformUnavailable,
				"인증 사용자를 만들지 못했어요",
			)
		}
	}
	if err := s.ensureNotBlocked(ctx, app.AppID, uid); err != nil {
		return FirebaseCustomTokenResult{}, err
	}

	customToken, err := s.customTokens.Mint(ctx, app, uid, platformGuest)
	if err != nil {
		return FirebaseCustomTokenResult{}, platformerr.Wrap(
			err,
			platformerr.CodePlatformUnavailable,
			"Firebase 인증 토큰을 만들지 못했어요",
		)
	}
	platformUserID, err := s.users.EnsureUser(ctx, app.AppID, NewIdentity{
		UID:            uid,
		AuthType:       "firebase_bridge",
		SignInProvider: signInProvider,
		Client:         client,
	})
	if err != nil {
		return FirebaseCustomTokenResult{}, err
	}
	s.observeAppVersion(ctx, app.AppID, client)
	return FirebaseCustomTokenResult{
		FirebaseCustomToken: customToken,
		AppUserID:           uid,
		PlatformUserID:      platformUserID,
	}, nil
}

// DeleteFirebaseAccount는 검증된 Firebase uid의 Platform identity만 지운다.
// Firebase Auth와 앱 데이터는 해당 Firebase 프로젝트가 이어서 삭제하며,
// IAP 감사 원장은 기존 DeleteUser 계약에 따라 보존한다.
func (s *Service) DeleteFirebaseAccount(
	ctx context.Context,
	appID string,
	firebaseIDToken string,
) error {
	app, err := s.registry.GetUsable(ctx, appID)
	if err != nil {
		return err
	}
	if !app.FeatureEnabled("firebase_custom_token_bridge") {
		return platformerr.New(
			platformerr.CodeAuthForbidden,
			"이 앱은 Firebase account bridge를 사용하지 않아요",
		)
	}
	firebaseIDToken = strings.TrimSpace(firebaseIDToken)
	if firebaseIDToken == "" || len(firebaseIDToken) > 4096 {
		return platformerr.New(
			platformerr.CodeRequestInvalid,
			"Firebase token이 올바르지 않아요",
		)
	}
	claims, err := s.verifier.Verify(ctx, firebaseIDToken, app)
	if err != nil {
		return err
	}
	platformUserID, found, err := s.users.LookupUser(ctx, app.AppID, claims.UID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return s.users.DeleteUser(ctx, app.AppID, claims.UID, platformUserID)
}

// WithClock은 시계를 주입한다. 테스트용이다.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// CreateSession은 자격증명을 플랫폼 세션으로 교환한다.
func (s *Service) CreateSession(
	ctx context.Context,
	appID string,
	cred Credential,
	client ClientInfo,
) (Result, error) {
	app, err := s.registry.GetUsable(ctx, appID)
	if err != nil {
		return Result{}, err
	}

	identity, err := s.resolveIdentity(ctx, app, cred)
	if err != nil {
		return Result{}, err
	}
	identity.Client = client

	puid, err := s.users.EnsureUser(ctx, app.AppID, identity)
	if err != nil {
		return Result{}, err
	}
	s.observeAppVersion(ctx, app.AppID, client)
	linked := identity.AuthType == "apps_in_toss"
	if s.accounts != nil {
		storedLinked, linkErr := s.accounts.IsAccountLinked(ctx, app.AppID, puid)
		if linkErr != nil {
			return Result{}, linkErr
		}
		linked = linked || storedLinked
	}

	return s.issue(ctx, Session{
		PlatformUserID:  puid,
		AppID:           app.AppID,
		AppUserID:       identity.UID,
		IsAnonymous:     identity.Anonymous,
		IsLinkedAccount: linked,
	})
}

// resolveIdentity는 자격증명에서 계정을 만들 때 남길 사실을 얻는다.
func (s *Service) resolveIdentity(
	ctx context.Context,
	app registry.App,
	cred Credential,
) (NewIdentity, error) {
	value := strings.TrimSpace(cred.Value)
	if value == "" {
		return NewIdentity{}, platformerr.New(platformerr.CodeAuthRequired, "자격증명이 필요해요")
	}

	switch cred.Kind {
	case KindFirebaseIDToken:
		if strings.TrimSpace(cred.Referrer) != "" {
			return NewIdentity{}, platformerr.New(platformerr.CodeRequestInvalid, "Firebase 로그인에는 referrer를 넣을 수 없어요")
		}
		claims, err := s.verifier.Verify(ctx, value, app)
		if err != nil {
			return NewIdentity{}, err
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
		return NewIdentity{
			UID:            claims.UID,
			AuthType:       "firebase",
			SignInProvider: claims.SignInProvider,
		}, nil

	case KindAnonymous:
		if strings.TrimSpace(cred.Referrer) != "" {
			return NewIdentity{}, platformerr.New(platformerr.CodeRequestInvalid, "익명 로그인에는 referrer를 넣을 수 없어요")
		}
		// 사칭 가능한 신원이다. 여기서 막지 않고 세션에 표시만 한다.
		// IAP 같은 민감 경로가 EnsureNotAnonymous로 거부한다.
		// 이렇게 하는 이유는 RemoteConfig 조회와 이벤트 로그는
		// 익명으로도 허용해야 하기 때문이다.
		if err := s.ensureNotBlocked(ctx, app.AppID, value); err != nil {
			return NewIdentity{}, err
		}
		return NewIdentity{UID: "anon:" + value, Anonymous: true, AuthType: "anonymous"}, nil

	case KindAITLogin:
		// AppsInToss userKey는 광고와 IAP가 함께 쓰는 앱 범위 신원이다.
		// 어느 기능도 provider를 허용하지 않은 앱에서 임의로 열리면 안 된다.
		adsEnabled := app.FeatureEnabled("ads") && slices.Contains(app.Ads.Providers, "apps_in_toss")
		iapEnabled := app.FeatureEnabled("iap") && app.MarketEnabled("apps_in_toss")
		if !adsEnabled && !iapEnabled {
			return NewIdentity{}, platformerr.New(platformerr.CodeAuthForbidden, "이 앱은 AppsInToss 로그인을 사용하지 않아요")
		}
		referrer := strings.ToUpper(strings.TrimSpace(cred.Referrer))
		if referrer != "DEFAULT" && referrer != "SANDBOX" {
			return NewIdentity{}, platformerr.New(platformerr.CodeRequestInvalid, "AppsInToss referrer가 올바르지 않아요")
		}
		if len(s.aitLogin) == 0 {
			return NewIdentity{}, platformerr.New(platformerr.CodePlatformUnavailable, "AppsInToss 로그인이 준비되지 않았어요")
		}
		// 이 앱의 인증서가 없으면 다른 앱 인증서로 대신 교환하지 않는다.
		// 그렇게 하면 토스가 CN 불일치로 거부해 설정 오류가 인증 실패로 둔갑한다.
		verifier, ok := s.aitLogin[app.AppID]
		if !ok {
			return NewIdentity{}, platformerr.New(platformerr.CodeProviderConfigInvalid,
				"이 앱의 AppsInToss 로그인 인증서가 없어요")
		}
		uid, err := verifier.Verify(ctx, value, referrer)
		if err != nil {
			return NewIdentity{}, err
		}
		if !isSHA256(uid) {
			return NewIdentity{}, platformerr.New(platformerr.CodeProviderResponseInvalid, "AppsInToss 사용자 응답이 올바르지 않아요")
		}
		return NewIdentity{UID: "ait:" + uid, AuthType: "apps_in_toss", Referrer: referrer}, nil

	default:
		return NewIdentity{}, platformerr.Newf(platformerr.CodeRequestInvalid,
			"알 수 없는 자격증명 종류예요: %s", cred.Kind)
	}
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
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
	if err := s.ensureNotBlocked(ctx, app.AppID, sess.AppUserID); err != nil {
		return Result{}, err
	}
	if sess.IsLinkedAccount && s.accounts != nil {
		linked, err := s.accounts.IsAccountLinked(ctx, app.AppID, sess.PlatformUserID)
		if err != nil {
			return Result{}, err
		}
		sess.IsLinkedAccount = linked
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
		PlatformToken:   token,
		RefreshToken:    refresh,
		PlatformUserID:  sess.PlatformUserID,
		SupportCode:     NewSupportCode(sess.AppID, sess.PlatformUserID),
		AppUserID:       sess.AppUserID,
		IsAnonymous:     sess.IsAnonymous,
		IsLinkedAccount: sess.IsLinkedAccount,
		ExpiresIn:       int(s.issuer.TTL().Seconds()),
		ExpiresAt:       exp,
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
	// 차단은 캐시 TTL 안에 반영된다.
	if err := s.ensureNotBlocked(ctx, app.AppID, sess.AppUserID); err != nil {
		return Session{}, err
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
