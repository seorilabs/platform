package identity

import (
	"context"
	"errors"
	"time"

	"github.com/seorilabs/platform/server/internal/registry"
)

// 소비자인 identity가 단계를 정의하고 root에서 각 영역의 구현을 전달한다.
// Firebase 삭제 뒤에도 앱과 무관하게 서버 작업이 계속된다.
type DeletionQueue interface {
	ClaimDeletions(context.Context, int) ([]DeletionJob, error)
	AdvanceDeletion(context.Context, DeletionJob, bool) error
}
type AccountDataEraser interface {
	DeleteAccountData(context.Context, string, string) error
}
type FirebaseAccountEraser interface {
	DeleteFirebaseIdentity(context.Context, registry.App, string) error
}
type AnalyticsAccountEraser interface {
	SubmitAnalyticsDeletion(context.Context, registry.App, string) (time.Time, error)
	DeleteAnalyticsCopies(context.Context, registry.App, string, string) (string, error)
}
type DeletionIdentityEraser interface {
	DeleteIdentityData(context.Context, string, string, string) error
}

type DeletionWorker struct {
	Queue    DeletionQueue
	Registry *registry.Registry
	Firebase FirebaseAccountEraser
	Ads      AccountDataEraser
	Events   AnalyticsAccountEraser
	Identity DeletionIdentityEraser
}

func (w *DeletionWorker) RunOnce(ctx context.Context) error {
	if w.Queue == nil || w.Registry == nil || w.Firebase == nil || w.Ads == nil || w.Events == nil || w.Identity == nil {
		return errors.New("identity: deletion worker dependencies missing")
	}
	jobs, err := w.Queue.ClaimDeletions(ctx, 1)
	if err != nil {
		return err
	}
	var failures []error
	for _, j := range jobs {
		// lease보다 짧게 제한한다. 재시도 가능한 작업이므로 프로세스 종료도
		// 다음 Job 실행이 복구한다. 오류에는 토큰과 응답 본문을 담지 않는다.
		taskCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		err = w.step(taskCtx, &j)
		cancel()
		if saveErr := w.Queue.AdvanceDeletion(ctx, j, err == nil); saveErr != nil {
			failures = append(failures, saveErr)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (w *DeletionWorker) step(ctx context.Context, j *DeletionJob) error {
	app, err := w.Registry.Get(ctx, j.AppID)
	if err != nil {
		return err
	}
	if app.FirebaseProjectID != j.FirebaseProjectID || app.GA4.PropertyID != j.GA4PropertyID {
		return errors.New("identity: deletion app configuration unavailable")
	}
	switch j.Step {
	case 0:
		return w.Firebase.DeleteFirebaseIdentity(ctx, app, j.UID)
	case 1:
		if j.PlatformUserID != "" {
			return w.Ads.DeleteAccountData(ctx, j.AppID, j.PlatformUserID)
		}
		return nil
	case 2:
		// Google 접수와 BigQuery 사본 삭제를 분리한다. 사본의 user_id와
		// pseudo ID 연결은 지연 내보내기가 끝나는 최종 단계까지 보존한다.
		return w.submitAnalytics(ctx, app, j)
	case 3:
		// 접수 직전 custom token의 재생성 가능 시간이 지난 뒤 다시 지운다.
		if err := w.Firebase.DeleteFirebaseIdentity(ctx, app, j.UID); err != nil {
			return err
		}
		if err := w.submitAnalytics(ctx, app, j); err != nil {
			return err
		}
		if j.PlatformUserID != "" {
			ref, err := w.Events.DeleteAnalyticsCopies(ctx, app, j.PlatformUserID, j.AnalyticsJobRef)
			j.AnalyticsJobRef = ref
			if err != nil {
				return err
			}
		}
		return w.Identity.DeleteIdentityData(ctx, j.AppID, j.UID, j.PlatformUserID)
	default:
		return errors.New("identity: unknown deletion step")
	}
}

// Google 접수 시각을 단계별로 저장한다. BQ 준비가 실패해 job ID조차
// 없더라도 다음 실행이 같은 단계의 Google 요청을 반복하지 않는다.
func (w *DeletionWorker) submitAnalytics(ctx context.Context, app registry.App, j *DeletionJob) error {
	if j.PlatformUserID == "" {
		j.GoogleAnalyticsDeletion = "not_collected"
		return nil
	}
	if j.AnalyticsPhaseAcceptedAt != nil {
		return nil
	}
	at, err := w.Events.SubmitAnalyticsDeletion(ctx, app, j.PlatformUserID)
	if err != nil {
		return err
	}
	j.AnalyticsPhaseAcceptedAt = &at
	j.GoogleDeletionRequestedAt = &at
	j.GoogleAnalyticsDeletion = "accepted"
	return nil
}
