package appleapi

import "testing"

func TestClientRequiresOCSPInProduction(t *testing.T) {
	base := Config{
		KeyContent: []byte("-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----"),
		KeyID:      "2X9R4HXF34",
		Issuer:     "57246542-96fe-1a63-e053-0824d011072a",
		BundleID:   "com.seorilabs.test",
	}

	t.Run("production + OCSP 없음 → 거부", func(t *testing.T) {
		cfg := base
		cfg.Sandbox = false
		cfg.RequireOCSP = false
		if _, err := NewClient(cfg); err == nil {
			t.Fatal("production에서 폐기 확인 없이 통과시켰다")
		}
	})

	t.Run("sandbox는 OCSP 없이 허용", func(t *testing.T) {
		cfg := base
		cfg.Sandbox = true
		cfg.RequireOCSP = false
		if _, err := NewClient(cfg); err != nil {
			t.Fatalf("sandbox를 거부했다: %v", err)
		}
	})

	t.Run("필수 설정 누락", func(t *testing.T) {
		for _, name := range []string{"key", "keyID", "issuer", "bundleID"} {
			cfg := base
			cfg.Sandbox = true
			switch name {
			case "key":
				cfg.KeyContent = nil
			case "keyID":
				cfg.KeyID = ""
			case "issuer":
				cfg.Issuer = ""
			case "bundleID":
				cfg.BundleID = ""
			}
			if _, err := NewClient(cfg); err == nil {
				t.Errorf("%s 누락을 허용했다", name)
			}
		}
	})
}
