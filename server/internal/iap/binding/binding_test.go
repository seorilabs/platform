package binding

import (
	"bytes"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

var (
	key1 = bytes.Repeat([]byte("a"), 32)
	key2 = bytes.Repeat([]byte("b"), 32)
	key3 = bytes.Repeat([]byte("c"), 32)
)

func TestNewKeyringValidation(t *testing.T) {
	t.Run("키가 없으면 거부", func(t *testing.T) {
		if _, err := NewKeyring(); err == nil {
			t.Fatal("빈 keyring을 허용했다")
		}
	})

	t.Run("짧은 키는 거부", func(t *testing.T) {
		// SHA-256 출력보다 짧으면 보안 강도가 떨어진다.
		if _, err := NewKeyring(bytes.Repeat([]byte("x"), 31)); err == nil {
			t.Fatal("31바이트 키를 허용했다")
		}
	})

	t.Run("4개 이상은 거부", func(t *testing.T) {
		_, err := NewKeyring(key1, key2, key3, key1)
		if err == nil {
			t.Fatal("키 4개를 허용했다")
		}
	})
}

func TestGoogleAccountID(t *testing.T) {
	r, err := NewKeyring(key1)
	if err != nil {
		t.Fatalf("keyring 생성 실패: %v", err)
	}

	id := r.GoogleAccountID("pu_abc")

	if !ValidGoogleFormat(id) {
		t.Errorf("형식이 맞지 않는다: %q", id)
	}
	if r.GoogleAccountID("pu_abc") != id {
		t.Error("같은 사용자에 다른 값이 나왔다")
	}
	if r.GoogleAccountID("pu_xyz") == id {
		t.Error("다른 사용자에 같은 값이 나왔다")
	}
}

func TestAppleAccountToken(t *testing.T) {
	r, _ := NewKeyring(key1)

	tok := r.AppleAccountToken("pu_abc")

	// Apple은 UUID 형식을 요구한다. 형식이 틀리면 구매 자체가 거부된다.
	if !ValidAppleFormat(tok) {
		t.Errorf("UUID 형식이 아니다: %q", tok)
	}
	if r.AppleAccountToken("pu_abc") != tok {
		t.Error("같은 사용자에 다른 값이 나왔다")
	}
	if r.AppleAccountToken("pu_xyz") == tok {
		t.Error("다른 사용자에 같은 값이 나왔다")
	}
}

// 같은 사용자라도 마켓마다 다른 값이어야 한다.
// 한쪽이 유출돼도 다른 마켓을 사칭할 수 없어야 한다.
func TestMarketsProduceDifferentValues(t *testing.T) {
	r, _ := NewKeyring(key1)

	g := r.GoogleAccountID("pu_same")
	a := r.AppleAccountToken("pu_same")

	// 인코딩이 달라 직접 비교는 의미가 적지만,
	// 유도 문자열이 같으면 같은 HMAC에서 나온 값이 된다.
	if strings.EqualFold(g, strings.ReplaceAll(a, "-", "")) {
		t.Error("두 마켓이 같은 HMAC을 쓴다")
	}
}

func TestVerify(t *testing.T) {
	r, _ := NewKeyring(key1)

	t.Run("우리가 발급한 값은 통과", func(t *testing.T) {
		id := r.GoogleAccountID("pu_1")
		if err := r.VerifyGoogle("pu_1", id); err != nil {
			t.Errorf("자기 값을 거부했다: %v", err)
		}
	})

	t.Run("다른 사용자 값은 거부", func(t *testing.T) {
		other := r.GoogleAccountID("pu_2")
		err := r.VerifyGoogle("pu_1", other)
		if err == nil {
			t.Fatal("다른 사용자의 계정 참조를 통과시켰다")
		}
		if code := platformerr.CodeOf(err); code != platformerr.CodeAccountBindingMismatch {
			t.Errorf("code = %q, want account_binding_mismatch", code)
		}
	})

	t.Run("빈 값은 missing", func(t *testing.T) {
		err := r.VerifyGoogle("pu_1", "")
		if code := platformerr.CodeOf(err); code != platformerr.CodeAccountBindingMissing {
			t.Errorf("code = %q, want account_binding_missing", code)
		}
	})

	t.Run("Apple도 동일", func(t *testing.T) {
		tok := r.AppleAccountToken("pu_1")
		if err := r.VerifyApple("pu_1", tok); err != nil {
			t.Errorf("자기 값을 거부했다: %v", err)
		}
		if err := r.VerifyApple("pu_2", tok); err == nil {
			t.Error("다른 사용자의 토큰을 통과시켰다")
		}
	})
}

// 불변식 11: keyring 회전. 첫 항목이 현재 키이고 나머지로도 검증된다.
func TestKeyRotation(t *testing.T) {
	old, _ := NewKeyring(key1)
	oldID := old.GoogleAccountID("pu_rotate")

	// 키를 교체한다. 새 키가 앞에 오고 옛 키가 뒤에 남는다.
	rotated, err := NewKeyring(key2, key1)
	if err != nil {
		t.Fatalf("keyring 생성 실패: %v", err)
	}

	// 새 발급은 새 키를 쓴다
	newID := rotated.GoogleAccountID("pu_rotate")
	if newID == oldID {
		t.Error("키를 바꿨는데 같은 값이 나온다")
	}

	// 옛 키로 발급된 값도 아직 검증된다.
	// 이게 없으면 키 교체 순간 진행 중이던 구매가 전부 실패한다.
	if err := rotated.VerifyGoogle("pu_rotate", oldID); err != nil {
		t.Errorf("회전 전 키로 발급된 값을 거부했다: %v", err)
	}
	if err := rotated.VerifyGoogle("pu_rotate", newID); err != nil {
		t.Errorf("현재 키로 발급된 값을 거부했다: %v", err)
	}

	// keyring에서 완전히 빠진 키는 더 이상 통하지 않는다
	final, _ := NewKeyring(key3)
	if err := final.VerifyGoogle("pu_rotate", oldID); err == nil {
		t.Error("폐기된 키로 발급된 값이 통과했다")
	}
}

// AIT는 계정 바인딩이 면제다.
// aitUserKey custom claim 자체가 신뢰 경로이기 때문이다.
func TestRequiresBinding(t *testing.T) {
	tests := []struct {
		p    domain.Platform
		want bool
	}{
		{domain.PlatformGooglePlay, true},
		{domain.PlatformAppStore, true},
		{domain.PlatformAppsInToss, false},
		{domain.PlatformOperator, false},
	}
	for _, tt := range tests {
		if got := RequiresBinding(tt.p); got != tt.want {
			t.Errorf("RequiresBinding(%q) = %v, want %v", tt.p, got, tt.want)
		}
	}
}

func TestFormatValidators(t *testing.T) {
	r, _ := NewKeyring(key1)

	if !ValidGoogleFormat(r.GoogleAccountID("pu_1")) {
		t.Error("발급한 Google ID가 형식 검사를 통과하지 못한다")
	}
	if !ValidAppleFormat(r.AppleAccountToken("pu_1")) {
		t.Error("발급한 Apple 토큰이 형식 검사를 통과하지 못한다")
	}

	if ValidGoogleFormat("짧음") {
		t.Error("잘못된 Google 형식을 통과시켰다")
	}
	if ValidAppleFormat("not-a-uuid") {
		t.Error("잘못된 Apple 형식을 통과시켰다")
	}
	// version이 5가 아닌 UUID는 거부해야 한다
	if ValidAppleFormat("00000000-0000-4000-8000-000000000000") {
		t.Error("version 4 UUID를 통과시켰다")
	}
}
