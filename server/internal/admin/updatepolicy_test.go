package admin

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/remoteconfig"
)

// errObservationsUnavailable은 관측 원장이 조립되지 않은 상태를 흉내낸다.
var errObservationsUnavailable = errors.New("관측 원장이 준비되지 않았어요")

const (
	updatePolicyPath = "/v1/admin/config/update-policy"
	testPlayURL      = "https://play.google.com/store/apps/details?id=com.seorilabs.happyfarm"
	testAppStoreURL  = "https://apps.apple.com/app/id1234567890"
)

func storeApp() *fakeApps {
	return &fakeApps{app: registry.App{
		AppID:    "happy-farm",
		Status:   registry.StatusActive,
		Features: map[string]bool{"config": true},
		Store: registry.StoreConfig{
			GooglePlayURL: testPlayURL,
			AppStoreURL:   testAppStoreURL,
		},
	}}
}

func observedAndroid(now time.Time, versions ...string) []remoteconfig.ObservedAppVersion {
	out := make([]remoteconfig.ObservedAppVersion, 0, len(versions))
	for _, version := range versions {
		out = append(out, remoteconfig.ObservedAppVersion{
			Version:     version,
			Runtime:     "godot-native-android",
			FirstSeenAt: now.Add(-72 * time.Hour),
		})
	}
	return out
}

func newUpdatePolicyHandler(
	t *testing.T,
	cfg *fakeConfig,
	apps *fakeApps,
	auditor *fakeAuditor,
) *Handler {
	t.Helper()
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatalf("인증기 생성 실패: %v", err)
	}
	h, err := NewHandler(
		&fakeLedger{}, cfg, &fakeUsers{}, apps, &fakeCatalog{allowed: true}, auth, auditor,
	)
	if err != nil {
		t.Fatalf("핸들러 생성 실패: %v", err)
	}
	return h.WithClock(func() time.Time {
		return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	})
}

func blockBody(confirmation string, versions ...string) string {
	quoted := make([]string, 0, len(versions))
	for _, version := range versions {
		quoted = append(quoted, `"`+version+`"`)
	}
	return `{"appId":"happy-farm","platforms":{"android":{"blockedVersions":[` +
		strings.Join(quoted, ",") + `]}},"confirmation":"` + confirmation + `"}`
}

// 권장 안내는 확인 문구도 관측 가드도 없이 걸린다. 이게 기본 모드다.
func TestUpdatePolicyRecommendNeedsNoConfirmation(t *testing.T) {
	cfg := &fakeConfig{}
	auditor := &fakeAuditor{}
	h := newUpdatePolicyHandler(t, cfg, storeApp(), auditor)

	body := `{"appId":"happy-farm","platforms":{"android":{"blockedVersions":[]}}}`
	w := serve(t, h, http.MethodPost, updatePolicyPath, body, "tok", "syous")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(cfg.policyCalls) != 1 {
		t.Fatalf("저장 호출 %d회", len(cfg.policyCalls))
	}
	if cfg.policyCalls[0].actor != "syous" {
		t.Errorf("actor = %q", cfg.policyCalls[0].actor)
	}
	if len(auditor.records) != 1 || auditor.records[0].outcome != "cleared" {
		t.Errorf("감사 기록 = %+v", auditor.records)
	}
}

