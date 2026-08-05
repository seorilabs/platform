package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

const testCatalog = `{"version":1,"entitlements":{"sp_a":{"google_play":"a"}}}`

// testKey는 32바이트 키의 base64 표현이다.
//
// 원문 32자를 그대로 넣으면 안 된다. 그게 유효한 base64면
// 24바이트로 디코딩되어 길이 검사에 걸린다.
func testKey(seed byte) string {
	k := make([]byte, 32)
	for i := range k {
		k[i] = seed
	}
	return base64.StdEncoding.EncodeToString(k)
}

// setIAPBase는 최소 필수 설정만 채운다.
func setIAPBase(t *testing.T) {
	t.Helper()
	t.Setenv("IAP_CATALOG_JSON", testCatalog)
	t.Setenv("IAP_ACCOUNT_BINDING_KEYS", testKey('k'))
}

func TestLoadIAPDefaults(t *testing.T) {
	setIAPBase(t)

	c, err := loadIAP(true)
	if err != nil {
		t.Fatalf("설정 읽기 실패: %v", err)
	}

	// 명시하지 않으면 production이다. 실수로 샌드박스가 되면 안 된다.
	if c.Environment != EnvProduction {
		t.Errorf("environment = %q, want production", c.Environment)
	}
	if c.IsSandbox() {
		t.Error("기본값이 샌드박스로 잡혔다")
	}
	if string(c.CatalogJSON) != testCatalog {
		t.Errorf("카탈로그가 달라졌다: %s", c.CatalogJSON)
	}
	if len(c.BindingKeys) != 1 {
		t.Errorf("키 %d개", len(c.BindingKeys))
	}
	if c.VerifyRatePerMinute <= 0 || c.CompletionMaxAttempts <= 0 || c.CompletionMaxAge <= 0 {
		t.Errorf("상한 기본값이 비었다: %+v", c)
	}

	// 자격증명이 없으면 어느 마켓도 켜지지 않는다
	if c.Play.Enabled() || c.Apple.Enabled() || c.Toss.Enabled() {
		t.Error("자격증명 없이 마켓이 켜졌다")
	}
}

// 불변식 9. Apple 환경과 원장 환경이 어긋난 채 뜨면
// 샌드박스 구매가 실제 지급이 되거나 그 반대가 된다.
func TestAppleEnvironmentMustMatchLedger(t *testing.T) {
	tests := []struct {
		name      string
		ledger    string
		apple     string
		wantError bool
	}{
		{"둘 다 production", EnvProduction, "Production", false},
		{"둘 다 sandbox", EnvSandbox, "Sandbox", false},
		{"대소문자 무시", EnvSandbox, "SANDBOX", false},
		{"원장 production + Apple sandbox", EnvProduction, "Sandbox", true},
		{"원장 sandbox + Apple production", EnvSandbox, "Production", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setIAPBase(t)
			t.Setenv("IAP_LEDGER_ENVIRONMENT", tt.ledger)
			t.Setenv("APPLE_APP_STORE_ENVIRONMENT", tt.apple)

			c, err := loadIAP(true)

			if tt.wantError {
				if err == nil {
					t.Fatal("어긋난 환경을 통과시켰다")
				}
				return
			}
			if err != nil {
				t.Fatalf("설정 읽기 실패: %v", err)
			}
			if c.Apple.Sandbox != (tt.ledger == EnvSandbox) {
				t.Errorf("apple.sandbox = %v, ledger = %q", c.Apple.Sandbox, tt.ledger)
			}
		})
	}
}

func TestRejectsUnknownEnvironment(t *testing.T) {
	setIAPBase(t)
	t.Setenv("IAP_LEDGER_ENVIRONMENT", "staging")

	if _, err := loadIAP(true); err == nil {
		t.Fatal("모르는 환경을 통과시켰다")
	}
}

