package registry

import (
	"context"
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

type staticRegistrySource struct{ apps []App }

func (s staticRegistrySource) LoadApps(context.Context) ([]App, error) { return s.apps, nil }

func TestResolveGooglePlayPackageUsesRegistryBinding(t *testing.T) {
	app := validAppForTest()
	r := New(staticRegistrySource{apps: []App{app}})
	appID, err := r.ResolveGooglePlayPackage(t.Context(), app.IAP.GooglePlayPackageName)
	if err != nil {
		t.Fatal(err)
	}
	if appID != app.AppID {
		t.Fatalf("appId=%q want=%q", appID, app.AppID)
	}
	if _, err := r.ResolveGooglePlayPackage(t.Context(), "com.unknown.app"); platformerr.CodeOf(err) != platformerr.CodeConfigUnavailable {
		t.Fatalf("unknown code=%s", platformerr.CodeOf(err))
	}
}

func TestRegistryRejectsDuplicateGooglePlayPackageBindings(t *testing.T) {
	first := validAppForTest()
	second := validAppForTest()
	second.AppID = "second-app"
	second.FirebaseProjectID = "second-app"
	r := New(staticRegistrySource{apps: []App{first, second}})
	if _, err := r.List(t.Context()); platformerr.CodeOf(err) != platformerr.CodeConfigUnavailable {
		t.Fatalf("duplicate package load code=%s err=%v", platformerr.CodeOf(err), err)
	}
}
