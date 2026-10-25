package identity

import (
	"context"
	"errors"
	"github.com/seorilabs/platform/server/internal/registry"
	"io"
	"net/http"
	"strings"
	"testing"
)

type deletionTransport func(*http.Request) (*http.Response, error)

func (f deletionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStarterDeletionBlocksOldTokensBeforeRemovingProgressAndAuth(t *testing.T) {
	for _, tc := range []struct {
		name      string
		app       string
		statuses  []int
		wantCalls int
		wantErr   bool
	}{
		{"complete", "starter-game", []int{200, 200, 200}, 3, false},
		{"already absent", "starter-game", []int{200, 404, 400}, 3, false},
		{"marker failure stops", "starter-game", []int{403}, 1, true},
		{"progress failure stops", "starter-game", []int{200, 503}, 2, true},
		{"other apps unchanged", "other-game", []int{200}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			d := FirebaseAccountDeleter{clientFor: func(context.Context, registry.App) (*http.Client, error) {
				return &http.Client{Transport: deletionTransport(func(r *http.Request) (*http.Response, error) {
					paths = append(paths, r.Method+" "+r.URL.Path)
					status := tc.statuses[len(paths)-1]
					body := "{}"
					if status == 400 {
						body = `{"error":{"message":"USER_NOT_FOUND"}}`
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})}, nil
			}}
			err := d.DeleteFirebaseIdentity(context.Background(), registry.App{AppID: tc.app, FirebaseProjectID: "starter-test"}, "uid-test")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v", err)
			}
			if len(paths) != tc.wantCalls {
				t.Fatalf("calls=%v", paths)
			}
			if tc.app == "starter-game" {
				if !strings.Contains(paths[0], "PATCH /v1/projects/starter-test/databases/(default)/documents/deleted_accounts/uid-test") {
					t.Fatalf("marker not first: %v", paths)
				}
				if len(paths) > 1 && !strings.Contains(paths[1], "DELETE /v1/projects/starter-test/databases/(default)/documents/game_saves/uid-test") {
					t.Fatalf("progress not second: %v", paths)
				}
			}
			if len(paths) == 3 && !strings.Contains(paths[2], "accounts:delete") {
				t.Fatalf("auth not last: %v", paths)
			}
		})
	}
}

func TestDeletionCredentialFailureDoesNotCallFirebase(t *testing.T) {
	sentinel := errors.New("unavailable")
	d := FirebaseAccountDeleter{clientFor: func(context.Context, registry.App) (*http.Client, error) { return nil, sentinel }}
	if err := d.DeleteFirebaseIdentity(context.Background(), registry.App{AppID: "starter-game", FirebaseProjectID: "starter-test"}, "uid"); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
}
