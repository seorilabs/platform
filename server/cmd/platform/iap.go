package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"

	"golang.org/x/oauth2"
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
	"github.com/seorilabs/platform/server/internal/iap/refundreview"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
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
	service    *verify.Service
	ledger     *ledger.Ledger
	catalog    *catalog.Catalog
	verifiers  map[domain.Platform]verify.Verifier
	enabled    []domain.Platform
	refundKeys *refundreview.Keyring
	appLedgers map[string]*ledger.Ledger
	// appVerifiers는 webhook과 worker까지 앱 범위를 유지한다. verify
	// 요청에서만 앱을 나누고 여기서 전역 검증기로 돌아가면 환불과 완료
	// 처리가 lizard 설정으로 Happy Farm 주문을 호출하게 된다.
	appVerifiers map[string]map[domain.Platform]verify.Verifier
	apps         map[string]registry.App
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
	reg *registry.Registry,
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
	cat, err := catalog.Parse(ic.CatalogJSON, nil)
	if err != nil {
		return nil, err
	}

	keyring, err := binding.NewKeyring(ic.BindingKeys...)
	if err != nil {
		return nil, err
	}

	var refundKeys *refundreview.Keyring
	if ic.Play.Enabled() {
		keys := make([]refundreview.Key, 0, len(ic.RefundReviewKeys))
		for _, key := range ic.RefundReviewKeys {
			keys = append(keys, refundreview.Key{ID: key.ID, Material: key.Material})
		}
		refundKeys, err = refundreview.NewKeyring(keys...)
		if err != nil {
			return nil, err
		}
	}

	led := ledger.New(st, env)
	apps, err := reg.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("iap: 앱 레지스트리를 읽지 못했다: %w", err)
	}
	appLedgers := make(map[string]verify.Ledger)
	appLedgerValues := make(map[string]*ledger.Ledger)
	appOutboxes := make(map[string]verify.OutboxWriter)
	appVerifiers := make(map[string][]verify.Verifier)
	appVerifierMaps := make(map[string]map[domain.Platform]verify.Verifier)
	appsByID := make(map[string]registry.App)
	for _, app := range apps {
		if !app.FeatureEnabled("iap") {
			continue
		}
		appEnv := domain.EnvProduction
		if app.IAP.LedgerEnvironment == registry.LedgerSandbox {
			appEnv = domain.EnvSandbox
		}
		appLedger := ledgerForRegistryApp(st, app, appEnv)
		appLedgers[app.AppID] = appLedger
		appLedgerValues[app.AppID] = appLedger
		appsByID[app.AppID] = app
		appOutboxes[app.AppID] = appLedger
		list, err := newVerifiersForApp(ctx, ic, app)
		if err != nil {
			return nil, err
		}
		appVerifiers[app.AppID] = list
		appVerifierMaps[app.AppID] = make(map[domain.Platform]verify.Verifier, len(list))
		for _, verifier := range list {
			appVerifierMaps[app.AppID][verifier.Platform()] = verifier
		}
		for _, entitlementID := range app.IAP.EntitlementIDs {
			if !cat.HasForApp(app.AppID, entitlementID) {
				return nil, platformerr.Newf(platformerr.CodeCatalogIncomplete, "%s의 %s entitlement가 앱별 카탈로그에 없어요", app.AppID, entitlementID)
			}
			for _, market := range app.IAP.Markets {
				if _, ok := cat.SKUForApp(app.AppID, entitlementID, domain.Platform(market)); !ok {
					return nil, platformerr.Newf(platformerr.CodeCatalogIncomplete,
						"%s의 %s entitlement에 %s SKU가 없어요", app.AppID, entitlementID, market)
				}
			}
		}
	}

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
		Outbox:       led,
		AppVerifiers: appVerifiers,
		AppLedgers:   appLedgers,
		AppOutboxes:  appOutboxes,
		Apps:         reg,
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
		service:      svc,
		ledger:       led,
		catalog:      cat,
		verifiers:    byPlatform,
		enabled:      enabled,
		refundKeys:   refundKeys,
		appLedgers:   appLedgerValues,
		appVerifiers: appVerifierMaps,
		apps:         appsByID,
	}, nil
}

func ledgerForRegistryApp(st *store.Client, app registry.App, env domain.Environment) *ledger.Ledger {
	if app.IAP.LegacyUnscopedLedger {
		return ledger.New(st, env)
	}
	return ledger.NewForApp(st, env, app.AppID)
}