// 계정 참조 키가 약하면 참조를 위조할 수 있다.
func TestBindingKeyValidation(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantKeys  int
		wantError bool
	}{
		{"32바이트 키 하나", testKey('k'), 1, false},
		{"회전 중 — 여러 키", testKey('a') + "," + testKey('b'), 2, false},
		{"공백은 무시", testKey('a') + " , " + testKey('b'), 2, false},
		{"짧은 키는 거부", "short", 0, true},
		{"두 번째가 짧으면 거부", testKey('a') + ",short", 0, true},
		// base64가 아니면 거부한다. 형식이 한 가지 뜻만 갖게 한다.
		{"원문 32자는 거부", strings.Repeat("!", 32), 0, true},
		// 32자가 유효한 base64로 읽히면 24바이트다. 길이 미달로 거부된다.
		{"base64로 읽히지만 24바이트", strings.Repeat("k", 32), 0, true},
		{"비어 있으면 거부", "", 0, true},
		{"쉼표만", ",,,", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("IAP_CATALOG_JSON", testCatalog)
			t.Setenv("IAP_ACCOUNT_BINDING_KEYS", tt.value)

			c, err := loadIAP(true)

			if tt.wantError {
				if err == nil {
					t.Fatal("약한 키를 통과시켰다")
				}
				return
			}
			if err != nil {
				t.Fatalf("설정 읽기 실패: %v", err)
			}
			if len(c.BindingKeys) != tt.wantKeys {
				t.Errorf("키 %d개, want %d", len(c.BindingKeys), tt.wantKeys)
			}
		})
	}
}

func TestRequiresCatalog(t *testing.T) {
	t.Setenv("IAP_ACCOUNT_BINDING_KEYS", testKey('k'))
	t.Setenv("IAP_CATALOG_JSON", "")

	if _, err := loadIAP(true); err == nil {
		t.Fatal("카탈로그 없이 통과시켰다")
	}
}

