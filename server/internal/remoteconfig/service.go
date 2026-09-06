package remoteconfig

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/store"
)

// configsCollection은 앱별 설정이 들어가는 컬렉션이다.
const configsCollection = "configs"

// DefaultTTL은 캐시 수명이다.
//
// 값 변경이 60초 안에 반영돼야 한다. kill switch가 여기 걸려 있어
// 너무 길면 사고 대응이 늦어진다.
const DefaultTTL = 60 * time.Second

// recommendTTL은 자동 추종 값의 캐시 수명이다.
//
// 릴리스 채택은 초 단위로 움직이지 않는다. 설정보다 길게 잡아 Firestore
// 조회를 줄인다.
const recommendTTL = 10 * time.Minute

// observedVersionLimit은 한 앱에서 읽어올 관측 문서 상한이다.
//
// 정렬 없이 읽고 메모리에서 최댓값을 구한다. Firestore에서 appId 필터와
// firstSeenAt 정렬을 함께 걸면 복합 인덱스가 필요하고, 인덱스 누락은 배포
// 후에야 500으로 드러난다.
const observedVersionLimit = 500

// RecommendSoak은 자동 추종 후보가 되기까지 필요한 관측 경과 시간이다.
//
// 내부 테스트 트랙 기기 한 대만으로도 관측 문서는 생긴다. 그걸 바로 권장
// 기준으로 삼으면 아직 아무도 받을 수 없는 버전으로 전원을 안내하게 된다.
const RecommendSoak = 24 * time.Hour

// ObservedAppVersion은 세션 경로에서 실제로 관측된 빌드다.
//
// 마켓 업로드도 태그도 아니고 그 빌드로 실제 세션이 처음 열린 기록이다.
// "스토어에 이미 깔려 있다"에 가장 가까운, 우리가 가진 유일한 1차 신호다.
type ObservedAppVersion struct {
	Version     string
	Runtime     string
	FirstSeenAt time.Time
}

// AppVersions는 관측된 빌드를 읽는 소비자 포트다.
//
// identity의 StoreRepository가 app_versions 컬렉션을 소유하므로 그쪽이 만족한다.
type AppVersions interface {
	ListObservedAppVersions(ctx context.Context, appID string, limit int) ([]ObservedAppVersion, error)
}

type cached struct {
	doc      Document
	loadedAt time.Time
}

// Service는 원격 설정을 읽고 쓴다.
type recommendCached struct {
	byPlatform map[string]string
	loadedAt   time.Time
}

type Service struct {
	store       *store.Client
	appVersions AppVersions
	ttl         time.Duration
	now         func() time.Time

	mu    sync.RWMutex
	cache map[string]cached

	recommendMu    sync.RWMutex
	recommendCache map[string]recommendCached
}

func NewService(s *store.Client) *Service {
	return &Service{
		store:          s,
		ttl:            DefaultTTL,
		now:            time.Now,
		cache:          map[string]cached{},
		recommendCache: map[string]recommendCached{},
	}
}

// WithTTL은 캐시 수명을 바꾼다.
func (s *Service) WithTTL(d time.Duration) *Service {
	s.ttl = d
	return s
}

// WithAppVersions는 관측 원장을 연결한다.
//
// 연결하지 않으면 자동 추종이 꺼지고 강제 업데이트 가드가 fail-closed로
// 거부한다. 안전 가드가 미조립 상태에서 열려 있으면 안 된다.
func (s *Service) WithAppVersions(v AppVersions) *Service {
	s.appVersions = v
	return s
}

func configPath(appID string) (fspath.Path, error) {
	return fspath.Parse(configsCollection + "/" + appID)
}

