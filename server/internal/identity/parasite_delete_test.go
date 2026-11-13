package identity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/registry"
)

func TestParasiteDeletionRemovesEveryPageBeforeAuth(t *testing.T) {
	const base = "projects/parasite-test/databases/(default)/documents/"
	var calls []string
	lists := 0
	d := FirebaseAccountDeleter{clientFor: func(context.Context, registry.App) (*http.Client, error) {
		return &http.Client{Transport: deletionTransport(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.Method+" "+r.URL.Path)
			body := "{}"
			if r.Method == http.MethodGet {
				if r.URL.Query().Get("mask.fieldPaths") != "deletionMetadata" {
					t.Fatal("deletion list must omit full save payloads")
				}
				lists++
				if lists < 3 {
					body = `{"documents":[{"name":"` + base + `parasite_users/uid/blobs/` + strings.Repeat(string(rune('a'+lists)), 64) + `"}]}`
				}
			}
			if strings.HasSuffix(r.URL.Path, ":commit") {
				var payload struct {
					Writes []struct {
						Delete string `json:"delete"`
					} `json:"writes"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Writes) != 1 || !strings.HasPrefix(payload.Writes[0].Delete, base+"parasite_users/uid/blobs/") {
					t.Fatalf("unscoped delete: %+v, %v", payload, err)
				}
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}, nil
	}}
	if err := d.DeleteFirebaseIdentity(context.Background(), registry.App{AppID: "my-little-parasite", FirebaseProjectID: "parasite-test"}, "uid"); err != nil {
		t.Fatal(err)
	}
	if lists != 3 {
		t.Fatalf("pages not drained: %v", calls)
	}
	if !strings.Contains(calls[0], "PATCH /v1/"+base+"deleted_accounts/uid") {
		t.Fatalf("missing first tombstone: %v", calls)
	}
	if !strings.Contains(calls[len(calls)-2], "/state/head") || !strings.HasSuffix(calls[len(calls)-1], "/accounts:delete") {
		t.Fatalf("wrong cleanup ordering: %v", calls)
	}
}

func TestParasiteDeletionStopsBeforeAuthOnCloudFailure(t *testing.T) {
	for _, scenario := range []string{"marker", "list", "foreign-page", "commit", "head"} {
		t.Run(scenario, func(t *testing.T) {
			authCalled := false
			d := FirebaseAccountDeleter{clientFor: func(context.Context, registry.App) (*http.Client, error) {
				return &http.Client{Transport: deletionTransport(func(r *http.Request) (*http.Response, error) {
					status, body := 200, "{}"
					if strings.HasSuffix(r.URL.Path, "accounts:delete") {
						authCalled = true
					}
					if r.Method == http.MethodPatch && scenario == "marker" {
						status = 503
					}
					if r.Method == http.MethodGet {
						if scenario == "list" {
							status = 503
						}
						if scenario == "foreign-page" {
							body = `{"documents":[{"name":"projects/other/databases/(default)/documents/secret/page"}]}`
						}
						if scenario == "commit" {
							body = `{"documents":[{"name":"projects/parasite-test/databases/(default)/documents/parasite_users/uid/blobs/` + strings.Repeat("a", 64) + `"}]}`
						}
					}
					if strings.HasSuffix(r.URL.Path, ":commit") && scenario == "commit" {
						status = 503
					}
					if r.Method == http.MethodDelete && scenario == "head" {
						status = 503
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})}, nil
			}}
			if err := d.DeleteFirebaseIdentity(context.Background(), registry.App{AppID: "my-little-parasite", FirebaseProjectID: "parasite-test"}, "uid"); err == nil || authCalled {
				t.Fatalf("error=%v authCalled=%v", err, authCalled)
			}
		})
	}
}
