package content

import (
	"context"
	"fmt"
	"sort"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type Apps interface {
	GetUsable(context.Context, string) (registry.App, error)
}

type Releases interface {
	Load(context.Context, registry.App) (Release, error)
}

type Usage interface {
	AllowReading(context.Context, registry.App, string, string) error
	AllowTerm(context.Context, registry.App, string) error
}

type AccessController interface {
	Authorized(context.Context, registry.App, string, string, string, int) (bool, error)
	Unlock(context.Context, registry.App, string, string, string, UnlockRequest) error
}

type Service struct {
	apps     Apps
	releases Releases
	usage    Usage
	access   AccessController
}

func NewService(apps Apps, releases Releases, usage Usage, access AccessController) (*Service, error) {
	if apps == nil || releases == nil || usage == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"콘텐츠 서비스 설정이 올바르지 않아요")
	}
	return &Service{apps: apps, releases: releases, usage: usage, access: access}, nil
}

func (s *Service) Version(ctx context.Context, appID string) (ContentVersion, error) {
	_, release, err := s.release(ctx, appID)
	if err != nil {
		return ContentVersion{}, err
	}
	return ContentVersion{SchemaVersion: release.SchemaVersion, ContentVersion: release.ContentVersion}, nil
}

func (s *Service) Resolve(
	ctx context.Context,
	appID, puid string,
	req ResolveRequest,
) (ResolveResult, error) {
	selection, err := Select(req)
	if err != nil {
		return ResolveResult{}, err
	}
	app, release, err := s.release(ctx, appID)
	if err != nil {
		return ResolveResult{}, err
	}
	if release.SchemaVersion != req.SchemaVersion {
		return ResolveResult{}, platformerr.New(platformerr.CodeContentSchemaMismatch,
			"앱과 콘텐츠 스키마 버전이 일치하지 않아요")
	}
	if err := validateSelectionRelease(release, selection); err != nil {
		return ResolveResult{}, err
	}
	if err := s.usage.AllowReading(ctx, app, puid, selection.ReadingKey); err != nil {
		return ResolveResult{}, err
	}

	if req.Unlock != nil {
		if s.access == nil {
			return ResolveResult{}, platformerr.New(platformerr.CodeContentLocked,
				"심화 권한 확인이 준비되지 않았어요")
		}
		deepKey := flowDeepKey(req.Reading.Seun.Year)
		alreadyAuthorized, err := s.access.Authorized(
			ctx, app, puid, selection.ReadingKey, deepKey, req.Reading.Seun.Year,
		)
		if err != nil {
			return ResolveResult{}, platformerr.Wrap(err, platformerr.CodeContentUnavailable,
				"심화 권한을 확인하지 못했어요")
		}
		if !alreadyAuthorized {
			err = s.access.Unlock(ctx, app, puid, selection.ReadingKey, deepKey, *req.Unlock)
		}
		if err != nil {
			return ResolveResult{}, err
		}
	}

	articles := make(map[string]Article)
	if selection.Scope["base"] {
		if err := collectArticles(release, selection.BaseIDs, AccessFree, articles); err != nil {
			return ResolveResult{}, err
		}
		if err := collectOptionalArticles(release, selection.OptionalBaseIDs, AccessFree, articles); err != nil {
			return ResolveResult{}, err
		}
	}
	locked := make([]LockedDeep, 0, 2)
	for _, section := range []string{"seun", "wolun"} {
		if !selection.Scope[section] {
			continue
		}
		deepKey := flowDeepKey(req.Reading.Seun.Year)
		allowed := false
		if s.access != nil {
			allowed, err = s.access.Authorized(
				ctx, app, puid, selection.ReadingKey, deepKey, req.Reading.Seun.Year,
			)
			if err != nil {
				return ResolveResult{}, platformerr.Wrap(err, platformerr.CodeContentUnavailable,
					"심화 권한을 확인하지 못했어요")
			}
		}
		if !allowed {
			locked = append(locked, LockedDeep{DeepKey: deepKey, Section: section, Year: req.Reading.Seun.Year})
			continue
		}
		if err := collectArticles(release, selection.DeepIDs[section], AccessDeep, articles); err != nil {
			return ResolveResult{}, err
		}
	}

	articleList := make([]Article, 0, len(articles))
	for _, article := range articles {
		articleList = append(articleList, article)
	}
	sort.Slice(articleList, func(i, j int) bool { return articleList[i].ID < articleList[j].ID })
	return ResolveResult{
		SchemaVersion: release.SchemaVersion, ContentVersion: release.ContentVersion,
		ReadingKey: selection.ReadingKey, Articles: articleList, Locked: locked,
	}, nil
}