// Get은 앱 설정을 돌려준다. 캐시가 만료됐으면 다시 읽는다.
//
// 문서가 없으면 빈 설정을 돌려준다. 에러가 아니다.
// 설정을 아직 만들지 않은 앱도 SDK가 동작해야 한다.
func (s *Service) Get(ctx context.Context, appID string) (Document, error) {
	s.mu.RLock()
	c, ok := s.cache[appID]
	fresh := ok && s.now().Sub(c.loadedAt) < s.ttl
	s.mu.RUnlock()

	if fresh {
		return c.doc, nil
	}

	doc, err := s.load(ctx, appID)
	if err != nil {
		if ok {
			// 낡은 캐시로 계속 간다. 완전 실패보다 낫다.
			slog.WarnContext(ctx, "설정 갱신 실패. 캐시로 계속한다", "app_id", appID, "err", err)
			return c.doc, nil
		}
		return Document{}, err
	}
	return doc, nil
}

func (s *Service) load(ctx context.Context, appID string) (Document, error) {
	p, err := configPath(appID)
	if err != nil {
		return Document{}, platformerr.Wrap(err, platformerr.CodeInternal, "설정을 불러오지 못했어요")
	}

	doc := Document{AppID: appID}

	snap, err := s.store.Get(ctx, p)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// 설정이 없는 앱은 빈 문서로 취급한다.
		doc.SDK.Status = SDKStatusOK
	case err != nil:
		return Document{}, platformerr.Wrap(err, platformerr.CodeConfigUnavailable, "설정을 불러오지 못했어요")
	default:
		if err := snap.DataTo(&doc); err != nil {
			return Document{}, platformerr.Wrap(err, platformerr.CodeConfigUnavailable, "설정을 해석하지 못했어요")
		}
		doc.AppID = appID
	}

	s.mu.Lock()
	s.cache[appID] = cached{doc: doc, loadedAt: s.now()}
	s.mu.Unlock()

	return doc, nil
}

// Put은 설정을 저장한다. 백오피스 Admin API가 쓴다.
//
// 버전을 올려 ETag가 바뀌게 한다. 올리지 않으면 클라이언트가
// 304를 받아 새 값을 못 본다.
func (s *Service) Put(ctx context.Context, appID string, doc Document) error {
	p, err := configPath(appID)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "설정을 저장하지 못했어요")
	}

	doc.AppID = appID
	doc.Version = s.now().UnixMilli()
	doc.UpdatedAt = s.now()

	if err := s.store.Set(ctx, p, doc); err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "설정을 저장하지 못했어요")
	}

	s.invalidate(appID)
	return nil
}

// SetMaintenance는 점검 모드를 켜거나 끈다.
//
// BREAK-GLASS 절차가 부르는 경로다. 본문 텍스트를 받지 않고
// 시간만 받는다. 장애 중에 자유 텍스트 입력에 의존하면 안 된다.
func (s *Service) SetMaintenance(ctx context.Context, appID string, minutes int, actor string) error {
	doc, err := s.Get(ctx, appID)
	if err != nil {
		return err
	}

	if minutes <= 0 {
		doc.Maint = Maintenance{Active: false}
	} else {
		doc.Maint = Maintenance{
			Active:  true,
			Message: "지금 점검 중이에요. 잠시 후 다시 시도해 주세요",
			Until:   s.now().Add(time.Duration(minutes) * time.Minute),
		}
	}
	doc.UpdatedBy = actor

	return s.Put(ctx, appID, doc)
}

func (s *Service) invalidate(appID string) {
	s.mu.Lock()
	delete(s.cache, appID)
	s.mu.Unlock()
}

// GetUpdatePolicy는 저장된 업데이트 정책과 문서 버전을 돌려준다.
//
// 버전을 함께 주는 이유는 SetUpdatePolicy가 그 값으로 CAS를 하기 때문이다.
// 검증한 정책과 저장 시점의 정책이 다르면 가드를 통과하지 않은 값이 쓰인다.
func (s *Service) GetUpdatePolicy(ctx context.Context, appID string) (UpdatePolicy, int64, error) {
	doc, err := s.Get(ctx, appID)
	if err != nil {
		return UpdatePolicy{}, 0, err
	}
	return doc.Update, doc.Version, nil
}

