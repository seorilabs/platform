package config

import (
	"errors"
	"os"
)

type AdsConfig struct {
	AdMobVerifierKeysURL string
	AITClientCertPEM     []byte
	AITClientKeyPEM      []byte
	AITBaseURL           string
}

func (c AdsConfig) AITLoginEnabled() bool {
	return len(c.AITClientCertPEM) > 0 && len(c.AITClientKeyPEM) > 0
}

func loadAds() (AdsConfig, error) {
	cfg := AdsConfig{
		AdMobVerifierKeysURL: os.Getenv("ADMOB_SSV_VERIFIER_KEYS_URL"),
		AITClientCertPEM:     decodeMaybeBase64(os.Getenv("AIT_LOGIN_CLIENT_CERT")),
		AITClientKeyPEM:      decodeMaybeBase64(os.Getenv("AIT_LOGIN_CLIENT_KEY")),
		AITBaseURL:           os.Getenv("AIT_LOGIN_BASE_URL"),
	}
	if (len(cfg.AITClientCertPEM) == 0) != (len(cfg.AITClientKeyPEM) == 0) {
		return AdsConfig{}, errors.New("config: AIT Login mTLS cert와 key는 함께 필요하다")
	}
	return cfg, nil
}
