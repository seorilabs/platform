package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// fakeSource는 고정 앱 목록을 준다.
type fakeSource struct{ apps []registry.App }

func (f fakeSource) LoadApps(context.Context) ([]registry.App, error) { return f.apps, nil }

// fakeVerifier는 토큰 문자열을 그대로 uid로 쓴다.
type fakeVerifier struct {
	err error
}

type fakeAITLoginVerifier struct {
	hashedUserID string
	err          error
	code         string
	referrer     string
}

func (f *fakeAITLoginVerifier) Verify(_ context.Context, code, referrer string) (string, error) {
	f.code = code
	f.referrer = referrer
	return f.hashedUserID, f.err
}

type fakeAppCheckVerifier struct {
	err       error
	token     string
	projectID string
	calls     int
}

func (f *fakeAppCheckVerifier) Verify(
	_ context.Context,
	token,
	firebaseProjectID string,
) error {
	f.calls++
	f.token = token
	f.projectID = firebaseProjectID
	return f.err
}

func (f fakeVerifier) Verify(_ context.Context, token string, _ registry.App) (Claims, error) {
	if f.err != nil {
		return Claims{}, f.err
	}
	return Claims{UID: token, SignInProvider: "anonymous", IsAnonymous: true}, nil
}

type fakeCustomTokenIssuer struct {
	token string
	err   error
	uid   string
	guest bool
}

func (f *fakeCustomTokenIssuer) Mint(
	_ context.Context,
	_ registry.App,
	uid string,
	platformGuest bool,
) (string, error) {
	f.uid = uid
	f.guest = platformGuest
	if f.err != nil {
		return "", f.err
	}
	return f.token, nil
}

// memRepo는 메모리 기반 저장소다.
//
// EnsureUser의 멱등성을 실제로 검증하려고 잠금을 건다.
// Firestore 트랜잭션이 하는 일을 흉내낸 것이다.
type memRepo struct {
	mu           sync.Mutex
	users        map[string]string // appID+uid → puid
	refresh      map[string]Session
	created      int // 새로 만든 횟수
	deleteOK     bool
	deleted      int
	lastReferrer string
}

func newMemRepo() *memRepo {
	return &memRepo{
		users:    map[string]string{},
		refresh:  map[string]Session{},
		deleteOK: true,
	}
}

func (m *memRepo) EnsureUser(
	_ context.Context,
	appID, uid string,
	_ bool,
	_, referrer string,
) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastReferrer = referrer

	key := appID + "\x00" + uid
	if p, ok := m.users[key]; ok {
		return p, nil
	}
	p, err := NewPlatformUserID()
	if err != nil {
		return "", err
	}
	m.users[key] = p
	m.created++
	return p, nil
}

func (m *memRepo) LookupUser(
	_ context.Context,
	appID, uid string,
) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.users[appID+"\x00"+uid]
	return p, ok, nil
}

func (m *memRepo) SaveRefresh(_ context.Context, token string, sess Session, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refresh[token] = sess
	return nil
}

func (m *memRepo) LoadRefresh(_ context.Context, token string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.refresh[token]
	if !ok {
		return Session{}, platformerr.New(platformerr.CodeRefreshInvalid, "없어요")
	}
	return s, nil
}

func (m *memRepo) DeleteRefresh(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.refresh, token)
	return nil
}

func (m *memRepo) DeleteUser(_ context.Context, appID, uid, _ string) error {
	if !m.deleteOK {
		return errors.New("삭제 실패")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.users, appID+"\x00"+uid)
	m.deleted++
	return nil
}

func newTestService(t *testing.T, verifier TokenVerifier, repo UserRepository) *Service {
	t.Helper()

	reg := registry.New(fakeSource{apps: []registry.App{testApp()}})
	issuer, err := NewSessionIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("발급기 생성 실패: %v", err)
	}
	return NewService(reg, verifier, repo, issuer)
}

func newBridgeTestService(
	t *testing.T,
	verifier TokenVerifier,
	repo UserRepository,
	customTokens CustomTokenIssuer,
) *Service {
	t.Helper()
	app := testApp()
	app.Features = map[string]bool{}
	app.Features["firebase_custom_token_bridge"] = true
	app.FirebaseCustomTokenServiceAccount = "platform-auth@lizard-tycoon.iam.gserviceaccount.com"
	reg := registry.New(fakeSource{apps: []registry.App{app}})
	issuer, err := NewSessionIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatalf("발급기 생성 실패: %v", err)
	}
	return NewService(reg, verifier, repo, issuer).WithCustomTokenIssuer(customTokens)
}

