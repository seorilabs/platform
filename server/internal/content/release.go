package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

const DefaultReleaseTTL = 5 * time.Minute

var contentIDPattern = regexp.MustCompile(`^[a-z0-9가-힣][a-z0-9가-힣._-]{0,127}$`)
var versionPattern = regexp.MustCompile(`^sha256-[a-f0-9]{64}$`)

type ObjectSource interface {
	Read(context.Context, string, string) ([]byte, error)
}

type activePointer struct {
	SchemaVersion  int    `json:"schemaVersion"`
	ContentVersion string `json:"contentVersion"`
}

type releaseManifest struct {
	SchemaVersion  int    `json:"schemaVersion"`
	ContentVersion string `json:"contentVersion"`
	ContentSHA256  string `json:"contentSha256"`
	ItemCount      int    `json:"itemCount"`
}

type contentFile struct {
	SchemaVersion int    `json:"schemaVersion"`
	Items         []Item `json:"items"`
}

type cachedRelease struct {
	release   Release
	checkedAt time.Time
}

type ReleaseLoader struct {
	source      ObjectSource
	environment string
	ttl         time.Duration
	now         func() time.Time

	mu    sync.Mutex
	cache map[string]cachedRelease
	locks map[string]*sync.Mutex
}

func NewReleaseLoader(source ObjectSource, environment string) (*ReleaseLoader, error) {
	if source == nil || (environment != "staging" && environment != "production") {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"콘텐츠 릴리스 설정이 올바르지 않아요")
	}
	return &ReleaseLoader{
		source: source, environment: environment, ttl: DefaultReleaseTTL,
		now: time.Now, cache: make(map[string]cachedRelease), locks: make(map[string]*sync.Mutex),
	}, nil
}

func (l *ReleaseLoader) WithClock(now func() time.Time) *ReleaseLoader {
	l.now = now
	return l
}

func (l *ReleaseLoader) WithTTL(ttl time.Duration) *ReleaseLoader {
	if ttl > 0 {
		l.ttl = ttl
	}
	return l
}

func (l *ReleaseLoader) Load(ctx context.Context, app registry.App) (Release, error) {
	if !app.FeatureEnabled("content") {
		return Release{}, platformerr.New(platformerr.CodeContentNotEnabled,
			"이 앱은 콘텐츠 API를 사용하지 않아요")
	}

	// 같은 앱의 만료 캐시는 한 번만 다시 읽되, 한 앱의 GCS 지연이 다른 앱의
	// 콘텐츠 요청까지 막지 않도록 네트워크 I/O는 전역 mutex 밖에서 수행한다.
	appLock := l.lockForApp(app.AppID)
	appLock.Lock()
	defer appLock.Unlock()

	now := l.now().UTC()
	l.mu.Lock()
	previous, hasPrevious := l.cache[app.AppID]
	if hasPrevious && now.Sub(previous.checkedAt) < l.ttl {
		l.mu.Unlock()
		return previous.release, nil
	}
	l.mu.Unlock()

	next, err := l.read(ctx, app, now)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		l.cache[app.AppID] = cachedRelease{release: next, checkedAt: now}
		return next, nil
	}
	if hasPrevious {
		// 손상된 active 전환이 요청마다 GCS를 두드리지 않도록 확인 시각만 갱신한다.
		previous.checkedAt = now
		l.cache[app.AppID] = previous
		slog.ErrorContext(ctx, "콘텐츠 활성 릴리스를 거부하고 직전 정상본을 유지한다",
			"app_id", app.AppID, "content_version", previous.release.ContentVersion, "err", err)
		return previous.release, nil
	}
	return Release{}, platformerr.Wrap(err, platformerr.CodeContentUnavailable,
		"사용할 수 있는 콘텐츠 릴리스가 없어요")
}

func (l *ReleaseLoader) lockForApp(appID string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock := l.locks[appID]
	if lock == nil {
		lock = &sync.Mutex{}
		l.locks[appID] = lock
	}
	return lock
}