// flowDeepKey는 한 해의 세운과 12개월 월운을 같은 열람 단위로 묶는다.
// 한 번의 광고 보상 또는 열람권 차감으로 둘을 함께 열기 위한 키다.
func flowDeepKey(year int) string {
	return fmt.Sprintf("flow:%d", year)
}

func validateSelectionRelease(release Release, selection Selection) error {
	if err := validateItems(release, selection.BaseIDs, AccessFree); err != nil {
		return err
	}
	if err := validateOptionalItems(release, selection.OptionalBaseIDs, AccessFree); err != nil {
		return err
	}
	for _, section := range []string{"seun", "wolun"} {
		if err := validateItems(release, selection.DeepIDs[section], AccessDeep); err != nil {
			return err
		}
	}
	return nil
}

func validateOptionalItems(release Release, ids []string, want Access) error {
	for _, id := range ids {
		item, ok := release.Items[id]
		if ok && (!item.HasContext(ContextReading) || item.Access != want) {
			return platformerr.Newf(platformerr.CodeContentUnavailable,
				"활성 콘텐츠 릴리스의 optional selector 좌표 분류가 다릅니다: %s", id)
		}
	}
	return nil
}

func validateItems(release Release, ids []string, want Access) error {
	for _, id := range ids {
		item, ok := release.Items[id]
		if !ok || !item.HasContext(ContextReading) || item.Access != want {
			return platformerr.Newf(platformerr.CodeContentUnavailable,
				"활성 콘텐츠 릴리스에 selector 좌표가 없거나 분류가 다릅니다: %s", id)
		}
	}
	return nil
}

func (s *Service) Term(ctx context.Context, appID, puid, termID string) (TermResult, error) {
	if !contentIDPattern.MatchString(termID) {
		return TermResult{}, platformerr.New(platformerr.CodeRequestInvalid,
			"사전 항목 ID가 올바르지 않아요")
	}
	app, release, err := s.release(ctx, appID)
	if err != nil {
		return TermResult{}, err
	}
	if err := s.usage.AllowTerm(ctx, app, puid); err != nil {
		return TermResult{}, err
	}
	item, ok := release.Items[termID]
	if !ok || !item.HasContext(ContextTerm) || item.Access != AccessFree {
		return TermResult{}, platformerr.New(platformerr.CodeContentTermNotFound,
			"사전 항목을 찾을 수 없어요")
	}
	return TermResult{
		SchemaVersion: release.SchemaVersion, ContentVersion: release.ContentVersion,
		Article: Article{ID: item.ID, Text: item.Text, Access: item.Access},
	}, nil
}

func (s *Service) release(ctx context.Context, appID string) (registry.App, Release, error) {
	app, err := s.apps.GetUsable(ctx, appID)
	if err != nil {
		return registry.App{}, Release{}, err
	}
	if !app.FeatureEnabled("content") {
		return registry.App{}, Release{}, platformerr.New(platformerr.CodeContentNotEnabled,
			"이 앱은 콘텐츠 API를 사용하지 않아요")
	}
	release, err := s.releases.Load(ctx, app)
	if err != nil {
		return registry.App{}, Release{}, err
	}
	return app, release, nil
}

func collectArticles(release Release, ids []string, want Access, into map[string]Article) error {
	if err := validateItems(release, ids, want); err != nil {
		return err
	}
	for _, id := range ids {
		item := release.Items[id]
		into[id] = Article{ID: item.ID, Text: item.Text, Access: item.Access}
	}
	return nil
}

func collectOptionalArticles(release Release, ids []string, want Access, into map[string]Article) error {
	if err := validateOptionalItems(release, ids, want); err != nil {
		return err
	}
	for _, id := range ids {
		if item, ok := release.Items[id]; ok {
			into[id] = Article{ID: item.ID, Text: item.Text, Access: item.Access}
		}
	}
	return nil
}