func TestCreateSession(t *testing.T) {
	repo := newMemRepo()
	svc := newTestService(t, fakeVerifier{}, repo)

	res, err := svc.CreateSession(context.Background(), "lizard-tycoon", Credential{
		Kind:  KindFirebaseIDToken,
		Value: "uid-abc",
	})
	if err != nil {
		t.Fatalf("세션 생성 실패: %v", err)
	}

	if res.PlatformUserID == "" {
		t.Error("platform_user_id가 비어 있다")
	}
	if res.PlatformToken == "" || res.RefreshToken == "" {
		t.Error("토큰이 비어 있다")
	}
	// firebase_uid에서 파생하지 않는다. ADR 0008
	if res.PlatformUserID == "uid-abc" {
		t.Error("platform_user_id가 firebase_uid와 같다")
	}

	// 발급한 세션 토큰이 즉시 검증된다
	sess, err := svc.Authenticate(context.Background(), "lizard-tycoon", res.PlatformToken)
	if err != nil {
		t.Fatalf("방금 발급한 세션 검증 실패: %v", err)
	}
	if sess.PlatformUserID != res.PlatformUserID {
		t.Errorf("puid = %q, want %q", sess.PlatformUserID, res.PlatformUserID)
	}
}

func TestCreateFirebaseCustomTokenPreservesExistingUID(t *testing.T) {
	repo := newMemRepo()
	customTokens := &fakeCustomTokenIssuer{token: "signed-custom-token"}
	svc := newBridgeTestService(t, fakeVerifier{}, repo, customTokens)

	result, err := svc.CreateFirebaseCustomToken(
		context.Background(),
		"lizard-tycoon",
		"existing-firebase-uid",
	)
	if err != nil {
		t.Fatalf("custom token bridge 실패: %v", err)
	}
	if result.AppUserID != "existing-firebase-uid" || customTokens.uid != result.AppUserID {
		t.Fatalf("기존 uid가 보존되지 않았다: result=%q signer=%q", result.AppUserID, customTokens.uid)
	}
	if result.FirebaseCustomToken != "signed-custom-token" {
		t.Fatalf("custom token = %q", result.FirebaseCustomToken)
	}
	if customTokens.guest {
		t.Fatal("기존 Firebase uid를 플랫폼 게스트로 표시했다")
	}
	if result.PlatformUserID == "" {
		t.Fatal("platform user 매핑이 생성되지 않았다")
	}
}

func TestVerifyAppCheck(t *testing.T) {
	t.Run("원장 스위치가 꺼져 있으면 token을 요구하지 않는다", func(t *testing.T) {
		verifier := &fakeAppCheckVerifier{}
		svc := newBridgeTestService(
			t,
			fakeVerifier{},
			newMemRepo(),
			&fakeCustomTokenIssuer{token: "signed-custom-token"},
		).WithAppCheckVerifier(verifier)

		if err := svc.VerifyAppCheck(context.Background(), "lizard-tycoon", ""); err != nil {
			t.Fatalf("App Check 비활성 검증 실패: %v", err)
		}
		if verifier.calls != 0 {
			t.Fatalf("검증 호출 = %d, want 0", verifier.calls)
		}
	})

	newRequiredService := func(t *testing.T, verifier AppCheckVerifier) *Service {
		t.Helper()
		app := testApp()
		app.Features = map[string]bool{"firebase_custom_token_bridge": true}
		app.FirebaseCustomTokenServiceAccount =
			"platform-auth@lizard-tycoon.iam.gserviceaccount.com"
		app.RequireAppCheck = true
		reg := registry.New(fakeSource{apps: []registry.App{app}})
		issuer, err := NewSessionIssuer(
			[]byte("0123456789abcdef0123456789abcdef"),
			time.Hour,
		)
		if err != nil {
			t.Fatalf("발급기 생성 실패: %v", err)
		}
		return NewService(reg, fakeVerifier{}, newMemRepo(), issuer).
			WithCustomTokenIssuer(&fakeCustomTokenIssuer{token: "signed-custom-token"}).
			WithAppCheckVerifier(verifier)
	}

	t.Run("필수 앱은 빈 token을 거부한다", func(t *testing.T) {
		svc := newRequiredService(t, &fakeAppCheckVerifier{})
		err := svc.VerifyAppCheck(context.Background(), "lizard-tycoon", "")
		if platformerr.CodeOf(err) != platformerr.CodeAppCheckRequired {
			t.Fatalf("code = %q, want %q", platformerr.CodeOf(err), platformerr.CodeAppCheckRequired)
		}
	})

	t.Run("잘못된 token은 일반화된 에러로 거부한다", func(t *testing.T) {
		svc := newRequiredService(t, &fakeAppCheckVerifier{err: errors.New("bad signature")})
		err := svc.VerifyAppCheck(context.Background(), "lizard-tycoon", "invalid")
		if platformerr.CodeOf(err) != platformerr.CodeAppCheckInvalid {
			t.Fatalf("code = %q, want %q", platformerr.CodeOf(err), platformerr.CodeAppCheckInvalid)
		}
	})

	t.Run("유효한 token은 Firebase 프로젝트에 묶어 검증한다", func(t *testing.T) {
		verifier := &fakeAppCheckVerifier{}
		svc := newRequiredService(t, verifier)
		if err := svc.VerifyAppCheck(
			context.Background(),
			"lizard-tycoon",
			" attested-token ",
		); err != nil {
			t.Fatalf("App Check 검증 실패: %v", err)
		}
		if verifier.token != "attested-token" || verifier.projectID != "lizard-tycoon" {
			t.Fatalf("검증 입력 = token %q, project %q", verifier.token, verifier.projectID)
		}
	})
}

