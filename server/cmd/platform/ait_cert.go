package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/config"
)

// aitCertificate는 미니앱 하나에 묶인 AppsInToss mTLS 인증서다.
type aitCertificate struct {
	AppID    string
	Source   string
	Cert     tls.Certificate
	NotAfter time.Time
}

// aitCertificatesByApp은 mTLS 자격증명을 미니앱별로 묶는다.
//
// 어느 앱의 것인지는 **인증서 CN**이 정한다. 토스 파트너 API가 CN으로 미니앱을
// 식별하므로, 배포 설정의 이름이 아니라 인증서 자체가 소유자를 말해야 한다.
// 다른 앱의 인증서로 교환을 시도하면 토스는 등록되지 않은 미니앱으로 보고 거절한다.
//
// CN이 겹치면 어느 쪽이 이겼는지 모르는 채 뜨므로 부팅을 막는다.
func aitCertificatesByApp(
	credentials []config.TossClientCredential,
	roleName string,
) (map[string]aitCertificate, error) {
	byApp := make(map[string]aitCertificate, len(credentials))
	for _, credential := range credentials {
		cert, err := tls.X509KeyPair(credential.CertPEM, credential.KeyPEM)
		if err != nil {
			return nil, fmt.Errorf("%s: %s AppsInToss 인증서를 읽지 못했다: %w",
				roleName, credential.Source, err)
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %s AppsInToss 인증서를 해석하지 못했다: %w",
				roleName, credential.Source, err)
		}
		appID := strings.TrimSpace(leaf.Subject.CommonName)
		if appID == "" {
			return nil, fmt.Errorf("%s: %s AppsInToss 인증서에 CN이 없다",
				roleName, credential.Source)
		}
		if previous, duplicated := byApp[appID]; duplicated {
			return nil, fmt.Errorf("%s: AppsInToss 인증서 CN이 겹친다: %s(%s, %s)",
				roleName, appID, previous.Source, credential.Source)
		}
		byApp[appID] = aitCertificate{
			AppID:    appID,
			Source:   credential.Source,
			Cert:     cert,
			NotAfter: leaf.NotAfter.UTC(),
		}
	}
	return byApp, nil
}
