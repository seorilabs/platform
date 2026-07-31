package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// IAPConfig는 결제 설정이다.
//
// 마켓 자격증명은 platform-iap 서비스에만 마운트된다. R3다.
// 다른 role에서는 전부 비어 있는 것이 정상이다.
type IAPConfig struct {
	// Environment는 원장 환경이다. production 또는 sandbox다.
	//
	// 경로 prefix를 가른다. 섞이면 샌드박스 구매가 실제 지급이 된다.
	Environment string

	// CatalogJSON은 SKU 카탈로그다. 마켓 SKU를 entitlement로 옮긴다.
	CatalogJSON []byte

	// BindingKeys는 계정 참조 HMAC 키링이다.
	//
	// 첫 항목이 현재 키다. 나머지는 회전 중인 이전 키로 검증에만 쓴다.
	// 불변식 11이다.
	BindingKeys [][]byte

	Play  PlayConfig
	Apple AppleConfig
	Toss  TossConfig

	// VerifyRatePerMinute는 사용자당 분당 검증 요청 상한이다.
	VerifyRatePerMinute int
	// CompletionMaxAttempts는 마켓 완료 재시도 횟수 상한이다.
	CompletionMaxAttempts int
	// CompletionMaxAge는 완료를 포기하고 dead-letter로 보낼 나이다.
	CompletionMaxAge time.Duration
}

// PlayConfig는 Google Play 설정이다.
//
// 자격증명은 ADC를 쓴다. SA JSON 키를 배포하지 않는다는 조직 원칙이다.
// 런타임 SA에 Play Console 권한을 부여하는 방식이다.
type PlayConfig struct {
	PackageName string
}

// Enabled는 Play 검증기를 조립할 수 있는지 본다.
func (c PlayConfig) Enabled() bool { return c.PackageName != "" }

// AppleConfig는 App Store 설정이다.
type AppleConfig struct {
	// KeyContent는 App Store Connect에서 받은 .p8 개인키다.
	KeyContent []byte
	KeyID      string
	Issuer     string
	BundleID   string
	// Sandbox는 샌드박스 환경 여부다.
	//
	// IAPConfig.Environment와 반드시 일치해야 한다.
	// 어긋나면 부팅을 실패시킨다. 불변식 9다.
	Sandbox bool
}

// Enabled는 Apple 검증기를 조립할 수 있는지 본다.
func (c AppleConfig) Enabled() bool {
	return len(c.KeyContent) > 0 && c.KeyID != "" && c.Issuer != "" && c.BundleID != ""
}

// TossConfig는 AppsInToss 설정이다. mTLS를 쓴다.
type TossConfig struct {
	ClientCertPEM []byte
	ClientKeyPEM  []byte
	BaseURL       string
}

// Enabled는 AIT 검증기를 조립할 수 있는지 본다.
func (c TossConfig) Enabled() bool {
	return len(c.ClientCertPEM) > 0 && len(c.ClientKeyPEM) > 0
}

// IsSandbox는 샌드박스 원장인지 본다.
func (c IAPConfig) IsSandbox() bool { return c.Environment == EnvSandbox }

// 원장 환경이다.
const (
	EnvProduction = "production"
	EnvSandbox    = "sandbox"
)

// loadIAP는 결제 설정을 읽는다.
//
// 자격증명이 없는 마켓은 조용히 빠진다. 그 마켓 결제만
// platform_unavailable로 거부되고 나머지는 정상 동작한다.
// AIT 인증서 미확보 같은 상황이 실제로 있어서 전부-아니면-전무로 두지 않는다.
//
// requireMarket이 false면 원장 환경만 읽는다. Admin API가 그렇다 —
// 원장을 읽고 운영자 지급을 쓸 뿐 마켓에 묻지 않는다. 필요 없는
// 비밀을 마운트하지 않는 것이 R3의 실질이다.
func loadIAP(requireMarket bool) (IAPConfig, error) {
	c := IAPConfig{
		Environment:           envOr("IAP_LEDGER_ENVIRONMENT", EnvProduction),
		VerifyRatePerMinute:   30,
		CompletionMaxAttempts: 12,
		CompletionMaxAge:      48 * time.Hour,
	}

	switch c.Environment {
	case EnvProduction, EnvSandbox:
	default:
		return IAPConfig{}, fmt.Errorf(
			"config: IAP_LEDGER_ENVIRONMENT는 production 또는 sandbox여야 한다: %q", c.Environment)
	}

	if !requireMarket {
		// 원장 경로를 가르는 환경만 있으면 된다.
		return c, nil
	}

	raw := os.Getenv("IAP_CATALOG_JSON")
	if raw == "" {
		return IAPConfig{}, errors.New("config: IAP_CATALOG_JSON이 필요하다")
	}
	c.CatalogJSON = decodeMaybeBase64(raw)

	keys, err := loadBindingKeys()
	if err != nil {
		return IAPConfig{}, err
	}
	c.BindingKeys = keys

	c.Play = PlayConfig{PackageName: os.Getenv("IAP_PLAY_PACKAGE_NAME")}

	c.Apple = AppleConfig{
		KeyContent: decodeMaybeBase64(os.Getenv("IAP_APPLE_KEY")),
		KeyID:      os.Getenv("IAP_APPLE_KEY_ID"),
		Issuer:     os.Getenv("IAP_APPLE_ISSUER_ID"),
		BundleID:   os.Getenv("IAP_APPLE_BUNDLE_ID"),
		Sandbox:    c.IsSandbox(),
	}

	// Apple 환경을 따로 지정하면 원장 환경과 대조한다.
	// 어긋난 채 뜨면 샌드박스 구매가 실제 지급이 되거나 그 반대가 된다.
	if v := os.Getenv("APPLE_APP_STORE_ENVIRONMENT"); v != "" {
		appleSandbox := strings.EqualFold(v, EnvSandbox)
		if appleSandbox != c.IsSandbox() {
			return IAPConfig{}, fmt.Errorf(
				"config: APPLE_APP_STORE_ENVIRONMENT(%s)가 IAP_LEDGER_ENVIRONMENT(%s)와 어긋난다",
				v, c.Environment)
		}
		c.Apple.Sandbox = appleSandbox
	}

	c.Toss = TossConfig{
		ClientCertPEM: decodeMaybeBase64(os.Getenv("IAP_TOSS_CLIENT_CERT")),
		ClientKeyPEM:  decodeMaybeBase64(os.Getenv("IAP_TOSS_CLIENT_KEY")),
		BaseURL:       os.Getenv("IAP_TOSS_BASE_URL"),
	}

	if err := loadIAPLimits(&c); err != nil {
		return IAPConfig{}, err
	}
	return c, nil
}

