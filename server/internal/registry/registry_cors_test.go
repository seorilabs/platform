package registry

import (
	"context"
	"testing"
)

func TestAllowsCORSOriginUsesExplicitRegistryAllowlist(t *testing.T) {
	app := validAppForTest()
	app.CORSOrigins = []string{"https://test-app.private-apps.tossmini.com"}
	r := New(staticRegistrySource{apps: []App{app}})

	allowed, err := r.AllowsCORSOrigin(context.Background(), app.CORSOrigins[0])
	if err != nil {
		t.Fatalf("AllowsCORSOrigin() error = %v", err)
	}
	if !allowed {
		t.Fatal("등록된 origin이 거부됐다")
	}

	allowed, err = r.AllowsCORSOrigin(context.Background(), "https://attacker.example")
	if err != nil {
		t.Fatalf("AllowsCORSOrigin() error = %v", err)
	}
	if allowed {
		t.Fatal("등록되지 않은 origin이 허용됐다")
	}
}
