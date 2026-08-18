package main

import (
	"context"

	platformads "github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/config"
	platformcontent "github.com/seorilabs/platform/server/internal/content"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/store"
)

type contentParts struct {
	service *platformcontent.Service
	handler *platformcontent.Handler
	source  *platformcontent.GCSObjectSource
}

func newContent(
	ctx context.Context,
	cfg config.Config,
	st *store.Client,
	apps *registry.Registry,
	identityHandler *identity.Handler,
) (*contentParts, error) {
	source, err := platformcontent.NewGCSObjectSource(ctx)
	if err != nil {
		return nil, err
	}
	environment := "production"
	if cfg.IsStaging() {
		environment = "staging"
	}
	loader, err := platformcontent.NewReleaseLoader(source, environment)
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	repo := platformcontent.NewStoreRepository(st)
	access := platformcontent.NewAccessService(
		repo,
		platformads.NewStoreRepository(st),
		&contentEntitlements{store: st},
	)
	service, err := platformcontent.NewService(apps, loader, repo, access)
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	handler, err := platformcontent.NewHandler(service, identityHandler, identityHandler)
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	return &contentParts{service: service, handler: handler, source: source}, nil
}

// contentEntitlements는 마켓 자격증명 없이 Firestore 원장만 읽고 차감한다.
// 구매 검증은 계속 platform-iap 역할에만 남는다.
type contentEntitlements struct {
	store *store.Client
}

func contentLedger(st *store.Client, app registry.App) *ledger.Ledger {
	env := domain.EnvProduction
	if app.IAP.LedgerEnvironment == registry.LedgerSandbox {
		env = domain.EnvSandbox
	}
	return ledgerForRegistryApp(st, app, env)
}

func (e *contentEntitlements) Active(
	ctx context.Context, app registry.App, puid, entitlementID string,
) (bool, error) {
	return contentLedger(e.store, app).IsActive(ctx, puid, entitlementID)
}

func (e *contentEntitlements) SourceActive(
	ctx context.Context, app registry.App, puid, entitlementID, sourceKey string,
) (bool, error) {
	return contentLedger(e.store, app).IsSourceActive(ctx, puid, entitlementID, sourceKey)
}

func (e *contentEntitlements) Consume(
	ctx context.Context,
	app registry.App,
	puid, entitlementID string,
	unitsPerSource int,
	requestKey string,
) (string, error) {
	result, err := contentLedger(e.store, app).ConsumeUnits(
		ctx, puid, entitlementID, unitsPerSource, requestKey,
	)
	if err != nil {
		return "", err
	}
	return result.SourceKey, nil
}
