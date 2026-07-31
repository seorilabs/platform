package remoteconfig

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in                  string
		valid               bool
		major, minor, patch int
	}{
		{"1.2.3", true, 1, 2, 3},
		{"v1.2.3", true, 1, 2, 3},
		{"1.2", true, 1, 2, 0},
		{"1", true, 1, 0, 0},
		{"  v2.0.1  ", true, 2, 0, 1},
		// 조직은 stable SemVer만 쓰지만 들어와도 앞부분은 읽는다.
		{"1.2.3-beta", true, 1, 2, 3},
		{"1.2.3+build", true, 1, 2, 3},
		{"", false, 0, 0, 0},
		{"abc", false, 0, 0, 0},
		{"1.2.3.4", false, 0, 0, 0},
		{"-1.0.0", false, 0, 0, 0},
	}

	for _, tt := range tests {
		got := parseVersion(tt.in)
		if got.valid != tt.valid {
			t.Errorf("parseVersion(%q).valid = %v, want %v", tt.in, got.valid, tt.valid)
			continue
		}
		if !tt.valid {
			continue
		}
		if got.major != tt.major || got.minor != tt.minor || got.patch != tt.patch {
			t.Errorf("parseVersion(%q) = %d.%d.%d, want %d.%d.%d",
				tt.in, got.major, got.minor, got.patch, tt.major, tt.minor, tt.patch)
		}
	}
}

func TestVersionInRange(t *testing.T) {
	tests := []struct {
		name             string
		client, min, max string
		want             bool
	}{
		{"조건 없으면 전부 매칭", "1.0.0", "", "", true},
		{"min 이상", "1.2.0", "1.0.0", "", true},
		{"min 미만", "0.9.0", "1.0.0", "", false},
		{"min과 같음", "1.0.0", "1.0.0", "", true},
		{"max 이하", "1.0.0", "", "2.0.0", true},
		{"max 초과", "2.1.0", "", "2.0.0", false},
		{"max와 같음", "2.0.0", "", "2.0.0", true},
		{"범위 안", "1.5.0", "1.0.0", "2.0.0", true},
		{"범위 밖", "3.0.0", "1.0.0", "2.0.0", false},
		{"patch 비교", "1.0.10", "1.0.9", "", true},
		// 알 수 없는 버전에 조건부 설정을 적용하면 예측할 수 없는 동작이 된다.
		{"클라 버전 파싱 불가면 조건부 규칙에 매칭 안 함", "unknown", "1.0.0", "", false},
		{"클라 버전 없어도 조건 없으면 매칭", "", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionInRange(tt.client, tt.min, tt.max); got != tt.want {
				t.Errorf("versionInRange(%q, %q, %q) = %v, want %v",
					tt.client, tt.min, tt.max, got, tt.want)
			}
		})
	}
}

