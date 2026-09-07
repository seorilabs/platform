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
	deep       DeepAccess
	deepErr    error
}

func (a *serviceAccess) DeepAccess(
	_ context.Context, _ registry.App, _ string, _ int,
) (DeepAccess, error) {
	return a.deep, a.deepErr
}

func (a *flowServiceAccess) DeepAccess(
	_ context.Context, _ registry.App, _ string, _ int,
) (DeepAccess, error) {
	return DeepAccess{}, nil
}

type flowServiceAccess struct {
	authorized map[string]bool
	checked    []string
	unlocked   []string
}

func (a *flowServiceAccess) Authorized(
	_ context.Context, _ registry.App, _, _, deepKey string, _ int,
) (bool, error) {
	a.checked = append(a.checked, deepKey)
	return a.authorized[deepKey], nil
}

func (a *flowServiceAccess) Unlock(
	_ context.Context, _ registry.App, _, _, deepKey string, _ UnlockRequest,
) error {
	a.unlocked = append(a.unlocked, deepKey)
	a.authorized[deepKey] = true
	return nil
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

func TestResolveUnlocksAnnualAndMonthlyFlowTogether(t *testing.T) {
	req := validResolveRequest()
	req.Unlock = &UnlockRequest{Section: "seun", Kind: "ticket"}
	access := &flowServiceAccess{authorized: map[string]bool{}}

	result, err := newTestService(t, req, serviceUsage{}, access).
		Resolve(t.Context(), "ungeul", "puid", req)
	if err != nil {
		t.Fatal(err)
	}
	if len(access.unlocked) != 1 || access.unlocked[0] != "flow:2026" {
		t.Fatalf("unlock keys=%v, want [flow:2026]", access.unlocked)
	}
	for _, key := range access.checked {
		if key != "flow:2026" {
			t.Fatalf("authorization key=%q, want flow:2026", key)
		}
	}
	if len(result.Locked) != 0 {
		t.Fatalf("한 번 해금한 흐름이 다시 잠겼다: %+v", result.Locked)
	}
	deep := 0
	for _, article := range result.Articles {
		if article.Access == AccessDeep {
			deep++
		}
	}
	if deep == 0 {
		t.Fatal("해금 후 세운·월운 심화 본문이 없다")
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

func TestResolveAndTermPassThroughMore(t *testing.T) {
	req := validResolveRequest()
	release := serviceRelease(t, req)
	selection, err := Select(req)
	if err != nil {
		t.Fatal(err)
	}
	id := selection.BaseIDs[0]
	item := release.Items[id]
	item.More = "원문 본문"
	item.Contexts = []Context{ContextReading, ContextTerm}
	release.Items[id] = item
	service, err := NewService(fakeApps{testContentApp()}, fakeReleases{release}, serviceUsage{}, &serviceAccess{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Resolve(t.Context(), "ungeul", "puid", req)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, article := range result.Articles {
		if article.ID == id {
			found = true
			if article.More != "원문 본문" {
				t.Fatalf("리딩 응답이 more를 안 실었다: %+v", article)
			}
		} else if article.More != "" {
			t.Fatalf("more가 없는 항목에 more가 붙었다: %+v", article)
		}
	}
	if !found {
		t.Fatalf("기본 항목 %s 가 응답에 없다", id)
	}
	term, err := service.Term(t.Context(), "ungeul", "puid", id)
	if err != nil {
		t.Fatal(err)
	}
	if term.Article.More != "원문 본문" {
		t.Fatalf("사전 응답이 more를 안 실었다: %+v", term.Article)
	}
}

// pairingServiceAccess는 권한 조회·해제가 어느 열람 단위(readingKey/deepKey, 연도)로
// 들어왔는지 기록한다. 궁합은 pairKey와 고정 deepKey "gunghap", 연도 0 이어야 한다.
type pairingServiceAccess struct {
	authorized map[string]bool
	checked    []string
	years      []int
	unlocked   []string
}

func (a *pairingServiceAccess) Authorized(
	_ context.Context, _ registry.App, _, readingKey, deepKey string, year int,
) (bool, error) {
	a.checked = append(a.checked, readingKey+"/"+deepKey)
	a.years = append(a.years, year)
	return a.authorized[readingKey+"/"+deepKey], nil
}

func (a *pairingServiceAccess) Unlock(
	_ context.Context, _ registry.App, _, readingKey, deepKey string, _ UnlockRequest,
) error {
	a.unlocked = append(a.unlocked, readingKey+"/"+deepKey)
	a.authorized[readingKey+"/"+deepKey] = true
	return nil
}

func (a *pairingServiceAccess) DeepAccess(
	_ context.Context, _ registry.App, _ string, _ int,
) (DeepAccess, error) {
	return DeepAccess{}, nil
}

func pairingApp() registry.App {
	app := testContentApp()
	app.Content.PairingEnabled = true
	return app
}

func pairingRelease(t *testing.T, req ResolvePairingRequest) Release {
	t.Helper()
	selection, err := SelectPairing(req)
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]Item{}
	for _, id := range selection.DeepIDs {
		items[id] = Item{ID: id, Text: "궁합 해설", Access: AccessDeep, Contexts: []Context{ContextReading}}
	}
	return Release{
		SchemaVersion:  SupportedSchemaVersion,
		ContentVersion: "sha256-" + strings.Repeat("b", 64),
		Items:          items,
	}
}

func newPairingService(
	t *testing.T, app registry.App, req ResolvePairingRequest, usage Usage, access AccessController,
) *Service {
	t.Helper()
	service, err := NewService(fakeApps{app}, fakeReleases{pairingRelease(t, req)}, usage, access)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestResolvePairingLocksWithoutAccess(t *testing.T) {
	req := validPairingRequest()
	result, err := newPairingService(t, pairingApp(), req, serviceUsage{}, &serviceAccess{}).
		ResolvePairing(t.Context(), "ungeul", "puid", req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Locked) != 1 || result.Locked[0] != (LockedPairing{DeepKey: "gunghap", Section: "gunghap"}) {
		t.Fatalf("locked=%+v", result.Locked)
	}
	if len(result.Articles) != 0 {
		t.Fatalf("잠긴 궁합 본문이 반환됐다: %+v", result.Articles)
	}
	if !strings.HasPrefix(result.PairKey, "pk_") {
		t.Fatalf("pairKey=%q", result.PairKey)
	}
}

func TestResolvePairingTicketUnlockRecordsPairKeyAndGunghap(t *testing.T) {
	req := validPairingRequest()
	req.Unlock = &UnlockRequest{Section: "gunghap", Kind: "ticket"}
	access := &pairingServiceAccess{authorized: map[string]bool{}}
	selection, err := SelectPairing(req)
	if err != nil {
		t.Fatal(err)
	}

	result, err := newPairingService(t, pairingApp(), req, serviceUsage{}, access).
		ResolvePairing(t.Context(), "ungeul", "puid", req)
	if err != nil {
		t.Fatal(err)
	}
	wantUnit := selection.PairKey + "/gunghap"
	if len(access.unlocked) != 1 || access.unlocked[0] != wantUnit {
		t.Fatalf("unlock units=%v, want [%s]", access.unlocked, wantUnit)
	}
	for i, unit := range access.checked {
		if unit != wantUnit || access.years[i] != 0 {
			t.Fatalf("authorization unit=%q year=%d, want %s year=0", unit, access.years[i], wantUnit)
		}
	}
	if len(result.Locked) != 0 {
		t.Fatalf("해금한 궁합이 다시 잠겼다: %+v", result.Locked)
	}
	if len(result.Articles) != len(selection.DeepIDs) {
		t.Fatalf("articles=%d, want %d", len(result.Articles), len(selection.DeepIDs))
	}
	for _, article := range result.Articles {
		if article.Access != AccessDeep {
			t.Fatalf("궁합에 무료 본문이 섞였다: %+v", article)
		}
	}
}

func TestResolvePairingDoesNotConsumeUnlockWhenAlreadyAuthorized(t *testing.T) {
	req := validPairingRequest()
	req.Unlock = &UnlockRequest{Section: "gunghap", Kind: "ticket"}
	access := &serviceAccess{authorized: true}
	if _, err := newPairingService(t, pairingApp(), req, serviceUsage{}, access).
		ResolvePairing(t.Context(), "ungeul", "puid", req); err != nil {
		t.Fatal(err)
	}
	if access.unlockCall != 0 {
		t.Fatalf("이미 열린 궁합에 권한을 %d회 차감했다", access.unlockCall)
	}
}

func TestResolvePairingRejectsAppWithoutPairingFlag(t *testing.T) {
	req := validPairingRequest()
	_, err := newPairingService(t, testContentApp(), req, serviceUsage{}, &serviceAccess{authorized: true}).
		ResolvePairing(t.Context(), "ungeul", "puid", req)
	if platformerr.CodeOf(err) != platformerr.CodeContentNotEnabled {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestResolvePairingPropagatesDailyLimit(t *testing.T) {
	req := validPairingRequest()
	limit := platformerr.New(platformerr.CodeRateLimited, "limit")
	_, err := newPairingService(t, pairingApp(), req, serviceUsage{readingErr: limit}, &serviceAccess{}).
		ResolvePairing(t.Context(), "ungeul", "puid", req)
	if platformerr.CodeOf(err) != platformerr.CodeRateLimited {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestResolvePairingRequiresReleaseCoordinates(t *testing.T) {
	req := validPairingRequest()
	release := pairingRelease(t, req)
	delete(release.Items, "gung-ilji.samhap")
	service, err := NewService(fakeApps{pairingApp()}, fakeReleases{release}, serviceUsage{}, &serviceAccess{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ResolvePairing(t.Context(), "ungeul", "puid", req)
	if platformerr.CodeOf(err) != platformerr.CodeContentUnavailable {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}