func TestDeleteFirebaseAccount(t *testing.T) {
	repo := newMemRepo()
	if _, err := repo.EnsureUser(
		context.Background(),
		"lizard-tycoon",
		"firebase-user",
		false,
		"firebase",
		"",
	); err != nil {
		t.Fatalf("test user 생성 실패: %v", err)
	}
	svc := newBridgeTestService(
		t,
		fakeVerifier{},
		repo,
		&fakeCustomTokenIssuer{token: "signed-custom-token"},
	)

	if err := svc.DeleteFirebaseAccount(
		context.Background(),
		"lizard-tycoon",
		"firebase-user",
	); err != nil {
		t.Fatalf("Firebase account mapping 삭제 실패: %v", err)
	}
	if repo.deleted != 1 || len(repo.users) != 0 {
		t.Fatalf("삭제 결과 = deleted %d, users %d", repo.deleted, len(repo.users))
	}
	if err := svc.DeleteFirebaseAccount(
		context.Background(),
		"lizard-tycoon",
		"firebase-user",
	); err != nil {
		t.Fatalf("이미 삭제된 Firebase account 재삭제 실패: %v", err)
	}
	if repo.created != 1 || repo.deleted != 1 {
		t.Fatalf("재삭제 결과 = created %d, deleted %d", repo.created, repo.deleted)
	}
}

func TestCreateFirebaseCustomTokenGeneratesServerUID(t *testing.T) {
	customTokens := &fakeCustomTokenIssuer{token: "signed-custom-token"}
	svc := newBridgeTestService(t, fakeVerifier{}, newMemRepo(), customTokens)

	result, err := svc.CreateFirebaseCustomToken(context.Background(), "lizard-tycoon", "")
	if err != nil {
		t.Fatalf("custom token bridge 실패: %v", err)
	}
	if len(result.AppUserID) != len(firebaseBridgeUserPrefix)+26 ||
		result.AppUserID[:len(firebaseBridgeUserPrefix)] != firebaseBridgeUserPrefix {
		t.Fatalf("server uid 형식이 올바르지 않다: %q", result.AppUserID)
	}
	if customTokens.uid != result.AppUserID {
		t.Fatalf("서명 uid = %q, want %q", customTokens.uid, result.AppUserID)
	}
	if !customTokens.guest {
		t.Fatal("플랫폼이 새로 만든 Firebase uid에 게스트 claim이 없다")
	}
}

