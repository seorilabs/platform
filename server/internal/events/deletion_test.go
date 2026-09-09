package events

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/option"

	"github.com/seorilabs/platform/server/internal/registry"
)

func TestDeletionSQLKeepsIAPAndScopesEveryCopy(t *testing.T) {
	sql := deletionSQL("platform-project", "platform", []string{"game.analytics_123.events_20260909"})
	for _, part := range []string{"app_id=@app AND platform_user_id=@user", "NOT STARTS_WITH(action, 'iap.')", "WHERE user_id=@user"} {
		if !strings.Contains(sql, part) {
			t.Fatal("missing scope", part)
		}
	}
	if strings.Count(sql, "DELETE FROM") != 3 {
		t.Fatal("not all data copies are covered")
	}
}

func TestDeletionResumesBigQueryWithoutGoogleSubmission(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/queries/saved-job") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jobComplete":true,"jobReference":{"projectId":"platform-test","jobId":"saved-job","location":"asia-northeast3"}}`))
			return
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/jobs/saved-job") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobReference":{"projectId":"platform-test","jobId":"saved-job","location":"asia-northeast3"},"status":{"state":"DONE"},"configuration":{"query":{"query":"SELECT 1"}}}`))
	}))
	defer server.Close()
	client, err := bigquery.NewClient(context.Background(), "platform-test", option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	c := &Collector{client: client}
	// No service account is configured: reaching Google submission would fail.
	app := registry.App{Features: map[string]bool{"account_deletion": true}}
	at, ref, err := c.DeleteAnalyticsIdentity(context.Background(), app, "pu_qa", "asia-northeast3/saved-job")
	if err != nil || ref != "asia-northeast3/saved-job" || !at.IsZero() || requests == 0 {
		t.Fatalf("saved job was not resumed: at=%v ref=%q requests=%d err=%v", at, ref, requests, err)
	}
}
