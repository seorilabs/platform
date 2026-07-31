package apple

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// OCSP 층은 richzw/appstore가 하지 않는 폐기 확인이다.
//
// 실제 Apple 인증서 없이도 검증할 수 있는 것 — x5c 파싱, 발급자 추출,
// 캐시 동작, fail-open 규칙 — 을 여기서 확인한다.
// 실제 OCSP 응답자 왕복은 샌드박스 결제에서 확인한다.

// testChain은 발급자와 leaf 인증서 한 쌍이다.
type testChain struct {
	issuerDER []byte
	leafDER   []byte
	leaf      *x509.Certificate
}

// newTestChain은 서명 검증용 인증서 두 장을 만든다.
func newTestChain(t *testing.T, ocspServer string) testChain {
	t.Helper()

	issuerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("발급자 키 생성 실패: %v", err)
	}
	issuerTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "테스트 중간 인증서"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	issuerDER, err := x509.CreateCertificate(rand.Reader, issuerTmpl, issuerTmpl,
		&issuerKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatalf("발급자 인증서 생성 실패: %v", err)
	}
	issuerCert, err := x509.ParseCertificate(issuerDER)
	if err != nil {
		t.Fatalf("발급자 인증서 파싱 실패: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf 키 생성 실패: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "테스트 서명 인증서"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if ocspServer != "" {
		leafTmpl.OCSPServer = []string{ocspServer}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, issuerCert,
		&leafKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatalf("leaf 인증서 생성 실패: %v", err)
	}
	leafCert, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("leaf 인증서 파싱 실패: %v", err)
	}

	return testChain{issuerDER: issuerDER, leafDER: leafDER, leaf: leafCert}
}

