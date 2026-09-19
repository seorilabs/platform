package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/remoteconfig"
)

// fakeOverlay는 세션 응답에 얹을 설정을 흉내낸다.
type fakeOverlay struct {
	resolved remoteconfig.Resolved
	etag     string
	err      error

	targets []remoteconfig.Target
	apps    []string
}

func (f *fakeOverlay) ResolveFor(
	_ context.Context,
	app registry.App,
	t remoteconfig.Target,
) (remoteconfig.Resolved, string, error) {
	f.apps = append(f.apps, app.AppID)
	f.targets = append(f.targets, t)
	if f.err != nil {
		return remoteconfig.Resolved{}, "", f.err
	}
	return f.resolved, f.etag, nil
}

func blockedOverlay() *fakeOverlay {
	return &fakeOverlay{
		resolved: remoteconfig.Resolved{
			Values:   map[string]any{},
			Features: map[string]bool{"config": true},
			SDK: remoteconfig.SDKStatus{
				Status:             remoteconfig.SDKStatusBlocked,
				Message:            "업데이트가 필요해요",
				UpdateURL:          "https://play.google.com/store/apps/details?id=com.a.b",
				RecommendedVersion: "1.5.0",
			},
			Maint: remoteconfig.Maintenance{},
		},
		etag: `W/"abc123"`,
	}
}

func TestSessionCarriesConfigOverlay(t *testing.T) {
	svc := newTestService(t, fakeVerifier{}, newMemRepo())
	overlay := blockedOverlay()
	svc.WithConfigOverlay(overlay)

	res, err := svc.CreateSession(
		context.Background(), "lizard-tycoon", Credential{Kind: KindFirebaseIDToken, Value: "uid-overlay"},
		ClientInfo{AppVersion: "1.4.0", Runtime: "godot-native-android", SDK: "gd/0.6.8"},
	)
	if err != nil {
		t.Fatalf("세션 발급 실패: %v", err)
	}
	if !res.HasConfig {
		t.Fatal("설정이 동봉되지 않았다")
	}
	if res.Config.SDK.Status != remoteconfig.SDKStatusBlocked {
		t.Errorf("sdk.status = %q", res.Config.SDK.Status)
	}
	if res.ConfigETag != `W/"abc123"` {
		t.Errorf("configEtag = %q", res.ConfigETag)
	}
	// 런타임에서 플랫폼을 유도해 넘겨야 정책이 걸린다.
	if len(overlay.targets) != 1 || overlay.targets[0].Platform != remoteconfig.PlatformAndroid {
		t.Errorf("target = %+v", overlay.targets)
	}
	if overlay.targets[0].AppVersion != "1.4.0" {
		t.Errorf("target.appVersion = %q", overlay.targets[0].AppVersion)
	}
	// 레지스트리 항목을 그대로 넘겨야 기능 플래그와 스토어 주소가 함께 간다.
	if len(overlay.apps) != 1 || overlay.apps[0] != "lizard-tycoon" {
		t.Errorf("app = %v", overlay.apps)
	}
}

// 설정 조회 실패가 로그인을 막으면 얻는 것보다 잃는 게 크다.
func TestSessionSurvivesConfigOverlayFailure(t *testing.T) {
	svc := newTestService(t, fakeVerifier{}, newMemRepo())
	svc.WithConfigOverlay(&fakeOverlay{err: errors.New("Firestore가 흔들렸다")})

	res, err := svc.CreateSession(
		context.Background(), "lizard-tycoon", Credential{Kind: KindFirebaseIDToken, Value: "uid-overlay"},
		ClientInfo{AppVersion: "1.4.0", Runtime: "godot-native-android"},
	)
	if err != nil {
		t.Fatalf("설정 실패가 세션을 막았다: %v", err)
	}
	if res.PlatformToken == "" {
		t.Fatal("세션 토큰이 없다")
	}
	// 빈 값을 채워 넣지 않는다. 클라이언트가 그걸 유효한 판정으로 오해한다.
	if res.HasConfig {
		t.Fatal("실패했는데 설정이 동봉됐다")
	}
	if res.Config.SDK.Status != "" || res.ConfigETag != "" {
		t.Errorf("실패 후 잔여 값 = %+v etag=%q", res.Config.SDK, res.ConfigETag)
	}
}

// 오버레이를 연결하지 않은 배포는 지금과 완전히 같아야 한다.
func TestSessionWithoutConfigOverlay(t *testing.T) {
	svc := newTestService(t, fakeVerifier{}, newMemRepo())

	res, err := svc.CreateSession(
		context.Background(), "lizard-tycoon", Credential{Kind: KindFirebaseIDToken, Value: "uid-overlay"},
		ClientInfo{AppVersion: "1.4.0", Runtime: "godot-native-android"},
	)
	if err != nil {
		t.Fatalf("세션 발급 실패: %v", err)
	}
	if res.HasConfig {
		t.Fatal("오버레이가 없는데 설정이 동봉됐다")
	}
}

// 앱이 오래 떠 있으면 부팅 응답의 설정이 낡는다. 갱신이 새 값을 받는
// 유일한 정기 경로다.
func TestRefreshCarriesConfigOverlay(t *testing.T) {
	svc := newTestService(t, fakeVerifier{}, newMemRepo())
	overlay := blockedOverlay()
	svc.WithConfigOverlay(overlay)

	first, err := svc.CreateSession(
		context.Background(), "lizard-tycoon", Credential{Kind: KindFirebaseIDToken, Value: "uid-overlay"},
		ClientInfo{AppVersion: "1.4.0", Runtime: "godot-native-android"},
	)
	if err != nil {
		t.Fatalf("세션 발급 실패: %v", err)
	}

	second, err := svc.Refresh(
		context.Background(), "lizard-tycoon", first.RefreshToken,
		ClientInfo{AppVersion: "1.4.0", Runtime: "rn-ios"},
	)
	if err != nil {
		t.Fatalf("갱신 실패: %v", err)
	}
	if !second.HasConfig {
		t.Fatal("갱신 응답에 설정이 없다")
	}
	// 갱신 시점의 런타임을 쓴다. 발급 때 값을 재사용하면 플랫폼이 바뀐
	// 클라이언트가 엉뚱한 정책에 걸린다.
	last := overlay.targets[len(overlay.targets)-1]
	if last.Platform != remoteconfig.PlatformIOS {
		t.Errorf("갱신 target = %+v", last)
	}
}

// 런타임 헤더가 없으면 플랫폼을 모르므로 어떤 정책도 걸리지 않아야 한다.
func TestSessionOverlayWithoutRuntime(t *testing.T) {
	svc := newTestService(t, fakeVerifier{}, newMemRepo())
	overlay := blockedOverlay()
	svc.WithConfigOverlay(overlay)

	if _, err := svc.CreateSession(
		context.Background(), "lizard-tycoon", Credential{Kind: KindFirebaseIDToken, Value: "uid-overlay"}, ClientInfo{},
	); err != nil {
		t.Fatalf("세션 발급 실패: %v", err)
	}
	if len(overlay.targets) != 1 {
		t.Fatalf("호출 %d회", len(overlay.targets))
	}
	if overlay.targets[0].Platform != "" || overlay.targets[0].AppVersion != "" {
		t.Errorf("target = %+v, want 빈 값", overlay.targets[0])
	}
}
