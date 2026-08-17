// Package config는 환경변수에서 런타임 설정을 읽는다.
//
// 설정이 잘못되면 부팅을 실패시킨다. 런타임에 처음 쓰일 때 터지는 것보다
// 배포 즉시 드러나는 편이 낫다.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Role은 이 프로세스가 담당하는 역할이다.
type Role string

const (
	RoleAPI    Role = "api"
	RoleIAP    Role = "iap"
	RoleIngest Role = "ingest"
	RoleAdmin  Role = "admin"
	RoleWorker Role = "worker"
	RoleAds    Role = "ads"
)

// Config는 런타임 설정이다.
type Config struct {
	Role      Role
	Port      string
	ProjectID string

	// FirestorePrefix는 환경 prefix다. staging은 "stg_"를 쓴다.
	FirestorePrefix string

	// SessionSecret은 플랫폼 세션 토큰 서명키다.
	//
	// 32바이트 이상이어야 한다. 운영에서는 Secret Manager가 주입한다.
	SessionSecret []byte
	SessionTTL    time.Duration

	// BigQueryDataset은 이벤트와 감사 원장이 들어갈 데이터셋이다.
	BigQueryDataset string

	// IAP는 결제 설정이다. iap, worker, admin role에서 채워진다.
	//
	// 마켓 자격증명은 platform-iap 서비스에만 마운트된다. R3다.
	IAP         IAPConfig
	Ads         AdsConfig
	Operational OperationalConfig
}

// OperationalConfig는 확정 이벤트를 Backoffice에 서명해 전달하는 설정이다.
// URL과 키는 계정·IAP·광고 이벤트를 만드는 role과 재시도 worker에만 둔다.
type OperationalConfig struct {
	URL    string
	Secret []byte
}

func (c OperationalConfig) Enabled() bool { return c.URL != "" && len(c.Secret) >= 32 }

// Load는 환경변수에서 설정을 읽는다.
func Load() (Config, error) {
	c := Config{
		Port:            envOr("PORT", "8080"),
		ProjectID:       os.Getenv("GOOGLE_CLOUD_PROJECT"),
		FirestorePrefix: os.Getenv("PLATFORM_FS_PREFIX"),
		BigQueryDataset: envOr("PLATFORM_BQ_DATASET", "platform"),
		SessionTTL:      time.Hour,
	}

	role, err := parseRole(os.Getenv("PLATFORM_ROLE"))
	if err != nil {
		return Config{}, err
	}
	c.Role = role

	if c.ProjectID == "" {
		return Config{}, errors.New("config: GOOGLE_CLOUD_PROJECT가 필요하다")
	}
	if strings.Contains(c.FirestorePrefix, "/") {
		return Config{}, fmt.Errorf("config: PLATFORM_FS_PREFIX에 슬래시를 넣을 수 없다: %q", c.FirestorePrefix)
	}

	// worker는 HTTP를 열지 않으므로 세션 비밀키가 필요 없다.
	// ingest도 세션 없이 익명 수집을 허용한다.
	if role == RoleAPI || role == RoleIAP || role == RoleAds {
		secret, err := loadSessionSecret()
		if err != nil {
			return Config{}, err
		}
		c.SessionSecret = secret
	}

	if v := os.Getenv("PLATFORM_SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("config: PLATFORM_SESSION_TTL 해석 실패: %w", err)
		}
		c.SessionTTL = d
	}

	// 원장을 다루는 role이 마켓 설정을 읽는다.
	//
	// worker는 완료 재시도 때 마켓을 호출하므로 검증기가 필요하고,
	// admin은 원장 경로와 entitlement 카탈로그만 읽는다. 마켓 자격증명은
	// iap와 worker에만 마운트한다. R3다.
	if role == RoleIAP || role == RoleWorker || role == RoleAdmin {
		// admin은 카탈로그는 검증하되 마켓 자격증명은 요구하지 않는다.
		iap, err := loadIAP(role != RoleAdmin)
		if err != nil {
			return Config{}, err
		}
		c.IAP = iap
	}
	if role == RoleAds {
		ads, err := loadAds()
		if err != nil {
			return Config{}, err
		}
		c.Ads = ads
	}
	if role == RoleAPI || role == RoleIAP || role == RoleAds || role == RoleWorker {
		operational, err := loadOperational()
		if err != nil {
			return Config{}, err
		}
		c.Operational = operational
	}

	return c, nil
}

func loadOperational() (OperationalConfig, error) {
	rawURL := strings.TrimSpace(os.Getenv("BACKOFFICE_OPERATIONAL_EVENTS_URL"))
	rawSecret := os.Getenv("BACKOFFICE_OPERATIONAL_EVENTS_SECRET")
	if rawURL == "" && rawSecret == "" {
		return OperationalConfig{}, nil
	}
	if rawURL == "" || rawSecret == "" {
		return OperationalConfig{}, errors.New("config: Backoffice operational URL과 secret은 함께 필요하다")
	}
	parsed, err := url.ParseRequestURI(rawURL)
	loopbackHTTP := err == nil && parsed.Scheme == "http" &&
		(parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1")
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !loopbackHTTP) {
		return OperationalConfig{}, errors.New("config: BACKOFFICE_OPERATIONAL_EVENTS_URL은 HTTPS 또는 loopback HTTP여야 한다")
	}
	// Backoffice도 환경변수 문자열 자체를 HMAC key로 사용한다. 여기서만
	// base64 decode하면 양쪽 key가 달라져 모든 서명이 401이 된다.
	secret := []byte(rawSecret)
	if len(secret) < 32 {
		return OperationalConfig{}, fmt.Errorf("config: BACKOFFICE_OPERATIONAL_EVENTS_SECRET은 32바이트 이상이어야 한다 (현재 %d)", len(secret))
	}
	return OperationalConfig{URL: rawURL, Secret: secret}, nil
}

// IsStaging은 staging 환경인지 본다.
func (c Config) IsStaging() bool { return c.FirestorePrefix != "" }

func parseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleAPI, RoleIAP, RoleIngest, RoleAdmin, RoleWorker, RoleAds:
		return Role(s), nil
	case "":
		return "", errors.New("config: PLATFORM_ROLE이 필요하다")
	default:
		return "", fmt.Errorf("config: 알 수 없는 PLATFORM_ROLE: %s", s)
	}
}

// loadSessionSecret은 세션 서명키를 읽는다.
//
// base64로 받는다. 원시 바이트를 환경변수에 넣으면 개행이나 인코딩 문제가 생긴다.
func loadSessionSecret() ([]byte, error) {
	raw := os.Getenv("PLATFORM_SESSION_SECRET")
	if raw == "" {
		return nil, errors.New("config: PLATFORM_SESSION_SECRET이 필요하다")
	}

	secret, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// base64가 아니면 원문 그대로 쓴다. 로컬 개발 편의다.
		secret = []byte(raw)
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("config: PLATFORM_SESSION_SECRET은 32바이트 이상이어야 한다 (현재 %d)", len(secret))
	}
	return secret, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
