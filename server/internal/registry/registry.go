package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

// Source는 레지스트리 항목을 공급한다.
//
// 인터페이스를 여기에 두는 이유는 Registry가 소비자이기 때문이다.
// 구현은 파일과 Firestore 두 가지이며 둘 다 이 패키지를 import하지 않아도 된다.
type Source interface {
	LoadApps(ctx context.Context) ([]App, error)
}

// DefaultTTL은 캐시 수명이다.
//
// 레지스트리는 거의 바뀌지 않지만 kill switch(status: paused)가 여기 있어
// 반영이 너무 늦으면 안 된다. 60초면 사고 대응에 충분하다.
const DefaultTTL = 60 * time.Second

// Registry는 앱 레지스트리 캐시다.
type Registry struct {
	source Source
	ttl    time.Duration
	now    func() time.Time

	mu       sync.RWMutex
	apps     map[string]App
	loadedAt time.Time
}

// Option은 Registry 설정이다.
type Option func(*Registry)

// WithTTL은 캐시 수명을 바꾼다.
func WithTTL(d time.Duration) Option {
	return func(r *Registry) { r.ttl = d }
}

// WithClock은 시계를 주입한다. 테스트에서 만료를 제어하려고 둔다.
func WithClock(now func() time.Time) Option {
	return func(r *Registry) { r.now = now }
}

// New는 레지스트리를 만든다. 첫 조회 시점에 로드한다.
func New(source Source, opts ...Option) *Registry {
	r := &Registry{
		source: source,
		ttl:    DefaultTTL,
		now:    time.Now,
		apps:   map[string]App{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Get은 앱을 돌려준다. 캐시가 만료됐으면 다시 로드한다.
//
// 로드에 실패해도 캐시가 있으면 그걸 쓴다. 레지스트리 저장소가 잠시
// 불안정하다고 전체 서비스를 멈출 이유가 없다. 다만 로그는 남긴다.
func (r *Registry) Get(ctx context.Context, appID string) (App, error) {
	if err := r.ensureFresh(ctx); err != nil {
		return App{}, err
	}

	r.mu.RLock()
	app, ok := r.apps[appID]
	r.mu.RUnlock()

	if !ok {
		// 등록되지 않은 앱을 404가 아니라 403으로 돌려준다.
		// 어떤 app_id가 존재하는지 알려주지 않기 위해서다.
		return App{}, platformerr.New(platformerr.CodeAppUnknown, "등록되지 않은 앱이에요")
	}
	return app, nil
}

// GetUsable은 앱을 돌려주되 정지 상태면 에러다.
// 대부분의 핸들러가 이걸 쓴다.
func (r *Registry) GetUsable(ctx context.Context, appID string) (App, error) {
	app, err := r.Get(ctx, appID)
	if err != nil {
		return App{}, err
	}
	if err := app.EnsureUsable(); err != nil {
		return App{}, err
	}
	return app, nil
}

// List는 모든 앱을 app_id 순으로 돌려준다.
func (r *Registry) List(ctx context.Context) ([]App, error) {
	if err := r.ensureFresh(ctx); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]App, 0, len(r.apps))
	for _, a := range r.apps {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })
	return out, nil
}

