package registry

import (
	"context"
	"testing"
)

func TestAllowsCORSOriginUsesExplicitRegistryAllowlist(t *testing.T) {
	app := validAppForTest()
	app.CORSOrigins = []string{
		"https://test-app.private-apps.tossmini.com",
		"capacitor://localhost",
	}
	r := New(staticRegistrySource{apps: []App{app}})

	for _, origin := range app.CORSOrigins {
		allowed, err := r.AllowsCORSOrigin(context.Background(), origin)
		if err != nil {
			t.Fatalf("AllowsCORSOrigin(%q) error = %v", origin, err)
		}
		if !allowed {
			t.Fatalf("등록된 origin이 거부됐다: %s", origin)
		}
	}

	allowed, err := r.AllowsCORSOrigin(context.Background(), "https://attacker.example")
	if err != nil {
		t.Fatalf("AllowsCORSOrigin() error = %v", err)
	}
	if allowed {
		t.Fatal("등록되지 않은 origin이 허용됐다")
	}
}
