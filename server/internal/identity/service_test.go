package identity

import (
	"context"
	"errors"
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

func (f fakeVerifier) Verify(_ context.Context, token string, _ registry.App) (Claims, error) {
	if f.err != nil {
		return Claims{}, f.err
	}
	return Claims{UID: token, SignInProvider: "anonymous", IsAnonymous: true}, nil
}

// memRepo는 메모리 기반 저장소다.
//
// EnsureUser의 멱등성을 실제로 검증하려고 잠금을 건다.
// Firestore 트랜잭션이 하는 일을 흉내낸 것이다.
type memRepo struct {
	mu       sync.Mutex
	users    map[string]string // appID+uid → puid
	refresh  map[string]Session
	created  int // 새로 만든 횟수
	deleteOK bool
}

func newMemRepo() *memRepo {
	return &memRepo{
		users:    map[string]string{},
		refresh:  map[string]Session{},
		deleteOK: true,
	}
}

func (m *memRepo) EnsureUser(_ context.Context, appID, uid string, _ bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

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