// SetUpdatePolicy는 검증한 정책이 그대로 현재일 때만 저장한다.
//
// expectedVersion은 가드를 통과시킨 GetUpdatePolicy의 문서 버전이다.
// CAS가 없으면 동시 요청이 가드를 우회한다. A가 차단을 해제하는 동안 B가
// 그 버전을 아직 담고 있는 낡은 정책을 읽으면, B에게는 "새로 추가된 차단"이
// 없으므로 확인 문구도 관측 가드도 걸리지 않은 채 그 버전이 다시 막힌다.
//
// 이미 걸려 있던 버전의 차단 시각은 보존한다. 같은 값 재저장에 시계를
// 리셋하면 "언제부터 막혔나"가 사라지고, worker 재시도가 그대로 시각을
// 덮어써 감사가 거짓말을 하게 된다.
func (s *Service) SetUpdatePolicy(
	ctx context.Context,
	appID string,
	next UpdatePolicy,
	expectedVersion int64,
	actor string,
) error {
	p, err := configPath(appID)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "설정을 저장하지 못했어요")
	}

	err = s.store.RunTransaction(ctx, func(ctx context.Context, tx *store.Tx) error {
		doc := Document{AppID: appID}
		snap, err := tx.Get(p)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return err
		default:
			if err := snap.DataTo(&doc); err != nil {
				return platformerr.Wrap(err, platformerr.CodeConfigUnavailable, "설정을 해석하지 못했어요")
			}
		}
		if doc.Version != expectedVersion {
			return platformerr.New(platformerr.CodeUpdatePolicyConflict,
				"그 사이 정책이 바뀌었어요. 다시 읽고 시도해 주세요")
		}

		doc.AppID = appID
		doc.Update = mergeUpdatePolicy(doc.Update, next, s.now())
		doc.UpdatedBy = actor
		doc.Version = s.now().UnixMilli()
		doc.UpdatedAt = s.now()
		return tx.Set(p, doc)
	})
	if err != nil {
		// 트랜잭션 안에서 만든 판정 에러는 그대로 올린다. 감싸면 409가
		// 500이 되어 콘솔이 "다시 읽고 시도"를 안내할 수 없다.
		var pe *platformerr.Error
		if errors.As(err, &pe) {
			return err
		}
		return platformerr.Wrap(err, platformerr.CodeInternal, "설정을 저장하지 못했어요")
	}

	s.invalidate(appID)
	s.invalidateRecommend(appID)
	return nil
}

func mergeUpdatePolicy(current, next UpdatePolicy, now time.Time) UpdatePolicy {
	merged := UpdatePolicy{Platforms: map[string]PlatformUpdatePolicy{}}
	for platform, policy := range next.Platforms {
		existing := current.Platforms[platform]
		blocked := make([]BlockedVersion, 0, len(policy.BlockedVersions))
		for _, entry := range policy.BlockedVersions {
			entry.BlockedAt = now
			for _, previous := range existing.BlockedVersions {
				if SameVersion(previous.Version, entry.Version) && !previous.BlockedAt.IsZero() {
					entry.BlockedAt = previous.BlockedAt
					break
				}
			}
			blocked = append(blocked, entry)
		}
		policy.BlockedVersions = blocked
		merged.Platforms[platform] = policy
	}
	return merged
}

// ObservedVersions는 이 앱에서 관측된 빌드를 돌려준다.
//
// 관측 원장이 연결되지 않았으면 에러다. 강제 업데이트 가드가 이 목록에
// 의존하므로 조용히 빈 목록을 주면 가드가 열린 채로 동작한다.
func (s *Service) ObservedVersions(ctx context.Context, appID string) ([]ObservedAppVersion, error) {
	if s.appVersions == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"관측 원장이 준비되지 않았어요")
	}
	return s.appVersions.ListObservedAppVersions(ctx, appID, observedVersionLimit)
}

