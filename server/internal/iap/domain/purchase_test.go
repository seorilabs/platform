package domain

import (
	"strings"
	"testing"
	"time"
)

// 불변식 1: orderKey = sha256("{platform}:{canonicalId}")
func TestOrderKeyInvariant(t *testing.T) {
	t.Run("같은 입력은 같은 키", func(t *testing.T) {
		a := OrderKey(PlatformGooglePlay, "token-abc")
		b := OrderKey(PlatformGooglePlay, "token-abc")
		if a != b {
			t.Errorf("같은 입력에 다른 키: %s vs %s", a, b)
		}
	})

	t.Run("마켓이 다르면 키가 다르다", func(t *testing.T) {
		// 같은 문자열이 다른 마켓에서 우연히 겹쳐도 충돌하면 안 된다.
		a := OrderKey(PlatformGooglePlay, "same-id")
		b := OrderKey(PlatformAppStore, "same-id")
		if a == b {
			t.Error("마켓이 달라도 키가 같다")
		}
	})

	t.Run("canonicalId가 다르면 키가 다르다", func(t *testing.T) {
		a := OrderKey(PlatformAppStore, "orig-1")
		b := OrderKey(PlatformAppStore, "orig-2")
		if a == b {
			t.Error("canonicalId가 달라도 키가 같다")
		}
	})

	t.Run("알려진 마켓끼리 충돌하지 않는다", func(t *testing.T) {
		// 같은 canonicalId를 모든 마켓에 넣어도 키가 전부 달라야 한다.
		seen := map[string]Platform{}
		for _, p := range AllPlatforms() {
			k := OrderKey(p, "동일한-canonical-id")
			if prev, dup := seen[k]; dup {
				t.Errorf("%q와 %q의 키가 충돌한다", p, prev)
			}
			seen[k] = p
		}
	})
}

// OrderKey 형식의 전제를 지키는 테스트다.
//
// sha256("{platform}:{canonicalId}")는 platform에 콜론이 들어가면 모호해진다.
// "a:b"+":"+"c"와 "a"+":"+"b:c"가 같은 문자열이 되기 때문이다.
// 불변식 1의 형식을 바꾸지 않는 대신 이 전제를 테스트로 고정한다.
//
// 새 마켓을 추가할 때 이름에 콜론을 넣으면 여기서 걸린다.
func TestPlatformsAreColonFree(t *testing.T) {
	for _, p := range AllPlatforms() {
		if strings.Contains(string(p), ":") {
			t.Errorf("마켓 이름 %q에 콜론이 있다. orderKey가 모호해진다", p)
		}
	}
}