// loadIAPLimits는 재시도와 상한 값을 읽는다.
func loadIAPLimits(c *IAPConfig) error {
	rate, err := envInt("IAP_VERIFY_REQUESTS_PER_MINUTE", c.VerifyRatePerMinute)
	if err != nil {
		return err
	}
	c.VerifyRatePerMinute = rate

	attempts, err := envInt("IAP_COMPLETION_MAX_ATTEMPTS", c.CompletionMaxAttempts)
	if err != nil {
		return err
	}
	c.CompletionMaxAttempts = attempts

	if v := os.Getenv("IAP_COMPLETION_MAX_AGE_HOURS"); v != "" {
		h, err := strconv.Atoi(v)
		if err != nil || h <= 0 {
			return fmt.Errorf("config: IAP_COMPLETION_MAX_AGE_HOURS가 올바르지 않다: %q", v)
		}
		c.CompletionMaxAge = time.Duration(h) * time.Hour
	}

	if c.VerifyRatePerMinute <= 0 || c.CompletionMaxAttempts <= 0 {
		return errors.New("config: IAP 상한 값은 1 이상이어야 한다")
	}
	return nil
}

// loadBindingKeys는 계정 참조 HMAC 키링을 읽는다.
//
// 쉼표로 구분한다. 첫 항목이 현재 키다. 불변식 11이다.
func loadBindingKeys() ([][]byte, error) {
	raw := os.Getenv("IAP_ACCOUNT_BINDING_KEYS")
	if raw == "" {
		return nil, errors.New("config: IAP_ACCOUNT_BINDING_KEYS가 필요하다")
	}

	parts := strings.Split(raw, ",")
	keys := make([][]byte, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		k, err := decodeKey(p)
		if err != nil {
			return nil, fmt.Errorf("config: IAP_ACCOUNT_BINDING_KEYS[%d] %w", i, err)
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, errors.New("config: IAP_ACCOUNT_BINDING_KEYS에 키가 없다")
	}
	return keys, nil
}

// decodeKey는 base64 키를 디코딩한다.
//
// 키만은 base64를 강제한다. "원문도 허용"은 모호하기 때문이다.
// 32자 랜덤 문자열이 우연히 유효한 base64면 24바이트로 디코딩되어
// 길이 검사에 걸린다. 키가 약해지지는 않지만 — 검사가 디코딩 후에
// 돌기 때문이다 — 운영자는 32자를 넣고 "32바이트 이상이어야 한다"는
// 에러를 보게 된다. 무엇을 고쳐야 하는지 알 수 없다.
//
// base64를 강제하면 형식과 길이가 한 가지 뜻만 갖는다.
// 인증서와 .p8은 PEM 헤더로 원문임을 알 수 있어 이 문제가 없다.
//
//	openssl rand -base64 32
func decodeKey(s string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("는 base64여야 한다 (openssl rand -base64 32): %w", err)
	}
	if len(k) < 32 {
		return nil, fmt.Errorf(
			"는 디코딩 후 32바이트 이상이어야 한다 (현재 %d바이트)", len(k))
	}
	return k, nil
}

// decodeMaybeBase64는 base64면 디코딩하고 아니면 원문을 준다.
//
// .p8 키와 PEM 인증서는 개행이 있어 환경변수에 그대로 넣기 어렵다.
// base64로 받는 것을 권하되 원문도 허용해 로컬 개발을 편하게 한다.
func decodeMaybeBase64(s string) []byte {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// PEM은 base64 문자만으로 이루어지지 않는다. 헤더가 있으면 원문이다.
	if strings.Contains(s, "-----BEGIN") {
		return []byte(s)
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b
	}
	return []byte(s)
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s가 올바르지 않다: %q", key, v)
	}
	return n, nil
}