// AutoRecommendedVersions는 플랫폼별 자동 추종 값을 돌려준다.
func (s *Service) AutoRecommendedVersions(ctx context.Context, appID string) map[string]string {
	if s.appVersions == nil {
		return nil
	}

	s.recommendMu.RLock()
	c, ok := s.recommendCache[appID]
	fresh := ok && s.now().Sub(c.loadedAt) < recommendTTL
	s.recommendMu.RUnlock()
	if fresh {
		return c.byPlatform
	}

	observed, err := s.appVersions.ListObservedAppVersions(ctx, appID, observedVersionLimit)
	if err != nil {
		if ok {
			// 낡은 캐시로 계속 간다. 안내가 조금 늦는 것이 조회 실패로
			// 설정 응답 전체를 실패시키는 것보다 낫다.
			slog.WarnContext(ctx, "관측 버전 갱신 실패. 캐시로 계속한다", "app_id", appID, "err", err)
			return c.byPlatform
		}
		slog.WarnContext(ctx, "관측 버전을 읽지 못했다. 권장 안내를 건너뛴다", "app_id", appID, "err", err)
		return nil
	}

	byPlatform := HighestObservedVersions(observed, s.now())
	s.recommendMu.Lock()
	s.recommendCache[appID] = recommendCached{byPlatform: byPlatform, loadedAt: s.now()}
	s.recommendMu.Unlock()
	return byPlatform
}

func (s *Service) invalidateRecommend(appID string) {
	s.recommendMu.Lock()
	delete(s.recommendCache, appID)
	s.recommendMu.Unlock()
}

// HighestObservedVersions는 플랫폼별 자동 추종 값을 계산한다.
//
// 후보는 셋을 모두 만족해야 한다. 안정 SemVer이고, runtime이 그 플랫폼으로
// 해석되고, 처음 관측된 지 RecommendSoak이 지났다.
//
// ait과 web은 제외한다. 설치본이 없어 안내할 대상이 없다.
func HighestObservedVersions(observed []ObservedAppVersion, now time.Time) map[string]string {
	candidates := map[string][]string{}
	for _, entry := range observed {
		platform := PlatformFromRuntime(entry.Runtime)
		if platform != PlatformAndroid && platform != PlatformIOS {
			continue
		}
		if !ValidStableVersion(entry.Version) {
			continue
		}
		if entry.FirstSeenAt.IsZero() || now.Sub(entry.FirstSeenAt) < RecommendSoak {
			continue
		}
		candidates[platform] = append(candidates[platform], entry.Version)
	}

	out := make(map[string]string, len(candidates))
	for platform, versions := range candidates {
		if highest := HighestVersion(versions); highest != "" {
			out[platform] = highest
		}
	}
	return out
}

