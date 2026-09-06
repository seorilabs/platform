package remoteconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func policyDoc(platform string, blocked ...string) Document {
	entry := PlatformUpdatePolicy{}
	for _, version := range blocked {
		entry.BlockedVersions = append(entry.BlockedVersions,
			BlockedVersion{Version: version})
	}
	return Document{
		AppID:  "happy-farm",
		Update: UpdatePolicy{Platforms: map[string]PlatformUpdatePolicy{platform: entry}},
	}
}

// 버전을 모르는 클라이언트를 막으면 헤더를 보내지 않는 구버전이 전부 죽는다.
func TestResolveUpdatePolicyNeverBlocksUnknownVersion(t *testing.T) {
	doc := policyDoc(PlatformAndroid, "1.4.0")

	for _, appVersion := range []string{"", "   ", "nightly", "1.2.3.4", "v"} {
		t.Run("appVersion="+appVersion, func(t *testing.T) {
			got := doc.Resolve(Target{Platform: PlatformAndroid, AppVersion: appVersion}, "9.9.9")
			if got.SDK.Status != SDKStatusOK {
				t.Fatalf("status = %q, want ok", got.SDK.Status)
			}
		})
	}
}

func TestResolveUpdatePolicy(t *testing.T) {
	tests := []struct {
		name       string
		doc        Document
		target     Target
		recommend  string
		wantStatus string
	}{
		{
			name:       "목록에 있는 버전만 강제한다",
			doc:        policyDoc(PlatformAndroid, "1.4.0", "1.4.1"),
			target:     Target{Platform: PlatformAndroid, AppVersion: "1.4.0"},
			wantStatus: SDKStatusBlocked,
		},
		{
			name:       "목록에 없는 낮은 버전은 강제하지 않는다",
			doc:        policyDoc(PlatformAndroid, "1.4.0", "1.4.1"),
			target:     Target{Platform: PlatformAndroid, AppVersion: "1.3.0"},
			wantStatus: SDKStatusOK,
		},
		{
			name:       "목록에 없는 높은 버전도 강제하지 않는다",
			doc:        policyDoc(PlatformAndroid, "1.4.0"),
			target:     Target{Platform: PlatformAndroid, AppVersion: "1.4.2"},
			wantStatus: SDKStatusOK,
		},
		{
			name:       "권장 기준 미만은 안내한다",
			doc:        policyDoc(PlatformAndroid),
			target:     Target{Platform: PlatformAndroid, AppVersion: "1.4.2"},
			recommend:  "1.5.0",
			wantStatus: SDKStatusDeprecated,
		},
		{
			name:       "권장 기준과 같으면 안내하지 않는다",
			doc:        policyDoc(PlatformAndroid),
			target:     Target{Platform: PlatformAndroid, AppVersion: "1.5.0"},
			recommend:  "1.5.0",
			wantStatus: SDKStatusOK,
		},
		{
			name:       "강제가 권장을 이긴다",
			doc:        policyDoc(PlatformAndroid, "1.4.0"),
			target:     Target{Platform: PlatformAndroid, AppVersion: "1.4.0"},
			recommend:  "1.5.0",
			wantStatus: SDKStatusBlocked,
		},
		{
			name:       "정책이 없는 플랫폼은 판정하지 않는다",
			doc:        policyDoc(PlatformAndroid, "1.4.0"),
			target:     Target{Platform: PlatformIOS, AppVersion: "1.4.0"},
			recommend:  "1.5.0",
			wantStatus: SDKStatusOK,
		},
		{
			name:       "플랫폼을 모르면 판정하지 않는다",
			doc:        policyDoc(PlatformAndroid, "1.4.0"),
			target:     Target{AppVersion: "1.4.0"},
			wantStatus: SDKStatusOK,
		},
		{
			name:       "v 접두사가 달라도 같은 빌드로 본다",
			doc:        policyDoc(PlatformAndroid, "v1.4.0"),
			target:     Target{Platform: PlatformAndroid, AppVersion: "1.4.0"},
			wantStatus: SDKStatusBlocked,
		},
		{
			name:       "자리 수가 달라도 같은 빌드로 본다",
			doc:        policyDoc(PlatformAndroid, "1.4"),
			target:     Target{Platform: PlatformAndroid, AppVersion: "1.4.0"},
			wantStatus: SDKStatusBlocked,
		},
		{
			name:       "대문자 플랫폼도 정규화한다",
			doc:        policyDoc(PlatformAndroid, "1.4.0"),
			target:     Target{Platform: "Android", AppVersion: "1.4.0"},
			wantStatus: SDKStatusBlocked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.doc.Resolve(tt.target, tt.recommend)
			if got.SDK.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got.SDK.Status, tt.wantStatus)
			}
		})
	}
}

