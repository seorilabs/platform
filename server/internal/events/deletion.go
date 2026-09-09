package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"cloud.google.com/go/bigquery"
	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/iterator"

	"github.com/seorilabs/platform/server/internal/registry"
)

var exportTableName = regexp.MustCompile(`^events_(intraday_)?[0-9]{8}$`)

// DeleteAnalyticsIdentity는 서버가 확인한 platform user ID만 대상으로 삼는다.
// 클라이언트가 제출한 appInstanceId, userId로 타인의 데이터를 지우지 않는다.
// 네이티브 수집은 Platform 세션 확인 후 동일 user ID를 설정하는 계약이다.
func (c *Collector) DeleteAnalyticsIdentity(ctx context.Context, app registry.App, puid string) (time.Time, error) {
	if puid == "" || !app.FeatureEnabled("account_deletion") {
		return time.Time{}, errors.New("events: deletion target required")
	}
	acceptedAt, err := submitAnalyticsDeletion(ctx, app, puid)
	if err != nil {
		return time.Time{}, err
	}
	// Google 사용자 삭제는 BigQuery 사본을 지워 주지 않는다. 각 영역이
	// 가진 사본을 따로 지우며 streaming buffer 오류는 워커가 재시도한다.
	params := []bigquery.QueryParameter{{Name: "app", Value: app.AppID}, {Name: "user", Value: puid}}
	for _, table := range []string{EventsTable, AuditTable} {
		sql := fmt.Sprintf("DELETE FROM `%s.%s.%s` WHERE app_id=@app AND platform_user_id=@user", c.client.Project(), c.dataset, table)
		if table == AuditTable {
			sql += " AND NOT STARTS_WITH(action, 'iap.')"
		} // IAP 감사 원장 불삭제.
		if err = c.runDeletionQuery(ctx, sql, params); err != nil {
			return time.Time{}, err
		}
	}
	dataset := c.client.DatasetInProject(app.FirebaseProjectID, "analytics_"+app.GA4.PropertyID)
	if _, err = dataset.Metadata(ctx); isGoogleNotFound(err) {
		return acceptedAt, nil
	} else if err != nil {
		return time.Time{}, err
	}
	tables := dataset.Tables(ctx)
	for {
		table, err := tables.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return time.Time{}, err
		}
		if !exportTableName.MatchString(table.TableID) {
			continue
		}
		sql := fmt.Sprintf("DELETE FROM `%s.%s.%s` WHERE user_id=@user", table.ProjectID, table.DatasetID, table.TableID)
		if err = c.runDeletionQuery(ctx, sql, params[1:]); err != nil && !isGoogleNotFound(err) {
			return time.Time{}, err
		}
	}
	return acceptedAt, nil
}
func isGoogleNotFound(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound
}
func (c *Collector) runDeletionQuery(ctx context.Context, sql string, params []bigquery.QueryParameter) error {
	q := c.client.Query(sql)
	q.Parameters = params
	q.MaxBytesBilled = 1 << 30
	job, err := q.Run(ctx)
	if err != nil {
		return err
	}
	status, err := job.Wait(ctx)
	if err != nil {
		return err
	}
	return status.Err()
}
func submitAnalyticsDeletion(ctx context.Context, app registry.App, puid string) (time.Time, error) {
	ts, err := impersonate.CredentialsTokenSource(ctx, impersonate.CredentialsConfig{TargetPrincipal: app.FirebaseCustomTokenServiceAccount, Scopes: []string{"https://www.googleapis.com/auth/analytics.edit"}, Lifetime: 5 * time.Minute})
	if err != nil {
		return time.Time{}, fmt.Errorf("events: deletion credentials unavailable: %w", err)
	}
	client := oauth2.NewClient(ctx, ts)
	client.Timeout = 30 * time.Second
	payload, _ := json.Marshal(map[string]string{"userId": puid})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://analyticsadmin.googleapis.com/v1alpha/properties/"+app.GA4.PropertyID+":submitUserDeletion", bytes.NewReader(payload))
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-user-project", app.FirebaseProjectID)
	response, err := client.Do(req)
	if err != nil {
		return time.Time{}, errors.New("events: Google deletion transport failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("events: Google deletion HTTP %d", response.StatusCode)
	}
	var result struct {
		DeletionRequestTime time.Time `json:"deletionRequestTime"`
	}
	if err = json.NewDecoder(http.MaxBytesReader(nil, response.Body, 65536)).Decode(&result); err != nil || result.DeletionRequestTime.IsZero() {
		return time.Time{}, errors.New("events: Google deletion receipt missing")
	}
	return result.DeletionRequestTime, nil
}