// .p8 키와 PEM 인증서는 개행이 있어 base64로 받는다.
func TestDecodeMaybeBase64(t *testing.T) {
	pem := "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"base64는 디코딩한다", base64.StdEncoding.EncodeToString([]byte("hello")), "hello"},
		{"PEM 원문은 그대로 둔다", pem, pem},
		{"base64가 아니면 원문", "not-base64-!!", "not-base64-!!"},
		{"빈 값", "", ""},
		{"공백만", "   ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(decodeMaybeBase64(tt.in)); got != tt.want {
				t.Errorf("= %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMarketEnabledDetection(t *testing.T) {
	setIAPBase(t)
	t.Setenv("IAP_PLAY_PACKAGE_NAME", "com.seorilabs.lizardtycoon")
	t.Setenv("IAP_REFUND_REVIEW_ENCRYPTION_KEYS", "2026-08:"+testKey('r'))
	t.Setenv("IAP_APPLE_KEY", base64.StdEncoding.EncodeToString([]byte("key")))
	t.Setenv("IAP_APPLE_KEY_ID", "2X9R4HXF34")
	t.Setenv("IAP_APPLE_ISSUER_ID", "57246542-96fe-1a63-e053-0824d011072a")
	t.Setenv("IAP_APPLE_BUNDLE_ID", "com.seorilabs.lizardtycoon")

	c, err := loadIAP(true)
	if err != nil {
		t.Fatalf("설정 읽기 실패: %v", err)
	}

	if !c.Play.Enabled() {
		t.Error("Play가 꺼졌다")
	}
	if len(c.RefundReviewKeys) != 1 || c.RefundReviewKeys[0].ID != "2026-08" {
		t.Errorf("환불 검토 키링 = %#v", c.RefundReviewKeys)
	}
	if !c.Apple.Enabled() {
		t.Error("App Store가 꺼졌다")
	}
	// AIT 인증서는 아직 미확보다. 그 마켓만 빠지고 나머지는 동작해야 한다.
	if c.Toss.Enabled() {
		t.Error("인증서 없이 AIT가 켜졌다")
	}

	// 일부만 있으면 켜지지 않는다
	t.Setenv("IAP_APPLE_KEY_ID", "")
	partial, err := loadIAP(true)
	if err != nil {
		t.Fatalf("설정 읽기 실패: %v", err)
	}
	if partial.Apple.Enabled() {
		t.Error("키 ID 없이 App Store가 켜졌다")
	}
}

func TestPlayRequiresRefundReviewEncryptionKeys(t *testing.T) {
	setIAPBase(t)
	t.Setenv("IAP_PLAY_PACKAGE_NAME", "com.seorilabs.lizardtycoon")

	if _, err := loadIAP(true); err == nil ||
		!strings.Contains(err.Error(), "IAP_REFUND_REVIEW_ENCRYPTION_KEYS") {
		t.Fatalf("환불 검토 키 없이 Play가 켜졌다: %v", err)
	}

	t.Setenv("IAP_REFUND_REVIEW_ENCRYPTION_KEYS", "current:"+testKey('a')+",old:"+testKey('b'))
	cfg, err := loadIAP(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RefundReviewKeys) != 2 || cfg.RefundReviewKeys[0].ID != "current" {
		t.Fatalf("키 회전 순서 = %#v", cfg.RefundReviewKeys)
	}

	t.Setenv("IAP_REFUND_REVIEW_ENCRYPTION_KEYS", "weak:"+testKey('a')+",weak:"+testKey('b'))
	if _, err := loadIAP(true); err == nil {
		t.Fatal("중복 keyId를 통과시켰다")
	}
}

func TestLimitOverrides(t *testing.T) {
	setIAPBase(t)
	t.Setenv("IAP_VERIFY_REQUESTS_PER_MINUTE", "10")
	t.Setenv("IAP_COMPLETION_MAX_ATTEMPTS", "5")
	t.Setenv("IAP_COMPLETION_MAX_AGE_HOURS", "24")

	c, err := loadIAP(true)
	if err != nil {
		t.Fatalf("설정 읽기 실패: %v", err)
	}
	if c.VerifyRatePerMinute != 10 || c.CompletionMaxAttempts != 5 {
		t.Errorf("상한 = %d/%d", c.VerifyRatePerMinute, c.CompletionMaxAttempts)
	}
	if c.CompletionMaxAge != 24*time.Hour {
		t.Errorf("최대 나이 = %v", c.CompletionMaxAge)
	}

	// 0이나 음수는 재시도를 아예 막거나 무한 반복시킨다
	for _, bad := range []string{"0", "-1", "많이"} {
		t.Run("잘못된 값 "+bad, func(t *testing.T) {
			setIAPBase(t)
			t.Setenv("IAP_COMPLETION_MAX_ATTEMPTS", bad)
			if _, err := loadIAP(true); err == nil {
				t.Errorf("%q를 통과시켰다", bad)
			}
		})
	}
}

// Admin API는 마켓에 묻지 않지만 조작 allowlist로 카탈로그는 읽는다.
//
// 필요 없는 비밀을 마운트하면 폭발 반경만 넓어진다. R3다.
func TestAdminNeedsEnvironmentAndCatalogWithoutMarketSecrets(t *testing.T) {
	t.Setenv("IAP_LEDGER_ENVIRONMENT", EnvSandbox)
	t.Setenv("IAP_CATALOG_JSON", testCatalog)
	// 계정 바인딩 키는 마켓 검증용이므로 일부러 비워둔다.
	t.Setenv("IAP_ACCOUNT_BINDING_KEYS", "")

	c, err := loadIAP(false)
	if err != nil {
		t.Fatalf("admin 설정을 거부했다: %v", err)
	}
	if !c.IsSandbox() {
		t.Error("환경을 읽지 못했다")
	}
	if len(c.CatalogJSON) == 0 || len(c.BindingKeys) != 0 {
		t.Error("카탈로그와 마켓 비밀 분리가 깨졌다")
	}

	// 마켓이 필요한 role은 계정 바인딩 키 없이 여전히 거부해야 한다.
	if _, err := loadIAP(true); err == nil {
		t.Error("마켓 비밀 없이 마켓 role을 통과시켰다")
	}

	// Admin도 entitlement 검증을 위해 카탈로그 없이는 부팅하지 않는다.
	t.Setenv("IAP_CATALOG_JSON", "")
	if _, err := loadIAP(false); err == nil {
		t.Error("카탈로그 없이 admin role을 통과시켰다")
	}
}