// 수동 kill switch와 정책 중 더 엄격한 쪽이 이겨야 한다.
func TestResolveUpdatePolicyKeepsStricterStatus(t *testing.T) {
	t.Run("수동 blocked를 정책이 완화하지 못한다", func(t *testing.T) {
		doc := policyDoc(PlatformAndroid)
		doc.SDK = SDKStatus{Status: SDKStatusBlocked, Message: "손으로 막았다"}

		got := doc.Resolve(Target{Platform: PlatformAndroid, AppVersion: "9.9.9"}, "1.0.0")
		if got.SDK.Status != SDKStatusBlocked {
			t.Fatalf("status = %q, want blocked", got.SDK.Status)
		}
		if got.SDK.Message != "손으로 막았다" {
			t.Fatalf("수동 문구가 덮였다: %q", got.SDK.Message)
		}
	})

	t.Run("낡은 수동 ok가 정책의 차단을 풀지 못한다", func(t *testing.T) {
		doc := policyDoc(PlatformAndroid, "1.4.0")
		doc.SDK = SDKStatus{Status: SDKStatusOK, Message: "옛날 문구"}

		got := doc.Resolve(Target{Platform: PlatformAndroid, AppVersion: "1.4.0"}, "")
		if got.SDK.Status != SDKStatusBlocked {
			t.Fatalf("status = %q, want blocked", got.SDK.Status)
		}
		if got.SDK.Message != defaultBlockedMessage {
			t.Fatalf("message = %q, want 기본 차단 문구", got.SDK.Message)
		}
	})
}

func TestResolveUpdatePolicyReportsTarget(t *testing.T) {
	doc := policyDoc(PlatformAndroid)
	doc.Update.Platforms[PlatformAndroid] = PlatformUpdatePolicy{RecommendOverride: "2.0.0"}

	// override가 자동 추종을 이긴다.
	got := doc.Resolve(Target{Platform: PlatformAndroid, AppVersion: "1.9.0"}, "1.5.0")
	if got.SDK.RecommendedVersion != "2.0.0" {
		t.Fatalf("recommendedVersion = %q, want 2.0.0", got.SDK.RecommendedVersion)
	}
	if got.SDK.Status != SDKStatusDeprecated {
		t.Fatalf("status = %q, want deprecated", got.SDK.Status)
	}
}

func TestPlatformFromRuntime(t *testing.T) {
	tests := map[string]string{
		"godot-native-android": PlatformAndroid,
		"godot-native-ios":     PlatformIOS,
		"godot-web-ait":        PlatformAIT,
		"rn-ios":               PlatformIOS,
		"rn-android":           PlatformAndroid,
		"ait-rn":               PlatformAIT,
		"web":                  PlatformWeb,
		"  RN-IOS  ":           PlatformIOS,
		"":                     "",
		"unknown-thing":        "",
		// 부분 문자열로 보면 windows의 "ios"가 걸린다.
		"windows": "",
	}

	for runtime, want := range tests {
		t.Run(runtime, func(t *testing.T) {
			if got := PlatformFromRuntime(runtime); got != want {
				t.Fatalf("PlatformFromRuntime(%q) = %q, want %q", runtime, got, want)
			}
		})
	}
}

