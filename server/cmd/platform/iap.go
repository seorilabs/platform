package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"

	"golang.org/x/oauth2/google"

	"github.com/seorilabs/platform/server/internal/config"
	"github.com/seorilabs/platform/server/internal/events"
	"github.com/seorilabs/platform/server/internal/iap/binding"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/iap/providers/apple"
	"github.com/seorilabs/platform/server/internal/iap/providers/play"
	"github.com/seorilabs/platform/server/internal/iap/providers/toss"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/store"
)

// playScope는 Play Developer API 접근 범위다.
//
// androidpublisher 패키지를 import하지 않고 문자열만 쓴다.
// 우리는 자체 HTTP 클라이언트를 쓰므로 생성 코드 전체가 필요 없다.
const playScope = "https://www.googleapis.com/auth/androidpublisher"

// iapParts는 결제 조립 결과다.
//
// 웹훅과 워커가 검증기와 원장을 직접 써야 해서 함께 돌려준다.
type iapParts struct {
	service   *verify.Service
	ledger    *ledger.Ledger
	verifiers map[domain.Platform]verify.Verifier
	enabled   []domain.Platform
}

// newIAPService는 결제 유스케이스를 조립한다.
//
// 자격증명이 없는 마켓은 조용히 빠진다. 그 마켓 결제만
// platform_unavailable로 거부되고 나머지는 정상 동작한다.
// AIT 인증서 미확보 같은 상황이 실제로 있어서 전부-아니면-전무로 두지 않는다.
func newIAPService(
	ctx context.Context,
	cfg config.Config,
	st *store.Client,
	col *events.Collector,
) (*iapParts, error) {
	ic := cfg.IAP

	env := domain.EnvProduction
	if ic.IsSandbox() {
		env = domain.EnvSandbox
	}

	verifiers, enabled, err := newVerifiers(ctx, ic)
	if err != nil {
		return nil, err
	}
	if len(verifiers) == 0 {
		return nil, fmt.Errorf("iap: 조립된 마켓 검증기가 하나도 없다")
	}

	// 카탈로그는 활성화된 마켓의 SKU만 요구한다.
	// AIT를 못 붙인 상태에서 AIT SKU를 강제하면 부팅이 막힌다.
	cat, err := catalog.Parse(ic.CatalogJSON, enabled)
	if err != nil {
		return nil, err
	}

	keyring, err := binding.NewKeyring(ic.BindingKeys...)
	if err != nil {
		return nil, err
	}

	led := ledger.New(st, env)

	byPlatform := make(map[domain.Platform]verify.Verifier, len(verifiers))
	for _, v := range verifiers {
		byPlatform[v.Platform()] = v
	}

	svc, err := verify.New(verify.Config{
		Verifiers: verifiers,
		Ledger:    led,
		Catalog:   cat,
		Keyring:   keyring,
		Auditor:   auditAdapter{col: col},
		// 완료 호출이 실패해도 지급은 롤백하지 않는다. 불변식 7이다.
		// 대신 여기 쌓아두고 워커가 다시 시도한다.
		Outbox: led,
	})
	if err != nil {
		return nil, err
	}

	slog.Info("결제 준비 완료",
		"environment", ic.Environment,
		"markets", enabled,
		"entitlements", len(cat.IDs()),
	)
	return &iapParts{
		service:   svc,
		ledger:    led,
		verifiers: byPlatform,
		enabled:   enabled,
	}, nil
}

// newVerifiers는 자격증명이 있는 마켓의 검증기를 만든다.
//
// 조립에 성공한 마켓 목록을 함께 돌려준다. 카탈로그 검사 기준이 된다.
func newVerifiers(ctx context.Context, ic config.IAPConfig) ([]verify.Verifier, []domain.Platform, error) {
	var (
		verifiers []verify.Verifier
		enabled   []domain.Platform
	)

	if ic.Play.Enabled() {
		// ADC를 쓴다. SA JSON 키를 배포하지 않는다는 조직 원칙이다.
		// 런타임 SA에 Play Console 권한을 부여하는 방식이다.
		httpClient, err := google.DefaultClient(ctx, playScope)
		if err != nil {
			return nil, nil, fmt.Errorf("iap: Play 자격증명을 얻지 못했다: %w", err)
		}
		v, err := play.New(ic.Play.PackageName, httpClient)
		if err != nil {
			return nil, nil, err
		}
		verifiers = append(verifiers, v)
		enabled = append(enabled, domain.PlatformGooglePlay)
	} else {
		slog.Warn("Play 자격증명이 없어 건너뛴다")
	}

	if ic.Apple.Enabled() {
		client, err := apple.NewClient(apple.Config{
			KeyContent: ic.Apple.KeyContent,
			KeyID:      ic.Apple.KeyID,
			Issuer:     ic.Apple.Issuer,
			BundleID:   ic.Apple.BundleID,
			Sandbox:    ic.Apple.Sandbox,
			// production은 인증서 폐기 확인을 우회하지 않는다.
			// NewClient가 이 조합을 부팅 시점에 강제한다.
			RequireOCSP: !ic.Apple.Sandbox,
		})
		if err != nil {
			return nil, nil, err
		}
		v, err := apple.New(client, ic.Apple.BundleID, ic.Apple.Sandbox)
		if err != nil {
			return nil, nil, err
		}
		verifiers = append(verifiers, v)
		enabled = append(enabled, domain.PlatformAppStore)
	} else {
		slog.Warn("App Store 자격증명이 없어 건너뛴다")
	}

	if ic.Toss.Enabled() {
		cert, err := tls.X509KeyPair(ic.Toss.ClientCertPEM, ic.Toss.ClientKeyPEM)
		if err != nil {
			return nil, nil, fmt.Errorf("iap: AppsInToss 인증서를 읽지 못했다: %w", err)
		}
		v, err := toss.New(toss.Config{ClientCert: cert, BaseURL: ic.Toss.BaseURL})
		if err != nil {
			return nil, nil, err
		}
		verifiers = append(verifiers, v)
		enabled = append(enabled, domain.PlatformAppsInToss)
	} else {
		slog.Warn("AppsInToss 인증서가 없어 건너뛴다")
	}

	return verifiers, enabled, nil
}

// auditAdapter는 verify.Auditor를 events.Collector에 잇는다.
//
// verify는 감사 기록의 형태를 모르고, events는 결제를 모른다.
// 둘을 아는 것은 composition root뿐이다.
type auditAdapter struct {
	col *events.Collector
}

func (a auditAdapter) Record(
	ctx context.Context,
	action, appID, puid, outcome string,
	detail map[string]any,
) {
	if a.col == nil {
		return
	}
	a.col.Audit(ctx, events.AuditRow{
		Action:         events.AuditAction(action),
		AppID:          appID,
		PlatformUserID: puid,
		Outcome:        outcome,
		Detail:         detail,
	})
}
