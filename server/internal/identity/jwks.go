package identity

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FirebaseCertURL은 Firebase ID 토큰 서명 인증서 주소다.
//
// 이 키셋 하나가 모든 Firebase 프로젝트의 ID 토큰을 서명한다.
// 프로젝트별 키가 아니다. 덕분에 앱 16개의 자격증명을 하나도 보유하지 않고
// 검증할 수 있고, 프로젝트 구분은 서명이 아니라 aud와 iss claim으로 한다.
//
// 이 사실이 인증 설계를 크게 단순화했다.
// docs/03-architecture/identity.md 참고.
const FirebaseCertURL = "https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com"

// keySet은 kid → 공개키 맵과 만료 시각을 들고 있다.
type keySet struct {
	keys      map[string]*rsa.PublicKey
	expiresAt time.Time
}

// KeyCache는 Firebase 서명 공개키를 캐시한다.
//
// 응답의 Cache-Control max-age를 존중한다. Google이 키를 주기적으로
// 교체하므로 무기한 캐시하면 검증이 깨진다.
type KeyCache struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu  sync.RWMutex
	set *keySet

	// fetching은 동시 갱신을 하나로 합친다.
	// 캐시가 만료된 순간 요청 100개가 오면 100번 받아오는 걸 막는다.
	fetching sync.Mutex
}

// NewKeyCache는 키 캐시를 만든다.
func NewKeyCache(client *http.Client) *KeyCache {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &KeyCache{
		url:    FirebaseCertURL,
		client: client,
		now:    time.Now,
	}
}

// WithURL은 키셋 주소를 바꾼다. 테스트용이다.
func (c *KeyCache) WithURL(u string) *KeyCache {
	c.url = u
	return c
}

// WithClock은 시계를 주입한다. 테스트용이다.
func (c *KeyCache) WithClock(now func() time.Time) *KeyCache {
	c.now = now
	return c
}

// Key는 kid에 해당하는 공개키를 돌려준다.
func (c *KeyCache) Key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if k, ok := c.lookup(kid); ok {
		return k, nil
	}

	// 캐시에 없다. 키가 교체됐을 수 있으니 한 번 갱신한다.
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}

	if k, ok := c.lookup(kid); ok {
		return k, nil
	}
	// 갱신 후에도 없으면 위조 토큰이거나 우리가 모르는 발급자다.
	return nil, fmt.Errorf("identity: 알 수 없는 kid: %s", kid)
}

func (c *KeyCache) lookup(kid string) (*rsa.PublicKey, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.set == nil || c.now().After(c.set.expiresAt) {
		return nil, false
	}
	k, ok := c.set.keys[kid]
	return k, ok
}

// Warm은 키셋을 미리 받아둔다.
//
// 부팅 시 부르면 첫 요청이 키셋 왕복을 기다리지 않는다.
// 실패해도 서버는 뜬다. 첫 검증 시점에 다시 시도한다.
func (c *KeyCache) Warm(ctx context.Context) error { return c.refresh(ctx) }

func (c *KeyCache) refresh(ctx context.Context) error {
	c.fetching.Lock()
	defer c.fetching.Unlock()

	// 락을 기다리는 동안 다른 고루틴이 이미 채웠을 수 있다.
	c.mu.RLock()
	fresh := c.set != nil && c.now().Before(c.set.expiresAt)
	c.mu.RUnlock()
	if fresh {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return fmt.Errorf("identity: 키셋 요청 생성 실패: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("identity: 키셋 요청 실패: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("identity: 키셋 응답이 %d다", resp.StatusCode)
	}

	// 키셋은 작다. 상한을 두어 응답이 이상해도 메모리를 다 쓰지 않게 한다.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("identity: 키셋 읽기 실패: %w", err)
	}

	// 응답은 {"kid": "-----BEGIN CERTIFICATE-----..."} 형태다.
	var raw map[string]string
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("identity: 키셋 파싱 실패: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("identity: 키셋이 비어 있다")
	}

	keys := make(map[string]*rsa.PublicKey, len(raw))
	for kid, certPEM := range raw {
		pub, err := parseRSAPublicKeyFromPEM(certPEM)
		if err != nil {
			return fmt.Errorf("identity: kid %s 인증서 해석 실패: %w", kid, err)
		}
		keys[kid] = pub
	}

	c.mu.Lock()
	c.set = &keySet{keys: keys, expiresAt: c.now().Add(cacheTTL(resp.Header))}
	c.mu.Unlock()
	return nil
}

// cacheTTL은 Cache-Control max-age를 읽는다.
//
// Google이 키를 교체하므로 이 값을 무시하면 검증이 언젠가 깨진다.
// 헤더가 없거나 이상하면 보수적으로 1시간을 쓴다.
func cacheTTL(h http.Header) time.Duration {
	const fallback = time.Hour

	cc := h.Get("Cache-Control")
	if cc == "" {
		return fallback
	}
	for _, part := range strings.Split(cc, ",") {
		part = strings.TrimSpace(part)
		v, ok := strings.CutPrefix(part, "max-age=")
		if !ok {
			continue
		}
		secs, err := strconv.Atoi(v)
		if err != nil || secs <= 0 {
			return fallback
		}
		// 만료 직전에 갱신하도록 조금 당긴다.
		d := time.Duration(secs) * time.Second
		if d > time.Minute {
			d -= time.Minute
		}
		return d
	}
	return fallback
}

func parseRSAPublicKeyFromPEM(certPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, fmt.Errorf("PEM 블록을 찾지 못했다")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("인증서 파싱 실패: %w", err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("RSA 공개키가 아니다")
	}
	return pub, nil
}