func TestCreateFirebaseCustomTokenFailsClosed(t *testing.T) {
	t.Run("feature가 꺼진 앱", func(t *testing.T) {
		svc := newTestService(t, fakeVerifier{}, newMemRepo()).WithCustomTokenIssuer(
			&fakeCustomTokenIssuer{token: "unused"},
		)
		_, err := svc.CreateFirebaseCustomToken(context.Background(), "lizard-tycoon", "")
		if code := platformerr.CodeOf(err); code != platformerr.CodeAuthForbidden {
			t.Fatalf("code = %q, want auth_forbidden", code)
		}
	})

	t.Run("원격 서명 실패", func(t *testing.T) {
		svc := newBridgeTestService(
			t,
			fakeVerifier{},
			newMemRepo(),
			&fakeCustomTokenIssuer{err: errors.New("signJwt denied")},
		)
		_, err := svc.CreateFirebaseCustomToken(context.Background(), "lizard-tycoon", "")
		if code := platformerr.CodeOf(err); code != platformerr.CodePlatformUnavailable {
			t.Fatalf("code = %q, want platform_unavailable", code)
		}
	})
}

// 같은 uid로 동시에 100번 호출해도 platform_user_id는 하나여야 한다.
// P1의 필수 게이트다. 여러 개가 만들어지면 결제 원장이 갈라진다.
func TestCreateSessionIsIdempotent(t *testing.T) {
	repo := newMemRepo()
	svc := newTestService(t, fakeVerifier{}, repo)

	const n = 100
	ids := make([]string, n)
	var wg sync.WaitGroup

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := svc.CreateSession(context.Background(), "lizard-tycoon", Credential{
				Kind:  KindFirebaseIDToken,
				Value: "동시요청-uid",
			})
			if err != nil {
				t.Errorf("세션 생성 실패: %v", err)
				return
			}
			ids[i] = res.PlatformUserID
		}(i)
	}
	wg.Wait()

	first := ids[0]
	for i, id := range ids {
		if id != first {
			t.Fatalf("%d번째 platform_user_id가 다르다: %q vs %q", i, id, first)
		}
	}
	if repo.created != 1 {
		t.Errorf("사용자를 %d번 만들었다. 1번이어야 한다", repo.created)
	}
}

func TestCreateSessionRejectsUnknownApp(t *testing.T) {
	svc := newTestService(t, fakeVerifier{}, newMemRepo())

	_, err := svc.CreateSession(context.Background(), "없는-앱", Credential{
		Kind:  KindFirebaseIDToken,
		Value: "uid",
	})
	if code := platformerr.CodeOf(err); code != platformerr.CodeAppUnknown {
		t.Errorf("code = %q, want app_unknown", code)
	}
}

func TestCreateSessionRejectsPausedApp(t *testing.T) {
	paused := testApp()
	paused.Status = registry.StatusPaused

	reg := registry.New(fakeSource{apps: []registry.App{paused}})
	issuer, _ := NewSessionIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	svc := NewService(reg, fakeVerifier{}, newMemRepo(), issuer)

	_, err := svc.CreateSession(context.Background(), "lizard-tycoon", Credential{
		Kind:  KindFirebaseIDToken,
		Value: "uid",
	})
	if code := platformerr.CodeOf(err); code != platformerr.CodeAppPaused {
		t.Errorf("code = %q, want app_paused", code)
	}
}

func TestCredentialKinds(t *testing.T) {
	svc := newTestService(t, fakeVerifier{}, newMemRepo())
	ctx := context.Background()

	t.Run("anonymous는 세션을 받되 익명으로 표시된다", func(t *testing.T) {
		res, err := svc.CreateSession(ctx, "lizard-tycoon", Credential{
			Kind:  KindAnonymous,
			Value: "anon-key-hash",
		})
		if err != nil {
			t.Fatalf("익명 세션 생성 실패: %v", err)
		}
		if !res.IsAnonymous {
			t.Error("IsAnonymous = false, want true")
		}

		// 익명 세션은 IAP 같은 민감 경로에서 거부된다
		sess, err := svc.Authenticate(ctx, "lizard-tycoon", res.PlatformToken)
		if err != nil {
			t.Fatalf("세션 검증 실패: %v", err)
		}
		if err := sess.EnsureNotAnonymous(); err == nil {
			t.Error("익명 세션이 민감 경로를 통과했다")
		} else if code := platformerr.CodeOf(err); code != platformerr.CodeAnonymousNotAllowed {
			t.Errorf("code = %q, want anonymous_not_allowed", code)
		}
	})

	t.Run("ait-login은 아직 받지 않는다", func(t *testing.T) {
		// 토스 서버 검증 API가 확인되지 않았다.
		// 검증 없이 받으면 클라이언트가 보낸 값을 그대로 신뢰하게 된다.
		_, err := svc.CreateSession(ctx, "lizard-tycoon", Credential{
			Kind:  KindAITLogin,
			Value: "ait-token",
		})
		if err == nil {
			t.Fatal("검증 경로가 없는데 통과시켰다")
		}
	})

	t.Run("알 수 없는 종류는 거부", func(t *testing.T) {
		_, err := svc.CreateSession(ctx, "lizard-tycoon", Credential{
			Kind:  "made-up",
			Value: "x",
		})
		if code := platformerr.CodeOf(err); code != platformerr.CodeRequestInvalid {
			t.Errorf("code = %q, want request_invalid", code)
		}
	})

	t.Run("빈 값은 거부", func(t *testing.T) {
		_, err := svc.CreateSession(ctx, "lizard-tycoon", Credential{
			Kind:  KindFirebaseIDToken,
			Value: "   ",
		})
		if code := platformerr.CodeOf(err); code != platformerr.CodeAuthRequired {
			t.Errorf("code = %q, want auth_required", code)
		}
	})
}