func newVerifiersForApp(ctx context.Context, ic config.IAPConfig, app registry.App) ([]verify.Verifier, error) {
	var out []verify.Verifier
	if app.MarketEnabled(string(domain.PlatformGooglePlay)) && ic.Play.Enabled() {
		client, err := newPlayHTTPClient(ctx, ic.Play)
		if err != nil {
			return nil, err
		}
		v, err := play.New(app.IAP.GooglePlayPackageName, client)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if app.MarketEnabled(string(domain.PlatformAppStore)) && ic.Apple.Enabled() {
		client, err := apple.NewClient(apple.Config{KeyContent: ic.Apple.KeyContent, KeyID: ic.Apple.KeyID, Issuer: ic.Apple.Issuer, BundleID: app.IAP.AppStoreBundleID, Sandbox: app.IAP.LedgerEnvironment == registry.LedgerSandbox, RequireOCSP: app.IAP.LedgerEnvironment != registry.LedgerSandbox})
		if err != nil {
			return nil, err
		}
		v, err := apple.New(client, app.IAP.AppStoreBundleID, app.IAP.LedgerEnvironment == registry.LedgerSandbox)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if app.MarketEnabled(string(domain.PlatformAppsInToss)) && ic.Toss.Enabled() {
		cert, err := tls.X509KeyPair(ic.Toss.ClientCertPEM, ic.Toss.ClientKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("iap: AppsInToss 인증서를 읽지 못했다: %w", err)
		}
		v, err := toss.New(toss.Config{ClientCert: cert, BaseURL: ic.Toss.BaseURL})
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// newAdminIAP는 검증기 없이 원장만 조립한다.
//
// Admin API는 원장을 읽고 운영자 지급을 쓴다. 앱에 허용된 entitlement인지
// 확인할 카탈로그는 필요하지만 마켓에 물어볼 일이 없어 자격증명과 계정
// 바인딩 키는 조립하지 않는다.
func newAdminIAP(cfg config.Config, st *store.Client) (*iapParts, error) {
	env := domain.EnvProduction
	if cfg.IAP.IsSandbox() {
		env = domain.EnvSandbox
	}

	cat, err := catalog.Parse(cfg.IAP.CatalogJSON, nil)
	if err != nil {
		return nil, err
	}

	slog.Info("Admin 원장 준비 완료",
		"environment", cfg.IAP.Environment,
		"entitlements", len(cat.IDs()),
	)

	return &iapParts{
		ledger:    ledger.New(st, env),
		catalog:   cat,
		verifiers: map[domain.Platform]verify.Verifier{},
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
		httpClient, err := newPlayHTTPClient(ctx, ic.Play)
		if err != nil {
			return nil, nil, err
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

// newPlayHTTPClient는 Play Developer API용 클라이언트를 만든다.
//
// 기본은 ADC다. SA JSON 키를 배포하지 않는다는 조직 원칙이고,
// 런타임 SA에 Play Console 권한을 부여하는 방식이다.
//
// Play publisher 계정이 다른 조직에 있으면 그 방식이 안 된다.
// 그때만 전용 자격증명을 준다. 전역 ADC로 두면 Firestore 접근까지
// 그 SA로 바뀌어 다른 프로젝트를 못 읽는다.
func newPlayHTTPClient(ctx context.Context, cfg config.PlayConfig) (*http.Client, error) {
	if len(cfg.ServiceAccountJSON) == 0 {
		client, err := google.DefaultClient(ctx, playScope)
		if err != nil {
			return nil, fmt.Errorf("iap: Play 자격증명을 얻지 못했다: %w", err)
		}
		return client, nil
	}

	creds, err := google.CredentialsFromJSONWithTypeAndParams(
		ctx,
		cfg.ServiceAccountJSON,
		google.ServiceAccount,
		google.CredentialsParams{Scopes: []string{playScope}},
	)
	if err != nil {
		return nil, fmt.Errorf("iap: Play 전용 자격증명을 읽지 못했다: %w", err)
	}
	return oauth2.NewClient(ctx, creds.TokenSource), nil
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
	a.col.Audit(ctx, newAuditRow(action, appID, puid, outcome, detail))
}

// newAuditRow는 detail에만 있던 운영자와 멱등 키를 BigQuery의 검색 가능한
// 고정 컬럼에도 올린다. detail JSON만 채우면 장애 대응 SQL이 매 행의 JSON을
// 파싱해야 하고 request_id 기준 재시도 추적도 인덱스 경계를 잃는다.
func newAuditRow(action, appID, puid, outcome string, detail map[string]any) events.AuditRow {
	row := events.AuditRow{
		Action:         events.AuditAction(action),
		AppID:          appID,
		PlatformUserID: puid,
		Outcome:        outcome,
		Detail:         detail,
	}
	if actor, ok := detail["actor"].(string); ok {
		row.Actor = actor
	}
	if requestID, ok := detail["request_id"].(string); ok {
		row.RequestID = requestID
	}
	return row
}
