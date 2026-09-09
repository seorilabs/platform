package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/registry"
)

type deletionMemory struct {
	app          registry.App
	uid, receipt string
	status       DeletionStatus
	blocked      bool
	err          error
}

func (m *deletionMemory) BeginDeletion(_ context.Context, app registry.App, uid, receipt string) (DeletionStatus, error) {
	m.app = app
	m.uid = uid
	m.receipt = receipt
	return m.status, m.err
}
func (m *deletionMemory) DeletionStatus(_ context.Context, app, receipt string) (DeletionStatus, error) {
	if app != m.app.AppID || receipt != m.receipt {
		return DeletionStatus{}, deletionDenied()
	}
	return m.status, m.err
}
func (m *deletionMemory) AccountDeleting(context.Context, string, string) (bool, error) {
	return m.blocked, m.err
}
func deletionTestApp() registry.App {
	app := testApp()
	app.Features = map[string]bool{"firebase_custom_token_bridge": true, "account_deletion": true}
	app.FirebaseCustomTokenServiceAccount = "platform-auth@" + app.FirebaseProjectID + ".iam.gserviceaccount.com"
	app.GA4.PropertyID = "123"
	return app
}
func deletionService(t *testing.T, verifier TokenVerifier, repo *deletionMemory) *Service {
	t.Helper()
	s := newTestService(t, verifier, newMemRepo())
	s.registry = registry.New(fakeSource{apps: []registry.App{deletionTestApp()}})
	return s.WithAccountDeletions(repo)
}
func TestDeletionUsesVerifiedIdentityAndOriginalReceipt(t *testing.T) {
	repo := &deletionMemory{status: DeletionStatus{State: "processing", RequestedAt: time.Now(), GoogleAnalyticsDeletion: "not_requested"}}
	s := deletionService(t, fakeVerifier{}, repo)
	token := strings.Repeat("a", 64)
	if _, err := s.RequestAccountDeletion(context.Background(), deletionTestApp().AppID, "verified-uid", token); err != nil {
		t.Fatal(err)
	}
	if repo.uid != "verified-uid" || repo.app.FirebaseProjectID != deletionTestApp().FirebaseProjectID || repo.receipt != token {
		t.Fatal("삭제 대상이 인증 결과와 다르다")
	}
	repo.blocked = true // 접수 이후에도 동일 접수 재시도와 상태 확인은 허용한다.
	if _, err := s.RequestAccountDeletion(context.Background(), repo.app.AppID, "verified-uid", token); err != nil {
		t.Fatal(err)
	}
	for _, other := range []struct{ app, receipt string }{{"other", token}, {repo.app.AppID, strings.Repeat("b", 64)}} {
		if _, err := s.AccountDeletionStatus(context.Background(), other.app, other.receipt); err == nil {
			t.Fatal("타 앱 또는 다른 접수증으로 상태를 읽었다")
		}
	}
	if err := s.ensureNotBlocked(context.Background(), repo.app.AppID, "verified-uid"); err == nil {
		t.Fatal("삭제 중 인증이 허용됐다")
	}
}
func TestDeletionRejectsInvalidProofBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		token, receipt string
		verifyErr      error
	}{{"", strings.Repeat("a", 64), nil}, {"uid", "guessable", nil}, {"uid", strings.Repeat("a", 64), errors.New("wrong audience")}} {
		repo := &deletionMemory{}
		s := deletionService(t, fakeVerifier{err: tc.verifyErr}, repo)
		if _, err := s.RequestAccountDeletion(context.Background(), deletionTestApp().AppID, tc.token, tc.receipt); err == nil {
			t.Fatal("유효하지 않은 삭제 요청을 수락했다")
		}
		if repo.uid != "" {
			t.Fatal("검증 실패 후 원장이 변경됐다")
		}
	}
}
func TestDeletionFailsClosedWhenStateUnavailable(t *testing.T) {
	repo := &deletionMemory{err: errors.New("store unavailable")}
	s := deletionService(t, fakeVerifier{}, repo)
	if err := s.ensureNotBlocked(context.Background(), deletionTestApp().AppID, "uid"); err == nil {
		t.Fatal("삭제 상태 조회 오류가 인증을 통과했다")
	}
}

type deletionSteps struct {
	calls []string
	fail  string
}

func (m *deletionSteps) call(s string) error {
	m.calls = append(m.calls, s)
	if m.fail == s {
		return errors.New("dependency unavailable")
	}
	return nil
}
func (m *deletionSteps) DeleteFirebaseIdentity(context.Context, registry.App, string) error {
	return m.call("firebase")
}
func (m *deletionSteps) DeleteAccountData(context.Context, string, string) error {
	return m.call("ads")
}
func (m *deletionSteps) DeleteAnalyticsIdentity(context.Context, registry.App, string) (time.Time, error) {
	return time.Now(), m.call("analytics")
}
func (m *deletionSteps) DeleteIdentityData(context.Context, string, string, string) error {
	return m.call("identity")
}
func TestDeletionFinalStepRequiresAnalyticsCleanup(t *testing.T) {
	app := deletionTestApp()
	steps := &deletionSteps{fail: "analytics"}
	w := DeletionWorker{Registry: registry.New(fakeSource{apps: []registry.App{app}}), Firebase: steps, Ads: steps, Events: steps, Identity: steps}
	j := DeletionJob{AppID: app.AppID, UID: "uid", PlatformUserID: "pu_test", FirebaseProjectID: app.FirebaseProjectID, GA4PropertyID: app.GA4.PropertyID, ServiceAccount: app.FirebaseCustomTokenServiceAccount, Step: 3}
	if err := w.step(context.Background(), &j); err == nil {
		t.Fatal("분석 삭제 실패 후 완료했다")
	}
	if strings.Join(steps.calls, ",") != "analytics" {
		t.Fatal("분석 정리 실패 중 복구에 필요한 identity를 지웠다")
	}
	steps.fail = ""
	steps.calls = nil
	if err := w.step(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	if strings.Join(steps.calls, ",") != "analytics,identity" {
		t.Fatal("최종 삭제 순서가 다르다")
	}
	j.FirebaseProjectID = "other-project"
	steps.calls = nil
	if err := w.step(context.Background(), &j); err == nil || len(steps.calls) != 0 {
		t.Fatal("접수 이후 바뀐 다른 프로젝트를 삭제했다")
	}
}
