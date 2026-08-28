package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/config"
)

// aitTestCredential은 CN만 다른 mTLS 자격증명을 만든다.
func aitTestCredential(t *testing.T, commonName, source string) config.TossClientCredential {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return config.TossClientCredential{
		Source:  source,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
}

func TestAITCertificatesByAppKeysOnCommonName(t *testing.T) {
	byApp, err := aitCertificatesByApp([]config.TossClientCredential{
		aitTestCredential(t, "lizard-tycoon", "IAP_TOSS_CLIENT_CERT"),
		aitTestCredential(t, "ungeul", "IAP_TOSS_CLIENT_CERT_UNGEUL"),
	}, "iap")
	if err != nil {
		t.Fatal(err)
	}
	if len(byApp) != 2 {
		t.Fatalf("byApp=%v", byApp)
	}
	if got := byApp["ungeul"].Source; got != "IAP_TOSS_CLIENT_CERT_UNGEUL" {
		t.Fatalf("ungeul source=%q", got)
	}
	if _, ok := byApp["lizard-tycoon"]; !ok {
		t.Fatal("lizard-tycoon 인증서가 없다")
	}
}

// 같은 CN이 둘이면 어느 인증서가 쓰였는지 알 수 없는 채로 뜬다.
// 그 상태는 로그에도 남지 않으므로 부팅을 막는다.
func TestAITCertificatesByAppRejectsDuplicateCommonName(t *testing.T) {
	_, err := aitCertificatesByApp([]config.TossClientCredential{
		aitTestCredential(t, "ungeul", "IAP_TOSS_CLIENT_CERT"),
		aitTestCredential(t, "ungeul", "IAP_TOSS_CLIENT_CERT_UNGEUL"),
	}, "iap")
	if err == nil || !strings.Contains(err.Error(), "겹친다") {
		t.Fatalf("err=%v", err)
	}
}