func TestRuleMatches(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		tgt  Target
		want bool
	}{
		{
			"빈 규칙은 전부 매칭",
			Rule{},
			Target{Platform: "android", AppVersion: "1.0.0", Locale: "ko"},
			true,
		},
		{
			"플랫폼 일치",
			Rule{Platforms: []string{"android"}},
			Target{Platform: "android"},
			true,
		},
		{
			"플랫폼 불일치",
			Rule{Platforms: []string{"ios"}},
			Target{Platform: "android"},
			false,
		},
		{
			"플랫폼 대소문자 무시",
			Rule{Platforms: []string{"Android"}},
			Target{Platform: "android"},
			true,
		},
		{
			"로케일 언어만으로 매칭",
			Rule{Locales: []string{"ko"}},
			Target{Locale: "ko-KR"},
			true,
		},
		{
			"로케일 불일치",
			Rule{Locales: []string{"ja"}},
			Target{Locale: "ko-KR"},
			false,
		},
		{
			"버전과 플랫폼 모두 맞아야 함",
			Rule{Platforms: []string{"android"}, MinVersion: "1.0.0"},
			Target{Platform: "android", AppVersion: "0.9.0"},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rule.matches(tt.tgt); got != tt.want {
				t.Errorf("matches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	doc := Document{
		AppID:    "lizard-tycoon",
		Values:   map[string]any{"reward_multiplier": 1, "banner": "기본"},
		Features: map[string]bool{"iap": true, "events": true},
		SDK:      SDKStatus{Status: SDKStatusOK},
		Rules: []Rule{
			{
				Platforms: []string{"android"},
				Values:    map[string]any{"banner": "안드로이드"},
			},
			{
				MinVersion: "2.0.0",
				Values:     map[string]any{"reward_multiplier": 2},
				Features:   map[string]bool{"iap": false},
			},
		},
	}

	t.Run("규칙에 안 맞으면 기본값", func(t *testing.T) {
		got := doc.Resolve(Target{Platform: "ios", AppVersion: "1.0.0"})
		if got.Values["banner"] != "기본" {
			t.Errorf("banner = %v, want 기본", got.Values["banner"])
		}
		if got.Values["reward_multiplier"] != 1 {
			t.Errorf("reward_multiplier = %v, want 1", got.Values["reward_multiplier"])
		}
		if !got.Features["iap"] {
			t.Error("iap = false, want true")
		}
	})

	t.Run("플랫폼 규칙 적용", func(t *testing.T) {
		got := doc.Resolve(Target{Platform: "android", AppVersion: "1.0.0"})
		if got.Values["banner"] != "안드로이드" {
			t.Errorf("banner = %v, want 안드로이드", got.Values["banner"])
		}
	})

	t.Run("버전 규칙이 기능도 덮는다", func(t *testing.T) {
		got := doc.Resolve(Target{Platform: "ios", AppVersion: "2.1.0"})
		if got.Values["reward_multiplier"] != 2 {
			t.Errorf("reward_multiplier = %v, want 2", got.Values["reward_multiplier"])
		}
		if got.Features["iap"] {
			t.Error("iap = true, want false")
		}
	})

	t.Run("두 규칙이 모두 맞으면 뒤가 이긴다", func(t *testing.T) {
		got := doc.Resolve(Target{Platform: "android", AppVersion: "2.1.0"})
		if got.Values["banner"] != "안드로이드" {
			t.Errorf("banner = %v", got.Values["banner"])
		}
		if got.Values["reward_multiplier"] != 2 {
			t.Errorf("reward_multiplier = %v, want 2", got.Values["reward_multiplier"])
		}
	})

	t.Run("원본이 오염되지 않는다", func(t *testing.T) {
		doc.Resolve(Target{Platform: "android"})
		if doc.Values["banner"] != "기본" {
			t.Errorf("원본 banner가 바뀌었다: %v", doc.Values["banner"])
		}
	})

	t.Run("SDK 상태 기본값은 ok", func(t *testing.T) {
		empty := Document{}
		if got := empty.Resolve(Target{}); got.SDK.Status != SDKStatusOK {
			t.Errorf("SDK.Status = %q, want ok", got.SDK.Status)
		}
	})
}

// 점검 종료 시각이 지나면 자동으로 풀린다.
// 운영자가 끄는 걸 잊어도 서비스가 계속 막히지 않아야 한다.
func TestResolveExpiresMaintenance(t *testing.T) {
	doc := Document{
		Maint: Maintenance{Active: true, Until: time.Now().Add(-time.Hour)},
	}
	if doc.Resolve(Target{}).Maint.Active {
		t.Error("만료된 점검 모드가 계속 켜져 있다")
	}

	doc.Maint.Until = time.Now().Add(time.Hour)
	if !doc.Resolve(Target{}).Maint.Active {
		t.Error("아직 유효한 점검 모드가 꺼졌다")
	}

	// Until이 없으면 수동으로 끌 때까지 유지한다
	doc.Maint = Maintenance{Active: true}
	if !doc.Resolve(Target{}).Maint.Active {
		t.Error("종료 시각 없는 점검 모드가 꺼졌다")
	}
}

// ETag는 버전과 타겟이 같으면 같고 하나라도 다르면 달라야 한다.
// 같은 버전이라도 타겟이 다르면 결과가 다르기 때문이다.
func TestETag(t *testing.T) {
	doc := Document{AppID: "a", Version: 1}

	base := doc.ETag(Target{Platform: "android", AppVersion: "1.0.0", Locale: "ko"})

	if got := doc.ETag(Target{Platform: "android", AppVersion: "1.0.0", Locale: "ko"}); got != base {
		t.Error("같은 입력에 다른 ETag가 나왔다")
	}
	if got := doc.ETag(Target{Platform: "ios", AppVersion: "1.0.0", Locale: "ko"}); got == base {
		t.Error("플랫폼이 달라도 ETag가 같다")
	}

	doc.Version = 2
	if got := doc.ETag(Target{Platform: "android", AppVersion: "1.0.0", Locale: "ko"}); got == base {
		t.Error("버전이 올라갔는데 ETag가 같다. 클라이언트가 새 값을 못 본다")
	}
}

// time.Time은 구조체라 omitempty가 동작하지 않는다.
// 그냥 두면 "until":"0001-01-01T00:00:00Z"가 응답에 나가고
// 클라이언트가 이걸 유효한 시각으로 읽으면 점검이 끝난 것으로 오해한다.
func TestMaintenanceMarshalOmitsZeroTime(t *testing.T) {
	b, err := json.Marshal(Maintenance{Active: false})
	if err != nil {
		t.Fatalf("마샬 실패: %v", err)
	}
	if strings.Contains(string(b), "until") {
		t.Errorf("종료 시각이 없는데 until이 나갔다: %s", b)
	}

	until := time.Now().Add(time.Hour)
	b, err = json.Marshal(Maintenance{Active: true, Until: until})
	if err != nil {
		t.Fatalf("마샬 실패: %v", err)
	}
	if !strings.Contains(string(b), "until") {
		t.Errorf("종료 시각이 있는데 until이 빠졌다: %s", b)
	}
}
