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

type cached struct {
	doc      Document
	loadedAt time.Time
}

// Service는 원격 설정을 읽고 쓴다.
type Service struct {
	store *store.Client
	ttl   time.Duration
	now   func() time.Time

	mu    sync.RWMutex
	cache map[string]cached
}

func NewService(s *store.Client) *Service {
	return &Service{
		store: s,
		ttl:   DefaultTTL,
		now:   time.Now,
		cache: map[string]cached{},
	}
}

// WithTTL은 캐시 수명을 바꾼다.
func (s *Service) WithTTL(d time.Duration) *Service {
	s.ttl = d
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

// ResolveFor는 타겟에 맞는 설정과 ETag를 돌려준다.
//
// identity가 세션 응답에 설정을 얹을 때도 이걸 쓴다.
// 부팅 시 왕복을 1회로 줄이는 게 목적이다.
func (s *Service) ResolveFor(ctx context.Context, appID string, t Target) (Resolved, string, error) {
	doc, err := s.Get(ctx, appID)
	if err != nil {
		return Resolved{}, "", err
	}
	return doc.Resolve(t), doc.ETag(t), nil
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

	t := Target{
		Platform:   r.URL.Query().Get("platform"),
		AppVersion: r.URL.Query().Get("appVersion"),
		Locale:     r.URL.Query().Get("locale"),
	}

	resolved, etag, err := h.svc.ResolveFor(r.Context(), app.AppID, t)
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