func TestUpdatePolicyBlockGuards(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		apps     *fakeApps
		observed []remoteconfig.ObservedAppVersion
		body     string
		wantCode string
	}{
		{
			name:     "관측되지 않은 버전은 막을 수 없다",
			apps:     storeApp(),
			observed: observedAndroid(now, "1.5.0", "1.4.0"),
			body:     blockBody("BLOCK happy-farm android 1.9.9", "1.9.9"),
			wantCode: "update_version_unknown",
		},
		{
			name:     "관측된 빌드가 하나도 없으면 막을 수 없다",
			apps:     storeApp(),
			observed: nil,
			body:     blockBody("BLOCK happy-farm android 1.4.0", "1.4.0"),
			wantCode: "update_version_unknown",
		},
		{
			name:     "더 높은 관측 버전이 없으면 갈 곳이 없다",
			apps:     storeApp(),
			observed: observedAndroid(now, "1.4.0", "1.5.0"),
			body:     blockBody("BLOCK happy-farm android 1.5.0", "1.5.0"),
			wantCode: "update_policy_invalid",
		},
		{
			name:     "관측된 모든 버전을 막으면 전원 차단이다",
			apps:     storeApp(),
			observed: observedAndroid(now, "1.4.0", "1.5.0"),
			body: blockBody(
				"BLOCK happy-farm android 1.4.0; BLOCK happy-farm android 1.5.0",
				"1.4.0", "1.5.0",
			),
			wantCode: "update_policy_invalid",
		},
		{
			name: "스토어 주소가 없으면 갈 곳 없는 차단 화면이 된다",
			apps: &fakeApps{app: registry.App{
				AppID:    "happy-farm",
				Status:   registry.StatusActive,
				Features: map[string]bool{"config": true},
			}},
			observed: observedAndroid(now, "1.4.0", "1.5.0"),
			body:     blockBody("BLOCK happy-farm android 1.4.0", "1.4.0"),
			wantCode: "update_policy_invalid",
		},
		{
			name:     "확인 문구가 다르면 거부한다",
			apps:     storeApp(),
			observed: observedAndroid(now, "1.4.0", "1.5.0"),
			body:     blockBody("BLOCK happy-farm android 1.5.0", "1.4.0"),
			wantCode: "request_invalid",
		},
		{
			name:     "안정 SemVer가 아니면 거부한다",
			apps:     storeApp(),
			observed: observedAndroid(now, "1.4.0", "1.5.0"),
			body:     blockBody("BLOCK happy-farm android nightly", "nightly"),
			wantCode: "request_invalid",
		},
		{
			name:     "설치본이 없는 플랫폼에는 걸 수 없다",
			apps:     storeApp(),
			observed: observedAndroid(now, "1.4.0", "1.5.0"),
			body:     `{"appId":"happy-farm","platforms":{"ait":{"blockedVersions":["1.4.0"]}}}`,
			wantCode: "request_invalid",
		},
		{
			name:     "바꿀 값이 없는 요청은 거부한다",
			apps:     storeApp(),
			observed: observedAndroid(now, "1.4.0", "1.5.0"),
			body:     `{"appId":"happy-farm","platforms":{"android":{}}}`,
			wantCode: "request_invalid",
		},
		{
			name:     "중복 버전은 거부한다",
			apps:     storeApp(),
			observed: observedAndroid(now, "1.4.0", "1.5.0"),
			body:     blockBody("BLOCK happy-farm android 1.4.0", "1.4.0", "v1.4.0"),
			wantCode: "request_invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &fakeConfig{observed: tt.observed}
			h := newUpdatePolicyHandler(t, cfg, tt.apps, &fakeAuditor{})

			w := serve(t, h, http.MethodPost, updatePolicyPath, tt.body, "tok", "syous")

			_, _, code := decodeEnvelope(t, w)
			if code != tt.wantCode {
				t.Fatalf("code = %q, want %q (status=%d body=%s)",
					code, tt.wantCode, w.Code, w.Body.String())
			}
			// 가드에 걸리면 아무것도 쓰지 않는다.
			if len(cfg.policyCalls) != 0 {
				t.Fatalf("거부된 요청이 저장됐다: %+v", cfg.policyCalls)
			}
		})
	}
}

func TestUpdatePolicyBlockSucceeds(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	cfg := &fakeConfig{observed: observedAndroid(now, "1.4.0", "1.4.1", "1.5.0")}
	auditor := &fakeAuditor{}
	h := newUpdatePolicyHandler(t, cfg, storeApp(), auditor)

	body := blockBody(
		"BLOCK happy-farm android 1.4.0; BLOCK happy-farm android 1.4.1",
		"1.4.0", "1.4.1",
	)
	w := serve(t, h, http.MethodPost, updatePolicyPath, body, "tok", "syous")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(cfg.policyCalls) != 1 {
		t.Fatalf("저장 호출 %d회", len(cfg.policyCalls))
	}
	blocked := cfg.policyCalls[0].policy.Platforms["android"].BlockedVersions
	if len(blocked) != 2 {
		t.Fatalf("차단 목록 = %+v", blocked)
	}
	if len(auditor.records) != 1 || auditor.records[0].outcome != "blocked" {
		t.Fatalf("감사 기록 = %+v", auditor.records)
	}
	// 무엇을 막았는지가 감사에 남아야 한다. 키 이름만 남기면 나중에
	// "어떤 버전을 왜 막았나"를 답할 수 없다.
	detail, _ := auditor.records[0].detail["platforms"].(map[string]any)
	android, _ := detail["android"].(map[string]any)
	versions, _ := android["blockedVersions"].([]string)
	if len(versions) != 2 || versions[0] != "1.4.0" || versions[1] != "1.4.1" {
		t.Errorf("감사 detail = %+v", auditor.records[0].detail)
	}
}