func (l *ReleaseLoader) read(ctx context.Context, app registry.App, now time.Time) (Release, error) {
	base := l.environment + "/" + app.Content.Prefix
	activeBytes, err := l.source.Read(ctx, app.Content.Bucket, base+"/active.json")
	if err != nil {
		return Release{}, fmt.Errorf("active.json 읽기 실패: %w", err)
	}
	var active activePointer
	if err := decodeStrict(activeBytes, &active); err != nil {
		return Release{}, fmt.Errorf("active.json 검증 실패: %w", err)
	}
	if active.SchemaVersion != SupportedSchemaVersion || !versionPattern.MatchString(active.ContentVersion) {
		return Release{}, fmt.Errorf("active.json 버전이 올바르지 않다")
	}

	releaseBase := base + "/releases/" + active.ContentVersion
	manifestBytes, err := l.source.Read(ctx, app.Content.Bucket, releaseBase+"/manifest.json")
	if err != nil {
		return Release{}, fmt.Errorf("manifest.json 읽기 실패: %w", err)
	}
	var manifest releaseManifest
	if err := decodeStrict(manifestBytes, &manifest); err != nil {
		return Release{}, fmt.Errorf("manifest.json 검증 실패: %w", err)
	}
	if manifest.SchemaVersion != active.SchemaVersion || manifest.ContentVersion != active.ContentVersion ||
		manifest.ItemCount <= 0 || len(manifest.ContentSHA256) != 64 ||
		"sha256-"+manifest.ContentSHA256 != active.ContentVersion {
		return Release{}, fmt.Errorf("manifest와 active가 일치하지 않는다")
	}

	contentBytes, err := l.source.Read(ctx, app.Content.Bucket, releaseBase+"/content.json")
	if err != nil {
		return Release{}, fmt.Errorf("content.json 읽기 실패: %w", err)
	}
	sum := sha256.Sum256(contentBytes)
	gotSHA := hex.EncodeToString(sum[:])
	if gotSHA != manifest.ContentSHA256 {
		return Release{}, fmt.Errorf("content.json SHA-256이 manifest와 다르다")
	}
	var file contentFile
	if err := decodeStrict(contentBytes, &file); err != nil {
		return Release{}, fmt.Errorf("content.json 검증 실패: %w", err)
	}
	if file.SchemaVersion != manifest.SchemaVersion || len(file.Items) != manifest.ItemCount {
		return Release{}, fmt.Errorf("content.json 스키마 또는 항목 수가 manifest와 다르다")
	}

	items := make(map[string]Item, len(file.Items))
	for _, item := range file.Items {
		if err := validateItem(item); err != nil {
			return Release{}, err
		}
		if _, exists := items[item.ID]; exists {
			return Release{}, fmt.Errorf("content item id가 중복됐다: %s", item.ID)
		}
		items[item.ID] = item
	}
	return Release{
		SchemaVersion: file.SchemaVersion, ContentVersion: active.ContentVersion,
		Items: items, LoadedAt: now,
	}, nil
}

func validateItem(item Item) error {
	if !contentIDPattern.MatchString(item.ID) || strings.TrimSpace(item.Text) == "" || len(item.Text) > 16*1024 {
		return fmt.Errorf("content item이 올바르지 않다: %s", item.ID)
	}
	if item.Access != AccessFree && item.Access != AccessDeep {
		return fmt.Errorf("content item access가 올바르지 않다: %s", item.ID)
	}
	if len(item.Contexts) == 0 || len(item.Contexts) > 2 {
		return fmt.Errorf("content item context가 올바르지 않다: %s", item.ID)
	}
	seen := map[Context]bool{}
	for _, c := range item.Contexts {
		if (c != ContextReading && c != ContextTerm && c != ContextInternal) || seen[c] {
			return fmt.Errorf("content item context가 올바르지 않다: %s", item.ID)
		}
		seen[c] = true
	}
	if seen[ContextTerm] && item.Access != AccessFree {
		return fmt.Errorf("사전 item은 free여야 한다: %s", item.ID)
	}
	return nil
}

func decodeStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON 뒤에 값이 남아 있다")
	}
	return nil
}
