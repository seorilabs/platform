// Command platform은 Seorilabs 공통 플랫폼 서버다.
//
// 단일 바이너리이고 PLATFORM_ROLE 환경변수로 역할을 나눈다.
// 코드베이스는 하나이고 네트워크 경계가 없다. 마이크로서비스가 아니다.
//
// 역할을 나누는 이유는 셋이며 전부 실질적이다.
//   - IAM 폭발 반경: platform-iap 만 마켓 자격증명을 갖는다
//   - 비용 격벽: ingest 폭주가 max-instances 를 다 먹어 결제를 죽이면 안 된다
//   - 동시성 튜닝이 정반대: ingest 는 I/O 바운드 write-only, api 는 캐시 + 읽기
//
// docs/03-architecture/overview.md 참고.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	platformads "github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/config"
	"github.com/seorilabs/platform/server/internal/events"
	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/remoteconfig"
	"github.com/seorilabs/platform/server/internal/store"
)

func main() {
	// main은 얇게 두고 로직은 run에 둔다.
	// os.Exit는 defer를 실행하지 않으므로 한 곳에서만 부른다.
	if err := run(); err != nil {
		slog.Error("종료", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// 구조화 JSON 로깅. Cloud Logging이 필드를 그대로 인덱싱한다.
	// 토큰·영수증·purchaseToken 원문은 절대 로그에 남기지 않는다. AGENTS.md 참고.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Role == config.RoleWorker {
		return runWorker(ctx, cfg)
	}

	deps, err := newDeps(ctx, cfg)
	if err != nil {
		return err
	}
	defer deps.Close()

	handler, err := buildHandler(cfg, deps)
	if err != nil {
		return err
	}

	// 캐시를 미리 채워 첫 요청이 키셋과 레지스트리 왕복을 기다리지 않게 한다.
	// 콜드스타트 425ms에 네트워크 왕복이 더 붙으면 결제 경로에서 체감된다.
	deps.warm(ctx)

	return serve(ctx, cfg, handler)
}

// deps는 role들이 공유하는 의존성이다.
//
// composition root에서만 조립한다. 패키지끼리 직접 조립하지 않는다.
type deps struct {
	store    *store.Client
	registry *registry.Registry
	identity *identity.Handler
	// adminUsers는 세션 issuer 없이 PII 없는 사용자 조회만 제공한다.
	adminUsers *identity.StoreRepository
	keys       *identity.KeyCache
	events     *events.Collector
	config     *remoteconfig.Service
	iap        *iapParts
	ads        *adsParts
}

func newDeps(ctx context.Context, cfg config.Config) (*deps, error) {
	st, err := store.New(ctx, cfg.ProjectID, cfg.FirestorePrefix)
	if err != nil {
		return nil, err
	}
	closeStore := func() {
		if closeErr := st.Close(); closeErr != nil {
			slog.Error("Firestore 종료 실패", "err", closeErr)
		}
	}

	reg := registry.New(registry.NewStoreSource(st))

	d := &deps{
		store:    st,
		registry: reg,
		config:   remoteconfig.NewService(st),
	}

	// 최종 사용자 세션을 발급하는 role만 identity issuer를 조립한다.
	// Admin은 Config에 비밀이 잘못 들어와도 issuer를 만들지 않는다.
	if cfg.Role == config.RoleAPI || cfg.Role == config.RoleIAP || cfg.Role == config.RoleAds {
		if len(cfg.SessionSecret) == 0 {
			closeStore()
			return nil, errors.New("identity role에 세션 비밀키가 필요하다")
		}
		keys := identity.NewKeyCache(nil)
		issuer, err := identity.NewSessionIssuer(cfg.SessionSecret, cfg.SessionTTL)
		if err != nil {
			closeStore()
			return nil, err
		}

		users := identity.NewStoreRepository(st)
		svc := identity.NewService(
			reg,
			identity.NewFirebaseVerifier(keys),
			users,
			issuer,
		)
		if cfg.Role == config.RoleAPI {
			customTokens, err := identity.NewIAMCustomTokenIssuer(ctx)
			if err != nil {
				closeStore()
				return nil, err
			}
			svc.WithCustomTokenIssuer(customTokens)
			svc.WithAppCheckVerifier(identity.NewFirebaseAppCheckVerifier())
		}
		// Toss Login의 authorization code 교환도 AppsInToss mTLS를 쓴다.
		// 결제 앱은 자격증명이 이미 격리된 iap role에서 세션을 열고,
		// 광고 전용 앱은 ads role에서 같은 경계를 사용한다.
		var aitCertPEM, aitKeyPEM []byte
		var aitBaseURL, aitRoleName string
		switch {
		case cfg.Role == config.RoleIAP && cfg.IAP.Toss.Enabled():
			aitCertPEM = cfg.IAP.Toss.ClientCertPEM
			aitKeyPEM = cfg.IAP.Toss.ClientKeyPEM
			aitBaseURL = cfg.IAP.Toss.BaseURL
			aitRoleName = "iap"
		case cfg.Role == config.RoleAds && cfg.Ads.AITLoginEnabled():
			aitCertPEM = cfg.Ads.AITClientCertPEM
			aitKeyPEM = cfg.Ads.AITClientKeyPEM
			aitBaseURL = cfg.Ads.AITBaseURL
			aitRoleName = "ads"
		}
		if len(aitCertPEM) > 0 {
			cert, err := tls.X509KeyPair(aitCertPEM, aitKeyPEM)
			if err != nil {
				closeStore()
				return nil, fmt.Errorf("%s: AppsInToss 로그인 인증서를 읽지 못했다: %w", aitRoleName, err)
			}
			client, err := identity.NewAITLoginClient(cert, aitBaseURL)
			if err != nil {
				closeStore()
				return nil, err
			}
			svc.WithAITLoginVerifier(client)
		}
		d.identity = identity.NewHandler(svc)
		d.adminUsers = users
		d.keys = keys
	}
	if cfg.Role == config.RoleAdmin {
		// Admin은 플랫폼 세션을 발급하거나 검증하지 않는다. 사용자 지원 조회에
		// 필요한 저장소 포트만 조립하므로 PLATFORM_SESSION_SECRET이 필요 없다.
		d.adminUsers = identity.NewStoreRepository(st)
	}

	// 이벤트를 다루는 role만 BigQuery에 붙는다.
	// api는 감사 원장을 남겨야 하므로 함께 연다.
	if cfg.Role == config.RoleIngest || cfg.Role == config.RoleAPI ||
		cfg.Role == config.RoleIAP || cfg.Role == config.RoleAdmin {
		col, err := events.NewCollector(ctx, cfg.ProjectID, cfg.BigQueryDataset)
		if err != nil {
			closeStore()
			return nil, err
		}
		d.events = col
	}

	// 마켓 자격증명은 iap와 worker role에만 마운트된다. R3다.
	// 워커도 완료 재시도 때 마켓을 호출하므로 검증기가 필요하다.
	switch cfg.Role {
	case config.RoleIAP, config.RoleWorker:
		svc, err := newIAPService(ctx, cfg, st, d.events, reg)
		if err != nil {
			closeStore()
			return nil, err
		}
		d.iap = svc

	case config.RoleAdmin:
		// admin은 원장을 읽고 운영자 지급을 쓴다. 마켓에 묻지 않는다.
		// 검증기를 조립하려 들면 없는 자격증명을 찾다가 부팅이 실패하고,
		// 자격증명을 붙이면 폭발 반경이 admin까지 넓어진다.
		d.iap, err = newAdminIAP(cfg, st)
		if err != nil {
			closeStore()
			return nil, err
		}
	}

	if cfg.Role == config.RoleAds || cfg.Role == config.RoleAdmin {
		repo := platformads.NewStoreRepository(st)
		entitlements := newAdsEntitlements(st)
		service, err := platformads.NewService(repo, reg, entitlements, d.adminUsers)
		if err != nil {
			closeStore()
			return nil, err
		}
		d.ads = &adsParts{service: service, repo: repo}
	}

	return d, nil
}

func (d *deps) Close() {
	if d.events != nil {
		if err := d.events.Close(); err != nil {
			slog.Error("BigQuery 종료 실패", "err", err)
		}
	}
	if d.store != nil {
		if err := d.store.Close(); err != nil {
			slog.Error("store 종료 실패", "err", err)
		}
	}
}

// warm은 첫 요청이 기다리지 않도록 캐시를 미리 채운다.
//
// 실패해도 서버는 뜬다. 첫 요청 시점에 다시 시도한다.
// 부팅을 막으면 일시적인 네트워크 문제로 배포가 실패한다.
func (d *deps) warm(ctx context.Context) {
	if d.keys != nil {
		if err := d.keys.Warm(ctx); err != nil {
			slog.WarnContext(ctx, "Firebase 키셋 예열 실패", "err", err)
		}
	}
	if err := d.registry.Reload(ctx); err != nil {
		slog.WarnContext(ctx, "레지스트리 예열 실패", "err", err)
	}
	// 테이블이 없으면 만든다. 이미 있으면 아무것도 하지 않는다.
	if d.events != nil {
		if err := d.events.EnsureTables(ctx); err != nil {
			slog.WarnContext(ctx, "BigQuery 테이블 준비 실패", "err", err)
		}
	}
}

func buildHandler(cfg config.Config, d *deps) (http.Handler, error) {
	mux := http.NewServeMux()

	// 헬스체크는 계약이 아니라 인프라 관심사다. spec/openapi.yaml에 두지 않는다.
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		// 레지스트리를 읽을 수 있어야 요청을 받을 준비가 된 것이다.
		if _, err := d.registry.List(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintln(w, "not ready")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ready")
	})

	switch cfg.Role {
	case config.RoleAPI:
		if d.identity == nil {
			return nil, errors.New("api role에 identity가 필요하다")
		}
		d.identity.Register(mux)
		remoteconfig.NewHandler(d.config, d.registry).Register(mux)
		// entitlement 조회는 여기가 아니라 iap role이 맡는다.
		// GET /v1/iap/entitlements 하나로 통일했다. 마켓 자격증명이
		// 붙은 서비스에 원장 읽기를 모아 두는 편이 경로가 줄어든다.

	case config.RoleIAP:
		if d.identity == nil {
			return nil, errors.New("iap role에 identity가 필요하다")
		}
		if d.iap == nil {
			return nil, errors.New("iap role에 결제 서비스가 필요하다")
		}
		// AIT 앱은 appLogin authorization code를 이 role의 mTLS
		// 자격증명으로 교환한 뒤 같은 호스트에서 구매를 검증한다.
		d.identity.RegisterSession(mux)
		iap.NewHandler(d.iap.service, d.identity).Register(mux)

		// 웹훅은 마켓별로 자격증명이 있을 때만 연다.
		// 없는 마켓의 엔드포인트를 열면 인증도 못 하고 알림만 쌓인다.
		if err := registerWebhooks(mux, cfg, d); err != nil {
			return nil, err
		}

	case config.RoleAds:
		if d.identity == nil || d.ads == nil {
			return nil, errors.New("ads role에 identity와 광고 서비스가 필요하다")
		}
		d.identity.RegisterSession(mux)
		platformads.NewHandler(d.ads.service, d.identity,
			platformads.NewAdMobVerifier(nil, cfg.Ads.AdMobVerifierKeysURL)).Register(mux)

	case config.RoleIngest:
		if d.events == nil {
			return nil, errors.New("ingest role에 BigQuery가 필요하다")
		}
		// 세션은 선택이다. identity가 없어도 익명 수집이 동작한다.
		var sessions events.SessionResolver
		if d.identity != nil {
			sessions = d.identity
		}
		events.NewHandler(d.events, d.registry, sessions).Register(mux)

	case config.RoleAdmin:
		if d.iap == nil {
			return nil, errors.New("admin role에 결제 설정이 필요하다")
		}
		if err := registerAdmin(mux, d); err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("HTTP를 열지 않는 role이다: %s", cfg.Role)
	}

	// 앞에 쓴 미들웨어가 바깥이다. 요청을 먼저 본다.
	// Recover가 가장 바깥이어야 다른 미들웨어의 패닉도 잡는다.
	return httpx.Chain(mux,
		httpx.Recover(),
		httpx.RequestID(),
		httpx.AccessLog(),
		httpx.CORS(d.registry),
		httpx.Timeout(30*time.Second),
	), nil
}

func serve(ctx context.Context, cfg config.Config, handler http.Handler) error {
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Apple 검증은 외부 API 왕복이 붙어 오래 걸릴 수 있다.
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("서버 시작",
			"role", cfg.Role,
			"port", cfg.Port,
			"project", cfg.ProjectID,
			"staging", cfg.IsStaging(),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("서버 실행 실패: %w", err)
	case <-ctx.Done():
		slog.Info("종료 신호 수신")
	}

	// Cloud Run이 SIGTERM을 보내면 진행 중 요청을 마치고 내려간다.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown 실패: %w", err)
	}
	slog.Info("정상 종료")
	return nil
}