// ResolveGooglePlayPackage는 RTDN packageName을 레지스트리 appId에 묶는다.
// 등록되지 않은 package를 임의 앱으로 보내면 다른 앱의 환불 검토 원장을
// 오염시키므로 일시적 설정 오류로 fail-closed한다. ADR 0014.
func (r *Registry) ResolveGooglePlayPackage(ctx context.Context, packageName string) (string, error) {
	if err := r.ensureFresh(ctx); err != nil {
		return "", err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, app := range r.apps {
		if app.FeatureEnabled("iap") && app.MarketEnabled("google_play") &&
			app.IAP.GooglePlayPackageName == packageName {
			if err := app.EnsureUsable(); err != nil {
				return "", err
			}
			return app.AppID, nil
		}
	}
	return "", platformerr.New(platformerr.CodeConfigUnavailable,
		"Google Play package에 연결된 앱 설정이 없어요")
}

// Reload는 캐시를 강제로 다시 채운다.
func (r *Registry) Reload(ctx context.Context) error {
	return r.load(ctx)
}

func (r *Registry) ensureFresh(ctx context.Context) error {
	r.mu.RLock()
	fresh := len(r.apps) > 0 && r.now().Sub(r.loadedAt) < r.ttl
	hasCache := len(r.apps) > 0
	r.mu.RUnlock()

	if fresh {
		return nil
	}

	if err := r.load(ctx); err != nil {
		if hasCache {
			// 낡은 캐시로 계속 간다. 완전 실패보다 낫다.
			slog.WarnContext(ctx, "레지스트리 갱신 실패. 캐시로 계속한다", "err", err)
			return nil
		}
		return err
	}
	return nil
}

func (r *Registry) load(ctx context.Context) error {
	apps, err := r.source.LoadApps(ctx)
	if err != nil {
		return platformerr.Wrap(err, platformerr.CodeConfigUnavailable, "앱 설정을 불러오지 못했어요")
	}

	// package name 중복은 어느 appId로 귀속해도 위험하다. 두 항목 모두
	// 제외해 요청 시 config_unavailable로 드러나게 한다.
	packageCounts := make(map[string]int)
	for _, a := range apps {
		if a.Validate() == nil && a.FeatureEnabled("iap") && a.MarketEnabled("google_play") {
			packageCounts[a.IAP.GooglePlayPackageName]++
		}
	}

	next := make(map[string]App, len(apps))
	for _, a := range apps {
		if err := a.Validate(); err != nil {
			// 항목 하나가 잘못돼도 나머지는 살린다.
			// 전체를 막으면 무관한 앱까지 중단된다.
			slog.ErrorContext(ctx, "레지스트리 항목이 올바르지 않다. 건너뛴다",
				"app_id", a.AppID, "err", err)
			continue
		}
		if a.FeatureEnabled("iap") && a.MarketEnabled("google_play") &&
			packageCounts[a.IAP.GooglePlayPackageName] != 1 {
			slog.ErrorContext(ctx, "Google Play package name이 중복됐다. 앱을 건너뛴다",
				"app_id", a.AppID)
			continue
		}
		next[a.AppID] = a
	}

	if len(next) == 0 {
		return platformerr.New(platformerr.CodeConfigUnavailable, "사용 가능한 앱 설정이 없어요")
	}

	r.mu.Lock()
	r.apps = next
	r.loadedAt = r.now()
	r.mu.Unlock()

	slog.InfoContext(ctx, "레지스트리 로드", "count", len(next))
	return nil
}

// FSSource는 파일 시스템에서 레지스트리를 읽는다.
//
// repo의 registry/apps/*.json이 source of truth다.
// CI 검증과 테스트, regsync의 입력, 그리고 Firestore가 비었을 때의
// 부트스트랩에 쓴다.
type FSSource struct {
	FS  fs.FS
	Dir string
}

// NewFSSource는 디렉토리 기반 소스를 만든다.
func NewFSSource(fsys fs.FS, dir string) *FSSource {
	return &FSSource{FS: fsys, Dir: dir}
}

func (s *FSSource) LoadApps(context.Context) ([]App, error) {
	entries, err := fs.ReadDir(s.FS, s.Dir)
	if err != nil {
		return nil, fmt.Errorf("registry: %s 읽기 실패: %w", s.Dir, err)
	}

	var apps []App
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".json" {
			continue
		}
		full := path.Join(s.Dir, e.Name())

		b, err := fs.ReadFile(s.FS, full)
		if err != nil {
			return nil, fmt.Errorf("registry: %s 읽기 실패: %w", full, err)
		}

		var a App
		dec := json.NewDecoder(bytes.NewReader(b))
		// 레지스트리 파일에 오타가 있으면 조용히 무시되는 대신 실패해야 한다.
		// 예를 들어 "featurs"로 잘못 쓰면 모든 기능이 꺼진 채 배포된다.
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			return nil, fmt.Errorf("registry: %s 파싱 실패: %w", full, err)
		}
		apps = append(apps, a)
	}
	return apps, nil
}

// StoreSource는 Firestore에서 레지스트리를 읽는다. 런타임 기본 경로다.
//
// git의 registry/apps/*.json이 source of truth이고 regsync가 여기로 upsert한다.
// 런타임이 파일을 읽지 않는 이유는 컨테이너에 repo가 없기 때문이다.
type StoreSource struct {
	store *store.Client
}

// NewStoreSource는 Firestore 기반 소스를 만든다.
func NewStoreSource(s *store.Client) *StoreSource {
	return &StoreSource{store: s}
}

// AppsPath는 레지스트리 컬렉션 경로다.
const AppsPath = "apps"

func (s *StoreSource) LoadApps(ctx context.Context) ([]App, error) {
	p, err := fspath.Parse(AppsPath)
	if err != nil {
		return nil, fmt.Errorf("registry: 경로 파싱 실패: %w", err)
	}

	it, err := s.store.Query(ctx, p, nil)
	if err != nil {
		return nil, fmt.Errorf("registry: 조회 실패: %w", err)
	}
	defer it.Stop()

	var apps []App
	for {
		snap, err := it.Next()
		if store.IsDone(err) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("registry: 순회 실패: %w", err)
		}

		var a App
		if err := snap.DataTo(&a); err != nil {
			// 항목 하나가 깨져도 나머지는 살린다.
			slog.ErrorContext(ctx, "레지스트리 문서 변환 실패. 건너뛴다",
				"doc", snap.Ref.ID, "err", err)
			continue
		}
		if a.AppID == "" {
			a.AppID = snap.Ref.ID
		}
		apps = append(apps, a)
	}
	return apps, nil
}

// Upsert는 앱 항목을 Firestore에 쓴다. cmd/regsync가 쓴다.
func (s *StoreSource) Upsert(ctx context.Context, a App) error {
	if err := a.Validate(); err != nil {
		return err
	}
	p, err := fspath.Parse(AppsPath + "/" + a.AppID)
	if err != nil {
		return fmt.Errorf("registry: 경로 파싱 실패: %w", err)
	}
	a.RegistrySyncedAt = time.Now().UTC()
	return s.store.Set(ctx, p, a)
}