// ResolveFor는 타겟에 맞는 설정과 ETag를 돌려준다.
//
// identity가 세션 응답에 설정을 얹을 때도 이걸 쓴다.
// 부팅 시 왕복을 1회로 줄이는 게 목적이다.
//
// 레지스트리 항목을 받는다. 호출자가 이미 갖고 있어 다시 조회할 이유가 없고,
// 기능 플래그와 스토어 주소가 둘 다 여기 있다.
func (s *Service) ResolveFor(ctx context.Context, app registry.App, t Target) (Resolved, string, error) {
	doc, err := s.Get(ctx, app.AppID)
	if err != nil {
		return Resolved{}, "", err
	}

	// features.config가 앱별 롤아웃 스위치다. 꺼진 앱은 정책 판정을 통째로
	// 건너뛰어 예전과 동일하게 동작한다. 레지스트리에 선언만 돼 있고 아무도
	// 검사하지 않던 플래그를 여기서 되살린다.
	if !app.FeatureEnabled("config") {
		doc.Update = UpdatePolicy{}
	}

	platform := NormalizeClientPlatform(t.Platform)
	recommend := ""
	// override가 있으면 관측을 읽을 이유가 없다. 정책 항목 자체가 없으면
	// 판정도 없으므로 조회를 건너뛴다.
	if policy, ok := doc.Update.Platforms[platform]; ok && policy.RecommendOverride == "" {
		recommend = s.AutoRecommendedVersions(ctx, app.AppID)[platform]
	}

	resolved := doc.Resolve(t, recommend)
	// 수동으로 넣은 주소가 이긴다. 레지스트리는 기본값이다.
	if resolved.SDK.Status != SDKStatusOK && resolved.SDK.UpdateURL == "" {
		resolved.SDK.UpdateURL = app.UpdateURL(platform)
	}

	// 자동 추종 값도 소금이다. 소킹이 끝나 권장 기준이 올라가면 응답이
	// 달라지는데 문서 version은 그대로다. 넣지 않으면 If-None-Match로
	// 폴링하는 클라이언트가 영영 304를 받아 새 안내를 보지 못한다.
	etag := doc.ETag(t,
		app.RegistrySyncedAt.UTC().Format(time.RFC3339),
		resolved.SDK.RecommendedVersion,
	)
	return resolved, etag, nil
}

// Handler는 원격 설정 HTTP 핸들러다.
type Handler struct {
	svc      *Service
	registry *registry.Registry
}

func NewHandler(svc *Service, reg *registry.Registry) *Handler {
	return &Handler{svc: svc, registry: reg}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/config", httpx.Wrap(h.get))
}

// get은 설정을 돌려준다.
//
// 인증이 선택이다. anonymous 신원도 조회할 수 있다.
// kill switch가 여기 있어 인증 실패한 클라이언트도 점검 안내는 봐야 한다.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	appID, err := httpx.Header(r, "X-Seori-App", platformerr.CodeRequestInvalid)
	if err != nil {
		return err
	}

	// 앱이 정지 상태여도 설정은 준다. 클라이언트가 이유를 알아야 하기 때문이다.
	app, err := h.registry.Get(r.Context(), appID)
	if err != nil {
		return err
	}

	// 명시 쿼리가 이긴다. 이미 배포된 SDK가 보내는 값이고 스스로 이름 댄
	// 쪽이 더 구체적이다. 무효한 값에 400을 주지 않는다 -- /v1/config는
	// kill switch를 나르는 경로라, 이미 나간 클라이언트의 오타 때문에 점검
	// 안내조차 못 받으면 안 된다. 조용히 런타임 유도로 떨어뜨린다.
	platform := NormalizeClientPlatform(r.URL.Query().Get("platform"))
	if platform == "" {
		platform = PlatformFromRuntime(r.Header.Get("X-Seori-Runtime"))
	}

	// 버전도 같은 순서다. SDK는 쿼리와 헤더를 모두 보내지만 raw HTTP로 붙는
	// 앱은 전송 계층이 붙여 준 헤더만 갖고 있을 수 있다.
	appVersion := r.URL.Query().Get("appVersion")
	if appVersion == "" {
		appVersion = r.Header.Get("X-Seori-AppVer")
	}

	t := Target{
		Platform:   platform,
		AppVersion: appVersion,
		Locale:     r.URL.Query().Get("locale"),
	}

	resolved, etag, err := h.svc.ResolveFor(r.Context(), app, t)
	if err != nil {
		return err
	}

	// 앱이 정지 상태면 설정으로도 알린다.
	if app.Status == registry.StatusPaused && !resolved.Maint.Active {
		resolved.Maint = Maintenance{
			Active:  true,
			Message: "지금 점검 중이에요. 잠시 후 다시 시도해 주세요",
		}
	}

	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "max-age=60")

	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		// 본문 없이 304. 캐시 히트면 Firestore read가 0이다.
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	httpx.WriteOK(w, http.StatusOK, resolved)
	return nil
}
