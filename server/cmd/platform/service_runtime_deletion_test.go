//go:build integration

package main

import (
	"context"
	"fmt"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/ads"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/registry"
)

type runtimeDeletionProviders struct {
	now      *time.Time
	fail     bool
	erasures int
}

func (p *runtimeDeletionProviders) DeleteFirebaseIdentity(context.Context, registry.App, string) error {
	if p.fail {
		p.fail = false
		return fmt.Errorf("isolated provider temporarily unavailable")
	}
	p.erasures++
	return nil
}
func (p *runtimeDeletionProviders) SubmitAnalyticsDeletion(context.Context, registry.App, string) (time.Time, error) {
	return *p.now, nil
}
func (*runtimeDeletionProviders) DeleteAnalyticsCopies(context.Context, registry.App, string, string) (string, error) {
	return "fixture-deletion-job", nil
}

func TestServiceRuntimeDeletionRequestWorkerRetryAndReceipt(t *testing.T) {
	f := newRuntimeFixture(t)
	sess := f.session("delete-user")
	token := sess["platformToken"].(string)
	receipt := strings.Repeat("a", 64)
	body := map[string]string{"appId": f.app.AppID, "firebaseIdToken": f.signed("delete-user", "", false), "receiptToken": receipt}
	f.request("POST", "/v1/auth/account-deletions", "", body, 202)
	f.restart()
	f.request("POST", "/v1/auth/account-deletions", "", body, 202)
	f.request("POST", "/v1/auth/account-deletions/status", "", map[string]string{"appId": f.app.AppID, "receiptToken": receipt}, 200)
	f.request("POST", "/v1/auth/account-link-challenges", token, map[string]string{"provider": "google"}, 403)
	now := time.Now()
	queue := identity.NewStoreRepository(f.store).WithClock(func() time.Time { return now })
	providers := &runtimeDeletionProviders{now: &now, fail: true}
	w := &identity.DeletionWorker{Queue: queue, Registry: f.registry, Firebase: providers, Ads: ads.NewStoreRepository(f.store), Events: providers, Identity: queue}
	if err := w.RunOnce(t.Context()); err == nil {
		t.Fatal("provider failure must be recorded for retry")
	}
	// 저장된 재시도·보존기간을 가상 시계로 진행한다. 실시간 경과 증거가 아니다.
	f.restart()
	now = now.Add(6 * time.Minute)
	w.Registry = f.registry
	for range 3 {
		if err := w.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(4 * 24 * time.Hour)
	if err := w.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.restart()
	status := f.request("POST", "/v1/auth/account-deletions/status", "", map[string]string{"appId": f.app.AppID, "receiptToken": receipt}, 200)
	if status["state"] != "completed" || providers.erasures != 2 {
		t.Fatal("deletion must finish all phases and retain hashed receipt")
	}
	f.request("POST", "/v1/auth/session", "", map[string]any{"appId": f.app.AppID, "credential": map[string]string{"kind": "firebase-id-token", "value": f.signed("delete-user", "", false)}}, 403)
	t.Log("HTTP signed deletion/request replay → persisted receipt → provider failure/restart/retry → actual Firestore+ads cleanup worker → virtual 4-day final phase → receipt/recreation rejection; external deletion providers are fixtures")
}

func TestServiceRuntimeStorageFailureStaysInternal(t *testing.T) {
	f := newRuntimeFixture(t)
	f.store.Close()
	_, err := f.users.EnsureUser(t.Context(), f.app.AppID, identity.NewIdentity{UID: "storage-error-fixture"})
	if platformerr.CodeOf(err) != platformerr.CodeInternal || platformerr.StatusOf(err) != 500 {
		t.Fatal("storage error must stay internal")
	}
}