// 해제에는 가드도 확인 문구도 없다. 되돌리기는 언제나 즉시 가능해야 한다.
func TestUpdatePolicyUnblockNeedsNoGuard(t *testing.T) {
	cfg := &fakeConfig{
		policy: remoteconfig.UpdatePolicy{Platforms: map[string]remoteconfig.PlatformUpdatePolicy{
			"android": {BlockedVersions: []remoteconfig.BlockedVersion{{Version: "1.4.0"}}},
		}},
		// 관측이 비어 있어도 해제는 통과해야 한다.
		observed: nil,
	}
	h := newUpdatePolicyHandler(t, cfg, storeApp(), &fakeAuditor{})

	body := `{"appId":"happy-farm","platforms":{"android":{"blockedVersions":[]}}}`
	w := serve(t, h, http.MethodPost, updatePolicyPath, body, "tok", "syous")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(cfg.policyCalls) != 1 {
		t.Fatalf("저장 호출 %d회", len(cfg.policyCalls))
	}
}

// 이미 막힌 버전을 그대로 두는 요청은 새 확인 문구를 요구하지 않는다.
func TestUpdatePolicyKeepingBlockNeedsNoConfirmation(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	cfg := &fakeConfig{
		policy: remoteconfig.UpdatePolicy{Platforms: map[string]remoteconfig.PlatformUpdatePolicy{
			"android": {BlockedVersions: []remoteconfig.BlockedVersion{{Version: "1.4.0"}}},
		}},
		observed: observedAndroid(now, "1.4.0", "1.5.0"),
	}
	h := newUpdatePolicyHandler(t, cfg, storeApp(), &fakeAuditor{})

	body := `{"appId":"happy-farm","platforms":{"android":{"blockedVersions":["1.4.0"],` +
		`"recommendOverride":"1.5.0"}}}`
	w := serve(t, h, http.MethodPost, updatePolicyPath, body, "tok", "syous")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

// 관측 원장이 연결되지 않으면 가드가 근거를 잃는다. 열어 두면 안 된다.
func TestUpdatePolicyFailsClosedWithoutObservations(t *testing.T) {
	cfg := &fakeConfig{observedErr: errObservationsUnavailable}
	h := newUpdatePolicyHandler(t, cfg, storeApp(), &fakeAuditor{})

	body := blockBody("BLOCK happy-farm android 1.4.0", "1.4.0")
	w := serve(t, h, http.MethodPost, updatePolicyPath, body, "tok", "syous")

	if w.Code == http.StatusOK {
		t.Fatalf("관측 원장 없이 통과했다: %s", w.Body.String())
	}
	if len(cfg.policyCalls) != 0 {
		t.Fatalf("관측 원장 없이 저장됐다: %+v", cfg.policyCalls)
	}
}

func TestUpdatePolicyReadShowsCandidates(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	cfg := &fakeConfig{
		policy: remoteconfig.UpdatePolicy{Platforms: map[string]remoteconfig.PlatformUpdatePolicy{
			"android": {BlockedVersions: []remoteconfig.BlockedVersion{
				{Version: "1.4.0", BlockedAt: now.Add(-24 * time.Hour)},
			}},
		}},
		observed: observedAndroid(now, "1.4.0", "1.5.0"),
	}
	h := newUpdatePolicyHandler(t, cfg, storeApp(), &fakeAuditor{})

	w := serve(t, h, http.MethodGet,
		"/v1/admin/apps/happy-farm/config/update-policy", "", "tok", "syous")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	observed, _ := result["observedVersions"].([]any)
	if len(observed) != 2 {
		t.Fatalf("관측 후보 = %v", result["observedVersions"])
	}
	platforms, _ := result["platforms"].(map[string]any)
	android, _ := platforms["android"].(map[string]any)
	if android["autoRecommendedVersion"] != "1.5.0" {
		t.Errorf("자동 추종 = %v, want 1.5.0", android["autoRecommendedVersion"])
	}
	// 아무도 막히기 전에 어디로 보내지는지 확인할 수 있어야 한다.
	if android["updateUrl"] != testPlayURL {
		t.Errorf("updateUrl = %v", android["updateUrl"])
	}
}
