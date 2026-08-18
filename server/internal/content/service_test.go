package content

import (
	"context"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type serviceUsage struct {
	readingErr error
	termErr    error
}

func (u serviceUsage) AllowReading(context.Context, registry.App, string, string) error {
	return u.readingErr
}
func (u serviceUsage) AllowTerm(context.Context, registry.App, string) error { return u.termErr }

type serviceAccess struct {
	authorized bool
	unlockCall int
}

func (a *serviceAccess) Authorized(
	context.Context, registry.App, string, string, string, int,
) (bool, error) {
	return a.authorized, nil
}
func (a *serviceAccess) Unlock(
	context.Context, registry.App, string, string, string, UnlockRequest,
) error {
	a.unlockCall++
	a.authorized = true
	return nil
}

func serviceRelease(t *testing.T, req ResolveRequest) Release {
	t.Helper()
	selection, err := Select(req)
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]Item{}
	for _, id := range selection.BaseIDs {
		items[id] = Item{ID: id, Text: "무료 해설", Access: AccessFree, Contexts: []Context{ContextReading}}
	}
	for _, id := range selection.OptionalBaseIDs {
		items[id] = Item{ID: id, Text: "선택 해설", Access: AccessFree, Contexts: []Context{ContextReading}}
	}
	for _, ids := range selection.DeepIDs {
		for _, id := range ids {
			items[id] = Item{ID: id, Text: "심화 해설", Access: AccessDeep, Contexts: []Context{ContextReading}}
		}
	}
	return Release{
		SchemaVersion:  SupportedSchemaVersion,
		ContentVersion: "sha256-" + strings.Repeat("a", 64),
		Items:          items,
	}
}

func newTestService(t *testing.T, req ResolveRequest, usage Usage, access AccessController) *Service {
	t.Helper()
	service, err := NewService(
		fakeApps{testContentApp()}, fakeReleases{serviceRelease(t, req)}, usage, access,
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestResolveOmitsLockedDeepArticles(t *testing.T) {
	req := validResolveRequest()
	result, err := newTestService(t, req, serviceUsage{}, &serviceAccess{}).
		Resolve(t.Context(), "ungeul", "puid", req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Locked) != 2 {
		t.Fatalf("locked=%+v", result.Locked)
	}
	for _, article := range result.Articles {
		if article.Access == AccessDeep {
			t.Fatalf("잠긴 심화 본문이 반환됐다: %s", article.ID)
		}
	}
}

func TestResolveReturnsAuthorizedDeepArticles(t *testing.T) {
	req := validResolveRequest()
	result, err := newTestService(t, req, serviceUsage{}, &serviceAccess{authorized: true}).
		Resolve(t.Context(), "ungeul", "puid", req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Locked) != 0 {
		t.Fatalf("locked=%+v", result.Locked)
	}
	deep := 0
	for _, article := range result.Articles {
		if article.Access == AccessDeep {
			deep++
		}
	}
	if deep == 0 {
		t.Fatal("권한이 있는데 심화 본문이 없다")
	}
}

func TestResolveDoesNotConsumeUnlockWhenAlreadyAuthorized(t *testing.T) {
	req := validResolveRequest()
	req.Unlock = &UnlockRequest{Section: "seun", Kind: "ticket"}
	access := &serviceAccess{authorized: true}
	_, err := newTestService(t, req, serviceUsage{}, access).
		Resolve(t.Context(), "ungeul", "puid", req)
	if err != nil {
		t.Fatal(err)
	}
	if access.unlockCall != 0 {
		t.Fatalf("이미 열린 항목에 권한을 %d회 차감했다", access.unlockCall)
	}
}

func TestResolvePropagatesDailyLimit(t *testing.T) {
	req := validResolveRequest()
	limit := platformerr.New(platformerr.CodeRateLimited, "limit")
	_, err := newTestService(t, req, serviceUsage{readingErr: limit}, &serviceAccess{}).
		Resolve(t.Context(), "ungeul", "puid", req)
	if platformerr.CodeOf(err) != platformerr.CodeRateLimited {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}