// jws는 x5c 헤더를 담은 JWS 형태 문자열을 만든다. 서명은 검증하지 않는다.
func (c testChain) jws(t *testing.T, certs ...[]byte) string {
	t.Helper()

	x5c := make([]string, 0, len(certs))
	for _, der := range certs {
		// x5c는 표준 base64다. URL-safe가 아니다.
		x5c = append(x5c, base64.StdEncoding.EncodeToString(der))
	}

	header, err := json.Marshal(map[string]any{"alg": "ES256", "x5c": x5c})
	if err != nil {
		t.Fatalf("헤더 직렬화 실패: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + ".payload.signature"
}

func TestCertChainFromJWS(t *testing.T) {
	chain := newTestChain(t, "http://ocsp.example.test")

	t.Run("leaf와 발급자를 꺼낸다", func(t *testing.T) {
		leaf, issuer, err := certChainFromJWS(chain.jws(t, chain.leafDER, chain.issuerDER))
		if err != nil {
			t.Fatalf("파싱 실패: %v", err)
		}
		if leaf.SerialNumber.Cmp(big.NewInt(42)) != 0 {
			t.Errorf("leaf 일련번호 = %v, want 42", leaf.SerialNumber)
		}
		if issuer == nil || issuer.SerialNumber.Cmp(big.NewInt(1)) != 0 {
			t.Errorf("발급자 = %v", issuer)
		}
	})

	// 중간 인증서가 없으면 OCSP 요청을 만들 수 없다. 에러가 아니라 건너뛴다.
	t.Run("leaf만 있으면 발급자는 nil", func(t *testing.T) {
		leaf, issuer, err := certChainFromJWS(chain.jws(t, chain.leafDER))
		if err != nil {
			t.Fatalf("파싱 실패: %v", err)
		}
		if leaf == nil {
			t.Fatal("leaf가 nil이다")
		}
		if issuer != nil {
			t.Error("발급자가 있으면 안 된다")
		}
	})
}

func TestCertChainFromJWSRejectsBadInput(t *testing.T) {
	chain := newTestChain(t, "")

	tests := []struct {
		name string
		jws  string
	}{
		{"점이 없다", "notajws"},
		{"헤더가 base64가 아니다", "!!!.payload.sig"},
		{"헤더가 JSON이 아니다",
			base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".p.s"},
		{"x5c가 비었다",
			base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","x5c":[]}`)) + ".p.s"},
		{"x5c 항목이 base64가 아니다",
			base64.RawURLEncoding.EncodeToString([]byte(`{"x5c":["!!!"]}`)) + ".p.s"},
		{"x5c 항목이 인증서가 아니다",
			base64.RawURLEncoding.EncodeToString(
				[]byte(`{"x5c":["`+base64.StdEncoding.EncodeToString([]byte("garbage"))+`"]}`)) + ".p.s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := certChainFromJWS(tt.jws); err == nil {
				t.Error("거부하지 않았다")
			}
		})
	}

	// x5c가 URL-safe base64면 표준 base64 디코딩에서 걸러져야 한다
	t.Run("발급자만 깨진 경우", func(t *testing.T) {
		bad := base64.RawURLEncoding.EncodeToString([]byte(
			`{"x5c":["` + base64.StdEncoding.EncodeToString(chain.leafDER) + `","!!!"]}`))
		if _, _, err := certChainFromJWS(bad + ".p.s"); err == nil {
			t.Error("깨진 발급자를 통과시켰다")
		}
	})
}

// 폐기 상태 캐시는 결제 경로의 OCSP 왕복을 없앤다.
func TestRevocationCache(t *testing.T) {
	now := appleNow
	r := newRevocationChecker(nil)
	r.now = func() time.Time { return now }

	const key = "serial"

	t.Run("만료 전에는 캐시를 쓴다", func(t *testing.T) {
		r.store(key, cachedStatus{revoked: false, expiresAt: now.Add(time.Hour)})
		if _, ok := r.lookup(key); !ok {
			t.Error("유효한 캐시를 못 찾았다")
		}
	})

	t.Run("만료되면 캐시를 무시한다", func(t *testing.T) {
		r.store(key, cachedStatus{revoked: false, expiresAt: now.Add(-time.Second)})
		if _, ok := r.lookup(key); ok {
			t.Error("만료된 캐시를 썼다")
		}
	})

	t.Run("없는 키", func(t *testing.T) {
		if _, ok := r.lookup("모르는키"); ok {
			t.Error("없는 캐시를 찾았다")
		}
	})
}

// 캐시에 폐기로 기록되어 있으면 네트워크 없이 즉시 거부한다.
func TestCheckUsesCachedRevocation(t *testing.T) {
	chain := newTestChain(t, "http://ocsp.example.test")

	r := newRevocationChecker(nil)
	r.now = func() time.Time { return appleNow }
	r.store(string(chain.leaf.SerialNumber.Bytes()),
		cachedStatus{revoked: true, expiresAt: appleNow.Add(time.Hour)})

	err := r.check(context.Background(), chain.jws(t, chain.leafDER, chain.issuerDER))
	if err == nil {
		t.Fatal("폐기된 인증서를 통과시켰다")
	}
	if code := platformerr.CodeOf(err); code != platformerr.CodeProviderResponseInvalid {
		t.Errorf("code = %q, want provider_response_invalid", code)
	}
	if !strings.Contains(err.Error(), "폐기") {
		t.Errorf("메시지가 폐기를 알리지 않는다: %v", err)
	}
}

// 발급자가 없으면 OCSP 요청 자체를 만들 수 없다. 통과시킨다.
func TestCheckSkipsWithoutIssuer(t *testing.T) {
	chain := newTestChain(t, "http://ocsp.example.test")
	r := newRevocationChecker(nil)
	r.now = func() time.Time { return appleNow }

	if err := r.check(context.Background(), chain.jws(t, chain.leafDER)); err != nil {
		t.Errorf("발급자 없음을 에러로 만들었다: %v", err)
	}
}

// fail-open. OCSP 응답자에 닿지 못하는 것은 폐기가 아니다.
//
// 응답자 장애로 정상 결제가 전부 막히는 쪽이 더 나쁘다.
// 체인 검증은 이미 통과한 상태라 위조 난이도는 여전히 높다.
func TestCheckFailsOpenOnUnreachableResponder(t *testing.T) {
	// 127.0.0.1의 닫힌 포트 — 즉시 연결 거부된다
	chain := newTestChain(t, "http://127.0.0.1:1/ocsp")
	r := newRevocationChecker(nil)
	r.now = func() time.Time { return appleNow }

	err := r.check(context.Background(), chain.jws(t, chain.leafDER, chain.issuerDER))
	if err != nil {
		t.Errorf("응답자 장애를 폐기로 취급했다: %v", err)
	}
}

// OCSP 주소가 없는 인증서도 통과시킨다.
func TestCheckFailsOpenWithoutOCSPServer(t *testing.T) {
	chain := newTestChain(t, "")
	r := newRevocationChecker(nil)
	r.now = func() time.Time { return appleNow }

	if err := r.check(context.Background(), chain.jws(t, chain.leafDER, chain.issuerDER)); err != nil {
		t.Errorf("OCSP 주소 없음을 에러로 만들었다: %v", err)
	}
}

// 깨진 JWS는 통과시키지 않는다. fail-open은 네트워크 실패에만 적용된다.
func TestCheckRejectsMalformedJWS(t *testing.T) {
	r := newRevocationChecker(nil)
	r.now = func() time.Time { return appleNow }

	if err := r.check(context.Background(), "쓰레기"); err == nil {
		t.Error("깨진 JWS를 통과시켰다")
	}
}