func TestAITLoginAllowsAppsInTossAdsAndStoresOnlyHashedIdentity(t *testing.T) {
	app := testApp()
	app.AppID = "happy-farm"
	app.Features = map[string]bool{"ads": true}
	app.Ads = registry.AdsConfig{
		Providers: []string{"apps_in_toss"},
		Placements: []registry.AdsPlacementConfig{{
			ID: "harvest_boost", Format: "rewarded", DailyLimit: 20, CooldownSeconds: 30,
			Reward:    &registry.AdsRewardConfig{Key: "harvest_boost", MinAmount: 1, MaxAmount: 1},
			Providers: map[string]registry.AdsProviderConfig{"apps_in_toss": {AdGroupID: "test-group"}},
		}},
	}
	reg := registry.New(fakeSource{apps: []registry.App{app}})
	issuer, _ := NewSessionIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	repo := newMemRepo()
	verifier := &fakeAITLoginVerifier{hashedUserID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	svc := NewService(reg, fakeVerifier{}, repo, issuer).WithAITLoginVerifier(verifier)

	res, err := svc.CreateSession(context.Background(), app.AppID, Credential{
		Kind: KindAITLogin, Value: "one-time-authorization-code", Referrer: "sandbox",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsAnonymous || verifier.code != "one-time-authorization-code" || verifier.referrer != "SANDBOX" {
		t.Fatalf("result=%+v verifier=%+v", res, verifier)
	}
	if _, ok := repo.users[app.AppID+"\x00ait:"+verifier.hashedUserID]; !ok {
		t.Fatalf("해시된 AIT identity가 저장되지 않았다: %v", repo.users)
	}
	// 운영 이벤트가 실서비스 유입과 샌드박스 테스트를 가르려면 정규화된
	// referrer가 저장소까지 내려가야 한다.
	if repo.lastReferrer != "SANDBOX" {
		t.Fatalf("referrer가 저장소로 전달되지 않았다: %q", repo.lastReferrer)
	}
	for key := range repo.users {
		if strings.Contains(key, "one-time-authorization-code") {
			t.Fatal("authorization code 원문이 저장됐다")
		}
	}

	_, err = svc.CreateSession(context.Background(), app.AppID, Credential{
		Kind: KindAITLogin, Value: "another-code", Referrer: "unknown",
	})
	if platformerr.CodeOf(err) != platformerr.CodeRequestInvalid {
		t.Fatalf("invalid referrer code=%q", platformerr.CodeOf(err))
	}
}

func TestAITLoginAllowsAppsInTossIAPWithoutAds(t *testing.T) {
	app := testApp()
	app.Features = map[string]bool{"iap": true, "ads": false}
	app.IAP = registry.IAPConfig{
		LedgerEnvironment: "production",
		Markets:           []string{"apps_in_toss"},
		EntitlementIDs:    []string{"premium_species"},
	}
	reg := registry.New(fakeSource{apps: []registry.App{app}})
	issuer, _ := NewSessionIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	verifier := &fakeAITLoginVerifier{
		hashedUserID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	svc := NewService(reg, fakeVerifier{}, newMemRepo(), issuer).WithAITLoginVerifier(verifier)

	_, err := svc.CreateSession(context.Background(), app.AppID, Credential{
		Kind: KindAITLogin, Value: "iap-authorization-code", Referrer: "SANDBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	if verifier.code != "iap-authorization-code" || verifier.referrer != "SANDBOX" {
		t.Fatalf("verifier=%+v", verifier)
	}
}

func TestAITLoginRejectsAdMobOnlyApp(t *testing.T) {
	app := testApp()
	app.AppID = "slotmachine-game"
	app.Features = map[string]bool{"ads": true}
	app.Ads = registry.AdsConfig{
		Providers: []string{"admob"},
		Placements: []registry.AdsPlacementConfig{{
			ID: "ad_win", Format: "rewarded", DailyLimit: 2, CooldownSeconds: 60,
			Reward: &registry.AdsRewardConfig{Key: "credits", MinAmount: 1, MaxAmount: 1},
			Providers: map[string]registry.AdsProviderConfig{
				"admob": {AndroidAdUnitID: "ca-app-pub-1234567890123456/1234567890"},
			},
		}},
	}
	reg := registry.New(fakeSource{apps: []registry.App{app}})
	issuer, _ := NewSessionIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	verifier := &fakeAITLoginVerifier{
		hashedUserID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	svc := NewService(reg, fakeVerifier{}, newMemRepo(), issuer).WithAITLoginVerifier(verifier)

	_, err := svc.CreateSession(context.Background(), app.AppID, Credential{
		Kind: KindAITLogin, Value: "must-not-be-exchanged", Referrer: "DEFAULT",
	})
	if code := platformerr.CodeOf(err); code != platformerr.CodeAuthForbidden {
		t.Fatalf("code=%q, want auth_forbidden", code)
	}
	if verifier.code != "" {
		t.Fatalf("AdMob 전용 앱의 authorization code를 교환했다: %q", verifier.code)
	}
}

// 갱신 토큰은 한 번 쓰면 폐기되고 새로 발급된다. 회전이다.
func TestRefreshRotatesToken(t *testing.T) {
	repo := newMemRepo()
	svc := newTestService(t, fakeVerifier{}, repo)
	ctx := context.Background()

	first, err := svc.CreateSession(ctx, "lizard-tycoon", Credential{
		Kind: KindFirebaseIDToken, Value: "uid-refresh",
	})
	if err != nil {
		t.Fatalf("세션 생성 실패: %v", err)
	}

	second, err := svc.Refresh(ctx, "lizard-tycoon", first.RefreshToken)
	if err != nil {
		t.Fatalf("갱신 실패: %v", err)
	}

	if second.RefreshToken == first.RefreshToken {
		t.Error("갱신 토큰이 회전되지 않았다")
	}
	if second.PlatformUserID != first.PlatformUserID {
		t.Error("갱신 후 platform_user_id가 바뀌었다")
	}

	// 옛 토큰은 더 이상 쓸 수 없다
	if _, err := svc.Refresh(ctx, "lizard-tycoon", first.RefreshToken); err == nil {
		t.Error("폐기된 갱신 토큰이 다시 통과했다")
	}
}

// 다른 앱의 세션 토큰으로 이 앱에 접근할 수 없다.
func TestAuthenticateRejectsCrossAppToken(t *testing.T) {
	other := testApp()
	other.AppID = "other-app"
	other.FirebaseProjectID = "other-project"

	reg := registry.New(fakeSource{apps: []registry.App{testApp(), other}})
	issuer, _ := NewSessionIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	svc := NewService(reg, fakeVerifier{}, newMemRepo(), issuer)
	ctx := context.Background()

	res, err := svc.CreateSession(ctx, "lizard-tycoon", Credential{
		Kind: KindFirebaseIDToken, Value: "uid",
	})
	if err != nil {
		t.Fatalf("세션 생성 실패: %v", err)
	}

	if _, err := svc.Authenticate(ctx, "other-app", res.PlatformToken); err == nil {
		t.Fatal("다른 앱의 세션 토큰을 통과시켰다")
	}
}

func TestAuthenticateRejectsBlockedUID(t *testing.T) {
	repo := newMemRepo()
	svc := newTestService(t, fakeVerifier{}, repo)
	ctx := context.Background()

	res, err := svc.CreateSession(ctx, "lizard-tycoon", Credential{
		Kind: KindFirebaseIDToken, Value: "곧-차단될-uid",
	})
	if err != nil {
		t.Fatalf("세션 생성 실패: %v", err)
	}

	// 세션 발급 후에 차단한다. 세션 수명이 남아 있어도 즉시 막혀야 한다.
	blocked := testApp()
	blocked.BlockedUIDs = []string{"곧-차단될-uid"}
	svc.registry = registry.New(fakeSource{apps: []registry.App{blocked}})

	_, err = svc.Authenticate(ctx, "lizard-tycoon", res.PlatformToken)
	if code := platformerr.CodeOf(err); code != platformerr.CodeUserBlocked {
		t.Errorf("code = %q, want user_blocked", code)
	}
}

func TestSupportCode(t *testing.T) {
	tests := []struct {
		appID string
		want  string
	}{
		{"lizard-tycoon", "LT"},
		{"happy-farm", "HF"},
		{"crossword-puzzle", "CP"},
		{"foam-party", "FP"},
	}
	for _, tt := range tests {
		if got := supportPrefix(tt.appID); got != tt.want {
			t.Errorf("supportPrefix(%q) = %q, want %q", tt.appID, got, tt.want)
		}
	}

	code := SupportCode("LT", "pu_01JXYZABCDEFGH12345678")
	if len(code) != len("LT-")+8 {
		t.Errorf("SupportCode 길이 = %d (%q)", len(code), code)
	}
}

func TestNewSessionIssuerRejectsShortSecret(t *testing.T) {
	if _, err := NewSessionIssuer([]byte("짧음"), time.Hour); err == nil {
		t.Fatal("짧은 비밀키를 받아들였다")
	}
}

// Firebase 익명 로그인은 결제를 막는 "익명"이 아니다.
//
// 이 플래그가 막는 것은 사칭 가능한 신원이지 계정이 없는 사용자가
// 아니다. Firebase 익명 계정도 서명된 ID 토큰을 받고 우리가 서명·aud·
// iss·exp를 전부 검증한다. 다른 사람의 uid를 주장할 수 없다.
//
// 둘을 묶으면 lizard-tycoon은 결제가 하나도 되지 않는다. 전 사용자가
// Firebase 익명 계정이기 때문이다.
func TestFirebaseAnonymousCanPay(t *testing.T) {
	// fakeVerifier가 sign_in_provider를 anonymous로 준다.
	svc := newTestService(t, fakeVerifier{}, newMemRepo())
	ctx := context.Background()

	res, err := svc.CreateSession(ctx, "lizard-tycoon", Credential{
		Kind:  KindFirebaseIDToken,
		Value: "firebase-uid-1",
	})
	if err != nil {
		t.Fatalf("세션 생성 실패: %v", err)
	}
	if res.IsAnonymous {
		t.Error("Firebase 익명 계정이 결제 차단 대상으로 표시됐다")
	}

	sess, err := svc.Authenticate(ctx, "lizard-tycoon", res.PlatformToken)
	if err != nil {
		t.Fatalf("세션 검증 실패: %v", err)
	}
	if err := sess.EnsureNotAnonymous(); err != nil {
		t.Errorf("Firebase 익명 계정이 결제 경로에서 막혔다: %v", err)
	}
}

// 반대편은 그대로 막혀 있어야 한다.
//
// getAnonymousKey 해시는 클라이언트가 아무 값이나 보낼 수 있어
// 타인 사칭이 된다. 위 완화가 이쪽까지 번지면 안 된다.
func TestAnonymousKeyStillCannotPay(t *testing.T) {
	svc := newTestService(t, fakeVerifier{}, newMemRepo())
	ctx := context.Background()

	res, err := svc.CreateSession(ctx, "lizard-tycoon", Credential{
		Kind:  KindAnonymous,
		Value: "anon-key-hash",
	})
	if err != nil {
		t.Fatalf("익명 세션 생성 실패: %v", err)
	}
	if !res.IsAnonymous {
		t.Fatal("사칭 가능한 신원이 결제 가능으로 표시됐다")
	}

	sess, err := svc.Authenticate(ctx, "lizard-tycoon", res.PlatformToken)
	if err != nil {
		t.Fatalf("세션 검증 실패: %v", err)
	}
	if err := sess.EnsureNotAnonymous(); err == nil {
		t.Error("사칭 가능한 신원이 결제 경로를 통과했다")
	}
}
