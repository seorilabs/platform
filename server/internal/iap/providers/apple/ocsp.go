package apple

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// richzw/appstore는 x509 체인을 검증하지만 폐기 확인을 하지 않는다.
// Apple 공식 라이브러리는 OCSP online check를 한다. 그 간극을 메운다.
//
// 폐기된 서명 인증서를 그대로 신뢰하면, 탈취된 키로 만든 위조 JWS가
// 체인 검증을 통과한다. 결제 경로에서는 감수할 수 없다.

// ocspTimeout은 폐기 확인 상한이다.
// 짧게 잡는다. OCSP 응답자가 느려도 결제 전체를 붙잡으면 안 된다.
const ocspTimeout = 3 * time.Second

// ocspMaxResponseBytes는 OCSP 응답 크기 상한이다.
const ocspMaxResponseBytes = 64 << 10

// revocationChecker는 JWS 서명 인증서의 폐기 여부를 확인한다.
type revocationChecker struct {
	client *http.Client
	roots  *x509.CertPool

	mu    sync.RWMutex
	cache map[string]cachedStatus
	now   func() time.Time
}

// cachedStatus는 캐시된 폐기 상태다.
//
// OCSP 응답은 nextUpdate까지 유효하다고 응답자가 명시한다.
// 그때까지는 재사용해서 결제 경로의 왕복을 없앤다.
type cachedStatus struct {
	revoked   bool
	expiresAt time.Time
}

func newRevocationChecker(roots *x509.CertPool) *revocationChecker {
	return &revocationChecker{
		client: &http.Client{Timeout: ocspTimeout},
		roots:  roots,
		cache:  make(map[string]cachedStatus),
		now:    time.Now,
	}
}

// check는 JWS의 서명 인증서가 폐기되었는지 본다.
//
// 폐기되었으면 에러다. 확인 자체가 실패하면 통과시킨다 — fail-open이다.
// OCSP 응답자 장애로 정상 결제가 전부 막히는 쪽이 더 나쁘다.
// 체인 검증은 이미 통과한 상태이므로 위조 난이도는 여전히 높다.
func (r *revocationChecker) check(ctx context.Context, jws string) error {
	leaf, issuer, err := certChainFromJWS(jws)
	if err != nil {
		// 체인을 못 읽었다면 서명 검증도 통과했을 리 없다.
		// 그래도 여기서 통과시키지는 않는다.
		return err
	}
	if issuer == nil {
		// 중간 인증서가 없으면 OCSP 요청을 만들 수 없다.
		return nil
	}

	key := string(leaf.SerialNumber.Bytes())

	if st, ok := r.lookup(key); ok {
		if st.revoked {
			return platformerr.New(platformerr.CodeProviderResponseInvalid,
				"App Store 서명 인증서가 폐기되었어요")
		}
		return nil
	}

	revoked, expiresAt, err := r.query(ctx, leaf, issuer)
	if err != nil {
		// fail-open. 확인 실패는 폐기가 아니다.
		return nil
	}

	r.store(key, cachedStatus{revoked: revoked, expiresAt: expiresAt})

	if revoked {
		return platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 서명 인증서가 폐기되었어요")
	}
	return nil
}

func (r *revocationChecker) lookup(key string) (cachedStatus, bool) {
	r.mu.RLock()
	st, ok := r.cache[key]
	r.mu.RUnlock()

	if !ok || r.now().After(st.expiresAt) {
		return cachedStatus{}, false
	}
	return st, true
}

func (r *revocationChecker) store(key string, st cachedStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[key] = st
}

// query는 OCSP 응답자에게 묻는다.
func (r *revocationChecker) query(
	ctx context.Context,
	leaf, issuer *x509.Certificate,
) (revoked bool, expiresAt time.Time, err error) {
	if len(leaf.OCSPServer) == 0 {
		return false, time.Time{}, errNoOCSPServer
	}

	req, err := ocsp.CreateRequest(leaf, issuer, nil)
	if err != nil {
		return false, time.Time{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, ocspTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		leaf.OCSPServer[0], strings.NewReader(string(req)))
	if err != nil {
		return false, time.Time{}, err
	}
	httpReq.Header.Set("Content-Type", "application/ocsp-request")

	rsp, err := r.client.Do(httpReq)
	if err != nil {
		return false, time.Time{}, err
	}
	defer rsp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(rsp.Body, ocspMaxResponseBytes))
	if err != nil {
		return false, time.Time{}, err
	}

	// issuer를 넘겨 응답 서명까지 검증한다.
	// 넘기지 않으면 위조된 "not revoked" 응답을 그대로 믿게 된다.
	parsed, err := ocsp.ParseResponseForCert(raw, leaf, issuer)
	if err != nil {
		return false, time.Time{}, err
	}

	// nextUpdate가 없으면 짧게만 캐시한다.
	expiresAt = parsed.NextUpdate
	if expiresAt.IsZero() {
		expiresAt = r.now().Add(1 * time.Hour)
	}

	return parsed.Status == ocsp.Revoked, expiresAt, nil
}

// errNoOCSPServer는 인증서에 OCSP 응답자 주소가 없을 때다.
var errNoOCSPServer = platformerr.New(platformerr.CodeProviderResponseInvalid,
	"인증서에 폐기 확인 주소가 없어요")

// certChainFromJWS는 JWS 헤더의 x5c에서 leaf와 발급자를 꺼낸다.
//
// richzw/appstore가 내부에서 같은 일을 하지만 결과를 밖으로 주지
// 않는다. OCSP 요청에는 leaf와 발급자 둘 다 필요해서 다시 읽는다.
func certChainFromJWS(jws string) (leaf, issuer *x509.Certificate, err error) {
	headerStr, _, ok := strings.Cut(jws, ".")
	if !ok {
		return nil, nil, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 서명 형식이 올바르지 않아요")
	}

	headerRaw, err := base64.RawURLEncoding.DecodeString(headerStr)
	if err != nil {
		return nil, nil, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"App Store 서명 헤더를 읽지 못했어요")
	}

	var header struct {
		X5c []string `json:"x5c"`
	}
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return nil, nil, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"App Store 서명 헤더를 해석하지 못했어요")
	}
	if len(header.X5c) == 0 {
		return nil, nil, platformerr.New(platformerr.CodeProviderResponseInvalid,
			"App Store 서명에 인증서가 없어요")
	}

	leaf, err = parseX5cCert(header.X5c[0])
	if err != nil {
		return nil, nil, err
	}

	// 중간 인증서가 leaf의 발급자다. 없으면 폐기 확인을 건너뛴다.
	if len(header.X5c) < 2 {
		return leaf, nil, nil
	}

	issuer, err = parseX5cCert(header.X5c[1])
	if err != nil {
		return nil, nil, err
	}
	return leaf, issuer, nil
}

// parseX5cCert는 x5c 항목 하나를 인증서로 바꾼다.
//
// x5c는 표준 base64다. URL-safe가 아니다.
func parseX5cCert(s string) (*x509.Certificate, error) {
	der, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"App Store 인증서를 읽지 못했어요")
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeProviderResponseInvalid,
			"App Store 인증서를 해석하지 못했어요")
	}
	return cert, nil
}
