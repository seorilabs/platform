package config

import (
	"os"
)

type AdsConfig struct {
	AdMobVerifierKeysURL string
	AITClients           []TossClientCredential
	AITBaseURL           string
}

func (c AdsConfig) AITLoginEnabled() bool {
	return len(c.AITClients) > 0
}

func loadAds() (AdsConfig, error) {
	clients, err := loadTossClients("AIT_LOGIN_CLIENT_CERT", "AIT_LOGIN_CLIENT_KEY")
	if err != nil {
		return AdsConfig{}, err
	}
	return AdsConfig{
		AdMobVerifierKeysURL: os.Getenv("ADMOB_SSV_VERIFIER_KEYS_URL"),
		AITClients:           clients,
		AITBaseURL:           os.Getenv("AIT_LOGIN_BASE_URL"),
	}, nil
}
