package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

type jwk struct {
	KeyType   string `json:"kty"`
	KeyID     string `json:"kid"`
	Use       string `json:"use"`
	Algorithm string `json:"alg"`
	Modulus   string `json:"n"`
	Exponent  string `json:"e"`
}

type jwkSetResponse struct {
	Keys []jwk `json:"keys"`
}

type cachedKeys struct {
	values    map[string]*rsa.PublicKey
	expiresAt time.Time
}

type jwksCache struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu        sync.RWMutex
	set       *cachedKeys
	lastFetch time.Time
	fetching  sync.Mutex
}

func newJWKSCache(url string, client *http.Client, now func() time.Time) *jwksCache {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return &jwksCache{url: url, client: client, now: now}
}

func (c *jwksCache) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if key, ok := c.lookup(kid); ok {
		return key, nil
	}
	// 캐시가 아직 유효해도 처음 보는 kid라면 한 번은 강제로 갱신한다.
	// 공급자가 키를 교체한 직후 기존 max-age가 남아 있을 수 있다.
	if err := c.refresh(ctx, kid); err != nil {
		return nil, err
	}
	if key, ok := c.lookup(kid); ok {
		return key, nil
	}
	return nil, platformerr.New(platformerr.CodeAuthInvalid, "로그인 토큰 서명 키가 올바르지 않아요")
}

func (c *jwksCache) lookup(kid string) (*rsa.PublicKey, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.set == nil || !c.now().Before(c.set.expiresAt) {
		return nil, false
	}
	key, ok := c.set.values[kid]
	return key, ok
}

func (c *jwksCache) refresh(ctx context.Context, kid string) error {
	c.fetching.Lock()
	defer c.fetching.Unlock()
	if _, ok := c.lookup(kid); ok {
		return nil
	}
	c.mu.RLock()
	recentlyFetched := c.set != nil && c.now().Before(c.set.expiresAt) &&
		c.now().Sub(c.lastFetch) < time.Minute
	c.mu.RUnlock()
	if recentlyFetched {
		// 임의 kid로 원격 JWKS를 매 요청 다시 받게 하는 증폭을 막는다.
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderConfigInvalid,
			"로그인 공급자 키 요청을 만들지 못했어요")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable,
			"로그인 공급자 키를 가져오지 못했어요")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return platformerr.Newf(platformerr.CodeProviderUnavailable,
			"로그인 공급자 키 응답이 %d예요", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeProviderUnavailable,
			"로그인 공급자 키를 읽지 못했어요")
	}
	var response jwkSetResponse
	if err := json.Unmarshal(body, &response); err != nil || len(response.Keys) == 0 {
		return platformerr.New(platformerr.CodeProviderResponseInvalid,
			"로그인 공급자 키 응답이 올바르지 않아요")
	}
	values := make(map[string]*rsa.PublicKey, len(response.Keys))
	for _, raw := range response.Keys {
		if raw.KeyID == "" || raw.KeyType != "RSA" || raw.Algorithm != "RS256" ||
			(raw.Use != "" && raw.Use != "sig") {
			continue
		}
		key, err := parseRSAJWK(raw)
		if err != nil {
			return platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
				"로그인 공급자 공개키가 올바르지 않아요")
		}
		values[raw.KeyID] = key
	}
	if len(values) == 0 {
		return platformerr.New(platformerr.CodeProviderResponseInvalid,
			"사용할 수 있는 로그인 공급자 키가 없어요")
	}
	c.mu.Lock()
	c.set = &cachedKeys{values: values, expiresAt: c.now().Add(jwksTTL(resp.Header))}
	c.lastFetch = c.now()
	c.mu.Unlock()
	return nil
}

func parseRSAJWK(raw jwk) (*rsa.PublicKey, error) {
	modulus, err := base64.RawURLEncoding.DecodeString(raw.Modulus)
	if err != nil || len(modulus) == 0 {
		return nil, fmt.Errorf("modulus decode 실패")
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(raw.Exponent)
	if err != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
		return nil, fmt.Errorf("exponent decode 실패")
	}
	exponent := 0
	for _, b := range exponentBytes {
		exponent = exponent<<8 | int(b)
	}
	if exponent < 3 || exponent%2 == 0 {
		return nil, fmt.Errorf("exponent가 올바르지 않다")
	}
	n := new(big.Int).SetBytes(modulus)
	if n.BitLen() < 2048 {
		return nil, fmt.Errorf("RSA 공개키가 2048비트보다 짧다")
	}
	return &rsa.PublicKey{N: n, E: exponent}, nil
}

func jwksTTL(header http.Header) time.Duration {
	const fallback = time.Hour
	for _, part := range strings.Split(header.Get("Cache-Control"), ",") {
		value, ok := strings.CutPrefix(strings.TrimSpace(part), "max-age=")
		if !ok {
			continue
		}
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds <= 0 {
			return fallback
		}
		ttl := time.Duration(seconds) * time.Second
		if ttl > time.Minute {
			ttl -= time.Minute
		}
		return ttl
	}
	return fallback
}