// 불변식 2: granted와 alreadyGranted는 항상 배타적이다.
func TestGrantResultExclusivity(t *testing.T) {
	tests := []struct {
		name  string
		r     GrantResult
		valid bool
	}{
		{"새로 지급", GrantResult{Granted: true, AlreadyGranted: false}, true},
		{"이미 지급됨", GrantResult{Granted: false, AlreadyGranted: true}, true},
		{"둘 다 true는 불가", GrantResult{Granted: true, AlreadyGranted: true}, false},
		{"둘 다 false는 불가", GrantResult{Granted: false, AlreadyGranted: false}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Valid(); got != tt.valid {
				t.Errorf("Valid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

// 불변식 3: observedAt과 stateRank로 stale 갱신을 억제한다.
//
// 이 규칙이 없으면 늦게 도착한 grant가 이미 처리된 환불을 되돌린다.
func TestStaleUpdateSuppression(t *testing.T) {
	t0 := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	tests := []struct {
		name                   string
		existing, incoming     State
		existingAt, incomingAt time.Time
		wantStale              bool
		why                    string
	}{
		{
			name:     "늦게 온 grant가 환불을 되돌리지 못한다",
			existing: StateRevoked, incoming: StateActive,
			existingAt: t1, incomingAt: t0,
			wantStale: true,
			why:       "환불이 더 최근이면 유지돼야 한다",
		},
		{
			name:     "더 최신 환불은 반영된다",
			existing: StateActive, incoming: StateRevoked,
			existingAt: t0, incomingAt: t1,
			wantStale: false,
		},
		{
			name:     "같은 시각이면 랭크가 낮은 쪽을 무시한다",
			existing: StateRevoked, incoming: StateActive,
			existingAt: t0, incomingAt: t0,
			wantStale: true,
			why:       "웹훅과 클라이언트 검증이 같은 시각을 보고할 수 있다",
		},
		{
			name:     "같은 시각에 랭크가 높으면 반영한다",
			existing: StateActive, incoming: StateRevoked,
			existingAt: t0, incomingAt: t0,
			wantStale: false,
		},
		{
			name:     "같은 시각 같은 상태는 무시하지 않는다",
			existing: StateActive, incoming: StateActive,
			existingAt: t0, incomingAt: t0,
			wantStale: false,
		},
		{
			name:     "pending은 active를 덮지 못한다",
			existing: StateActive, incoming: StatePending,
			existingAt: t0, incomingAt: t0,
			wantStale: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsStaleUpdate(tt.existing, tt.incoming, tt.existingAt, tt.incomingAt)
			if got != tt.wantStale {
				t.Errorf("IsStaleUpdate() = %v, want %v. %s", got, tt.wantStale, tt.why)
			}
		})
	}
}

func TestStateRank(t *testing.T) {
	// revoked > active > pending 순서가 뒤집히면 환불이 덮인다.
	if StateRevoked.Rank() <= StateActive.Rank() {
		t.Error("revoked가 active보다 낮다")
	}
	if StateActive.Rank() <= StatePending.Rank() {
		t.Error("active가 pending보다 낮다")
	}
	if StateInvalid.Rank() != 0 {
		t.Error("invalid는 0이어야 한다")
	}
}

// 불변식 6: entitlement active = sources 중 하나라도 active
func TestIsActiveFrom(t *testing.T) {
	tests := []struct {
		name    string
		sources map[string]Source
		want    bool
	}{
		{"빈 sources는 비활성", map[string]Source{}, false},
		{"nil도 비활성", nil, false},
		{
			"하나가 active면 활성",
			map[string]Source{"a": {State: StateActive}},
			true,
		},
		{
			"전부 revoked면 비활성",
			map[string]Source{"a": {State: StateRevoked}, "b": {State: StateRevoked}},
			false,
		},
		{
			"하나만 active여도 활성",
			map[string]Source{
				"환불된구매": {State: StateRevoked},
				"운영자지급": {State: StateActive},
			},
			true,
		},
		{
			"pending만 있으면 비활성",
			map[string]Source{"a": {State: StatePending}},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsActiveFrom(tt.sources); got != tt.want {
				t.Errorf("IsActiveFrom() = %v, want %v", got, tt.want)
			}
		})
	}
}

// 불변식 9: production과 sandbox 경로가 섞이지 않는다.
func TestEnvironmentPathPrefix(t *testing.T) {
	if got := EnvProduction.PathPrefix(); got != "" {
		t.Errorf("production prefix = %q, want 빈 문자열", got)
	}
	if got := EnvSandbox.PathPrefix(); got != "iap_environments/sandbox/" {
		t.Errorf("sandbox prefix = %q", got)
	}
	// 알 수 없는 환경은 production으로 떨어진다.
	// sandbox 데이터가 production에 섞이는 것보다 낫다고 볼 수도 있지만
	// 반대다. 그래서 환경 설정은 부팅 시 검증한다.
	if got := Environment("unknown").PathPrefix(); got != "" {
		t.Errorf("unknown prefix = %q", got)
	}
}

// 마켓 계정 참조는 원문을 저장하지 않는다. ADR 0005
func TestHashAccountID(t *testing.T) {
	raw := "user@example.com"
	h := HashAccountID(raw)

	if h == raw {
		t.Error("원문이 그대로 나왔다")
	}
	if h == "" {
		t.Error("해시가 비었다")
	}
	if len(h) != 64 {
		t.Errorf("길이 = %d, want 64 (sha256 hex)", len(h))
	}
	if HashAccountID(raw) != h {
		t.Error("같은 입력에 다른 해시")
	}
	if HashAccountID("") != "" {
		t.Error("빈 입력은 빈 해시여야 한다")
	}
}

func TestVerifiedPurchaseKey(t *testing.T) {
	v := VerifiedPurchase{
		Platform:    PlatformAppStore,
		CanonicalID: "original-tx-123",
	}
	want := OrderKey(PlatformAppStore, "original-tx-123")
	if got := v.Key(); got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

func TestPlatformValid(t *testing.T) {
	valid := []Platform{
		PlatformGooglePlay, PlatformAppStore, PlatformAppsInToss, PlatformOperator,
	}
	for _, p := range valid {
		if !p.Valid() {
			t.Errorf("%q가 유효하지 않다고 나온다", p)
		}
	}
	if Platform("amazon").Valid() {
		t.Error("모르는 마켓이 유효하다고 나온다")
	}
}
