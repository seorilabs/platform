package main

import (
	"context"

	platformads "github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/store"
)

type adsParts struct {
	service *platformads.Service
	repo    *platformads.StoreRepository
}

// adsEntitlements는 광고 정책이 앱별 IAP 원장 환경을 읽게 한다.
// platform-iap의 전역 환경 설정을 재사용하면 sandbox인 lizard와
// production인 Happy Farm이 같은 projection을 보게 되므로 두 원장을
// 명시적으로 분리한다.
type adsEntitlements struct {
	store *store.Client
}

func newAdsEntitlements(st *store.Client) *adsEntitlements {
	return &adsEntitlements{store: st}
}

func (a *adsEntitlements) ListActiveForApp(ctx context.Context, app registry.App, puid string) ([]string, error) {
	if !app.FeatureEnabled("iap") {
		return []string{}, nil
	}
	env := domain.EnvProduction
	if app.IAP.LedgerEnvironment == registry.LedgerSandbox {
		env = domain.EnvSandbox
	}
	return ledgerForRegistryApp(a.store, app, env).ListActive(ctx, puid)
}