// 런타임 문자열과 플랫폼의 대응은 서버가 정책을 거는 축이다.
// 계약 파일과 구현이 갈리면 콘솔에서 고른 플랫폼이 엉뚱한 빌드를 잡는다.
func TestPlatformFromRuntimeMatchesConformanceVector(t *testing.T) {
	path := filepath.Join("..", "..", "..", "spec", "conformance", "update-gate.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("계약 벡터를 읽지 못했다: %v", err)
	}
	var vector struct {
		RuntimeToPlatform []struct {
			Runtime  string `json:"runtime"`
			Platform string `json:"platform"`
		} `json:"runtime_to_platform"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatalf("계약 벡터를 해석하지 못했다: %v", err)
	}
	if len(vector.RuntimeToPlatform) == 0 {
		t.Fatal("계약 벡터가 비어 있다")
	}
	for _, tc := range vector.RuntimeToPlatform {
		if got := PlatformFromRuntime(tc.Runtime); got != tc.Platform {
			t.Errorf("PlatformFromRuntime(%q) = %q, want %q", tc.Runtime, got, tc.Platform)
		}
	}
}

func TestNormalizeClientPlatform(t *testing.T) {
	tests := map[string]string{
		"android": PlatformAndroid,
		"Android": PlatformAndroid,
		" ios ":   PlatformIOS,
		"web":     PlatformWeb,
		"ait":     PlatformAIT,
		"":        "",
		"windows": "",
	}
	for in, want := range tests {
		if got := NormalizeClientPlatform(in); got != want {
			t.Errorf("NormalizeClientPlatform(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHighestObservedVersions(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	fresh := now.Add(-1 * time.Hour)

	observed := []ObservedAppVersion{
		{Version: "1.4.0", Runtime: "godot-native-android", FirstSeenAt: old},
		{Version: "1.5.0", Runtime: "godot-native-android", FirstSeenAt: old},
		// 소킹이 끝나지 않은 빌드는 후보가 아니다.
		{Version: "1.6.0", Runtime: "godot-native-android", FirstSeenAt: fresh},
		// 안정 SemVer가 아니면 후보가 아니다.
		{Version: "nightly", Runtime: "godot-native-android", FirstSeenAt: old},
		{Version: "2.0.0", Runtime: "rn-ios", FirstSeenAt: old},
		// 설치본이 없는 런타임은 후보가 아니다.
		{Version: "9.9.9", Runtime: "ait-rn", FirstSeenAt: old},
		{Version: "9.9.9", Runtime: "web", FirstSeenAt: old},
		// 관측 시각이 없으면 소킹을 판정할 수 없다.
		{Version: "8.8.8", Runtime: "godot-native-android"},
	}

	got := HighestObservedVersions(observed, now)
	want := map[string]string{PlatformAndroid: "1.5.0", PlatformIOS: "2.0.0"}
	if len(got) != len(want) {
		t.Fatalf("자동 추종 = %v, want %v", got, want)
	}
	for platform, version := range want {
		if got[platform] != version {
			t.Errorf("자동 추종[%s] = %q, want %q", platform, got[platform], version)
		}
	}
}

// 레지스트리 값만 바뀌면 문서 version이 안 올라간다. 소금이 없으면 304를
// 받은 클라이언트가 영영 옛 스토어 주소를 쥔다.
func TestETagChangesWithSalt(t *testing.T) {
	doc := Document{AppID: "happy-farm", Version: 7}
	target := Target{Platform: PlatformAndroid, AppVersion: "1.0.0"}

	base := doc.ETag(target)
	if got := doc.ETag(target); got != base {
		t.Fatalf("같은 입력에 ETag가 달라졌다: %q != %q", got, base)
	}
	if got := doc.ETag(target, "2026-09-06T00:00:00Z"); got == base {
		t.Fatal("소금이 달라도 ETag가 같다")
	}
}

func TestMergeUpdatePolicyKeepsBlockedAt(t *testing.T) {
	blockedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	current := UpdatePolicy{Platforms: map[string]PlatformUpdatePolicy{
		PlatformAndroid: {BlockedVersions: []BlockedVersion{
			{Version: "1.4.0", BlockedAt: blockedAt},
		}},
	}}
	next := UpdatePolicy{Platforms: map[string]PlatformUpdatePolicy{
		PlatformAndroid: {BlockedVersions: []BlockedVersion{
			{Version: "v1.4.0"},
			{Version: "1.4.1"},
		}},
	}}

	merged := mergeUpdatePolicy(current, next, now)
	got := merged.Platforms[PlatformAndroid].BlockedVersions
	if len(got) != 2 {
		t.Fatalf("차단 목록 길이 = %d, want 2", len(got))
	}
	if !got[0].BlockedAt.Equal(blockedAt) {
		t.Errorf("이미 막힌 버전의 시각이 리셋됐다: %v", got[0].BlockedAt)
	}
	if !got[1].BlockedAt.Equal(now) {
		t.Errorf("새로 막은 버전의 시각 = %v, want %v", got[1].BlockedAt, now)
	}
}
