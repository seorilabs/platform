package content

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type fakeApps struct{ app registry.App }

func (f fakeApps) GetUsable(context.Context, string) (registry.App, error) { return f.app, nil }

type fakeReleases struct{ release Release }

func (f fakeReleases) Load(context.Context, registry.App) (Release, error) { return f.release, nil }

type fakeUsage struct{}

func (fakeUsage) AllowReading(context.Context, registry.App, string, string) error { return nil }
func (fakeUsage) AllowTerm(context.Context, registry.App, string) error            { return nil }

type fakeSessions struct {
	session identity.Session
	err     error
}

func (f fakeSessions) Authenticate(*http.Request) (identity.Session, error) { return f.session, f.err }

type fakeAppChecks struct {
	err   error
	token string
}

func (f *fakeAppChecks) VerifyAppCheck(_ context.Context, _ string, token string) error {
	f.token = token
	return f.err
}

func testHandler(t *testing.T, appChecks *fakeAppChecks) *http.ServeMux {
	t.Helper()
	req := validResolveRequest()
	selection, err := Select(req)
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]Item{}
	for _, id := range selection.BaseIDs {
		items[id] = Item{ID: id, Text: "무료 해설", Access: AccessFree, Contexts: []Context{ContextReading}}
	}
	for _, ids := range selection.DeepIDs {
		for _, id := range ids {
			items[id] = Item{ID: id, Text: "심화 해설", Access: AccessDeep, Contexts: []Context{ContextReading}}
		}
	}
	app := testContentApp()
	service, err := NewService(fakeApps{app}, fakeReleases{Release{
		SchemaVersion: 1, ContentVersion: "sha256-" + strings.Repeat("a", 64), Items: items,
	}}, fakeUsage{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, fakeSessions{session: identity.Session{
		AppID: app.AppID, PlatformUserID: "puid", AppUserID: "firebase-user",
	}}, appChecks)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux
}

func TestHandlerRequiresAppCheck(t *testing.T) {
	checks := &fakeAppChecks{err: platformerr.New(platformerr.CodeAppCheckRequired, "required")}
	r := httptest.NewRequest(http.MethodGet, "/v1/content/version", nil)
	w := httptest.NewRecorder()
	testHandler(t, checks).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || checks.token != "" {
		t.Fatalf("status=%d token=%q body=%s", w.Code, checks.token, w.Body.String())
	}
}

func TestHandlerRequiresPlatformSession(t *testing.T) {
	app := testContentApp()
	service, err := NewService(fakeApps{app}, fakeReleases{}, fakeUsage{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, fakeSessions{
		err: platformerr.New(platformerr.CodeAuthRequired, "required"),
	}, &fakeAppChecks{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/content/version", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHandlerRejectsArbitraryArticleIDs(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/content/readings:resolve",
		strings.NewReader(`{"schemaVersion":1,"reading":{},"scope":["base"],"articleIds":["ilju.gapja"]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(appCheckHeader, "attested")
	w := httptest.NewRecorder()
	testHandler(t, &fakeAppChecks{}).ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "articleIds") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestNewHandlerRejectsMissingBoundary(t *testing.T) {
	_, err := NewHandler(nil, fakeSessions{}, &fakeAppChecks{})
	if platformerr.CodeOf(err) != platformerr.CodeRuntimeConfigInvalid {
		t.Fatalf("err=%v", err)
	}
}
