package toss

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// mTLS 배선을 실제 TLS 핸드셰이크로 검증한다.
//
// 다른 테스트는 WithHTTPClient로 평문 서버를 꽂아서 mTLS 경로를
// 타지 않는다. 그러면 "인증서를 제시하는가"를 확인할 수 없다.
//
// AIT가 발급하는 실제 인증서는 없지만, 자체 서명 인증서로 클라이언트
// 인증을 요구하는 서버를 띄우면 배선 자체는 검증된다. 인증서를 받는
// 즉시 동작하는지 여기서 알 수 있다.

// mtlsFixture는 CA와 서버·클라이언트 인증서 한 벌이다.
type mtlsFixture struct {
	caPool     *x509.CertPool
	serverCert tls.Certificate
	clientCert tls.Certificate
}

func newMTLSFixture(t *testing.T) mtlsFixture {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA 키 생성 실패: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "테스트 CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CA 인증서 생성 실패: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("CA 인증서 파싱 실패: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	issue := func(cn string, serial int64, forServer bool) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("%s 키 생성 실패: %v", cn, err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
		}
		if forServer {
			tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			tmpl.DNSNames = []string{"localhost"}
			tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		} else {
			tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}

		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("%s 인증서 생성 실패: %v", cn, err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}

	return mtlsFixture{
		caPool:     pool,
		serverCert: issue("ait-server", 2, true),
		clientCert: issue("ait-client", 3, false),
	}
}

// newMTLSServer는 클라이언트 인증서를 요구하는 서버를 띄운다.
func newMTLSServer(t *testing.T, f mtlsFixture, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{f.serverCert},
		// 이것이 핵심이다. 클라이언트가 인증서를 제시하지 않으면
		// 핸드셰이크 자체가 실패한다.
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  f.caPool,
		MinVersion: tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// 클라이언트 인증서를 제시하면 통신이 성립한다.
func TestMTLSHandshakeSucceeds(t *testing.T) {
	f := newMTLSFixture(t)

	var gotCN string
	srv := newMTLSServer(t, f, func(w http.ResponseWriter, r *http.Request) {
		// 서버가 클라이언트 인증서를 실제로 받았는지 확인한다.
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			gotCN = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(successBody(statusPaymentCompleted, "2026-07-31T20:30:00")))
	})

	// New가 만드는 실제 mTLS 클라이언트를 쓴다.
	// WithHTTPClient로 갈아끼우지 않는 것이 이 테스트의 요점이다.
	v, err := New(Config{
		ClientCert: f.clientCert,
		RootCAs:    f.caPool,
		BaseURL:    srv.URL,
	}, WithClock(func() time.Time { return tossNow }))
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}

	got, err := v.Verify(context.Background(), tossProof())
	if err != nil {
		t.Fatalf("mTLS 검증 실패: %v", err)
	}

	if gotCN != "ait-client" {
		t.Errorf("서버가 받은 클라이언트 CN = %q, want ait-client", gotCN)
	}
	if got.State != domain.StateActive {
		t.Errorf("state = %q", got.State)
	}
	if got.CanonicalID != testOrderID {
		t.Errorf("canonicalId = %q", got.CanonicalID)
	}
}

// 클라이언트 인증서가 없으면 핸드셰이크가 실패한다.
//
// 이것이 mTLS의 존재 이유다. 서버가 우리를 확인할 수 없으면
// 통신 자체가 성립하지 않아야 한다.
func TestMTLSRejectsMissingClientCert(t *testing.T) {
	f := newMTLSFixture(t)
	srv := newMTLSServer(t, f, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// 인증서 없이 붙는 클라이언트를 만든다.
	bare := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: f.caPool, MinVersion: tls.VersionTLS12},
		},
		Timeout: 5 * time.Second,
	}

	v, err := New(Config{
		ClientCert: f.clientCert, // 설정 검사만 통과시킨다
		BaseURL:    srv.URL,
	}, WithHTTPClient(bare), WithClock(func() time.Time { return tossNow }))
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}

	_, err = v.Verify(context.Background(), tossProof())
	if err == nil {
		t.Fatal("인증서 없이 통신이 성립했다")
	}

	// 네트워크 계층 실패다. 재시도 가치가 있는 것으로 분류된다.
	code := platformerr.CodeOf(err)
	if code != platformerr.CodeProviderUnavailable && code != platformerr.CodeProviderTimeout {
		t.Errorf("code = %q — 연결 실패를 기대했다", code)
	}
	t.Logf("인증서 없는 연결이 거부됐다: %s", code)
}

// 다른 CA가 발급한 인증서는 거부된다.
func TestMTLSRejectsUntrustedClientCert(t *testing.T) {
	server := newMTLSFixture(t)
	// 서버가 모르는 별개의 CA에서 발급한 인증서
	other := newMTLSFixture(t)

	srv := newMTLSServer(t, server, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	v, err := New(Config{
		ClientCert: other.clientCert,
		RootCAs:    server.caPool,
		BaseURL:    srv.URL,
	}, WithClock(func() time.Time { return tossNow }))
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}

	if _, err := v.Verify(context.Background(), tossProof()); err == nil {
		t.Fatal("다른 CA 인증서로 통신이 성립했다")
	}
}

// 서버 인증서를 검증하지 못하면 붙지 않는다.
//
// AIT를 사칭하는 서버에 주문 정보와 사용자 키를 보내면 안 된다.
func TestMTLSVerifiesServerCert(t *testing.T) {
	f := newMTLSFixture(t)
	srv := newMTLSServer(t, f, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// RootCAs를 주지 않으면 시스템 루트를 쓴다.
	// 자체 서명 서버 인증서는 거기 없다.
	v, err := New(Config{
		ClientCert: f.clientCert,
		BaseURL:    srv.URL,
	}, WithClock(func() time.Time { return tossNow }))
	if err != nil {
		t.Fatalf("검증기 생성 실패: %v", err)
	}

	if _, err := v.Verify(context.Background(), tossProof()); err == nil {
		t.Fatal("검증되지 않은 서버에 연결했다")
	}
}
