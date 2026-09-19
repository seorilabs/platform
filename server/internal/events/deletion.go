package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/iterator"

	"github.com/seorilabs/platform/server/internal/registry"
)

var exportTableName = regexp.MustCompile(`^events_(intraday_)?[0-9]{8}$`)

// 검증된 PUID와 GA4 내보내기의 실제 연결만 삭제 근거로 사용한다.
// HTTP context.ga4ClientId는 클라이언트 입력이므로 삭제 권한을 부여하지 않는다.
func (c *Collector) DeleteAnalyticsCopies(ctx context.Context, app registry.App, puid, jobRef string) (string, error) {
	if puid == "" || app.FirebaseProjectID == "" || app.GA4.PropertyID == "" {
		return jobRef, errors.New("events: deletion target required")
	}
	if jobRef != "" {
		location, id, ok := strings.Cut(jobRef, "/")
		if !ok || location == "" || id == "" {
			return jobRef, errors.New("events: deletion job reference invalid")
		}
		job, err := c.client.JobFromIDLocation(ctx, id, location)
		if err == nil {
			ref, err := waitDeletionJob(ctx, job, jobRef)
			return ref, err
		}
		if !isGoogleNotFound(err) {
			return jobRef, err
		}
	}
	// 여러 일자 삭제를 하나의 서버 작업으로 제출한다. 워커 시간 제한이
	// 지나도 job reference를 원장에 보존해 다음 실행이 같은 작업을 조회한다.
	// 고정 1 GiB 상한으로 정상적인 개인정보 삭제가 영구 정지하지 않게 한다.
	var tables []string
	dataset := c.client.DatasetInProject(app.FirebaseProjectID, "analytics_"+app.GA4.PropertyID)
	if _, err := dataset.Metadata(ctx); err != nil && !isGoogleNotFound(err) {
		return "", err
	} else if err == nil {
		iter := dataset.Tables(ctx)
		for {
			table, err := iter.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				return "", err
			}
			if exportTableName.MatchString(table.TableID) {
				tables = append(tables, fmt.Sprintf("%s.%s.%s", table.ProjectID, table.DatasetID, table.TableID))
			}
		}
	}
	metadata, err := c.client.Dataset(c.dataset).Metadata(ctx)
	if err != nil {
		return "", err
	}
	q := c.client.Query(deletionSQL(c.client.Project(), c.dataset, tables))
	q.Parameters = []bigquery.QueryParameter{{Name: "app", Value: app.AppID}, {Name: "user", Value: puid}}
	q.JobIDConfig = bigquery.JobIDConfig{JobID: "account_deletion_" + uuid.NewString(), Location: metadata.Location}
	jobRef = q.Location + "/" + q.JobID
	job, err := q.Run(ctx)
	if err != nil {
		return jobRef, err
	}
	ref, err := waitDeletionJob(ctx, job, jobRef)
	return ref, err
}
func deletionSQL(project, dataset string, gaTables []string) string {
	statements := []string{"DECLARE linked_ids ARRAY<STRUCT<stream_id STRING, pseudo_id STRING>>", "BEGIN TRANSACTION"}
	if len(gaTables) > 0 {
		var sources []string
		for _, table := range gaTables {
			sources = append(sources, fmt.Sprintf("SELECT stream_id, user_pseudo_id AS pseudo_id FROM `%s` WHERE user_id=@user AND user_pseudo_id IS NOT NULL AND user_pseudo_id!=''", table))
		}
		statements = append(statements, "SET linked_ids = ARRAY(SELECT AS STRUCT stream_id, pseudo_id FROM ("+strings.Join(sources, " UNION DISTINCT ")+"))")
		for _, table := range gaTables {
			statements = append(statements, fmt.Sprintf("DELETE FROM `%s` AS target WHERE target.user_id=@user OR ((target.user_id IS NULL OR target.user_id='') AND EXISTS (SELECT 1 FROM UNNEST(linked_ids) AS linked WHERE linked.stream_id=target.stream_id AND linked.pseudo_id=target.user_pseudo_id))", table))
		}
	}
	// 연결 근거 수집과 모든 사본 삭제를 하나의 트랜잭션으로 묶는다.
	// streaming buffer 등의 부분 실패 시 근거를 먼저 잃지 않는다.
	statements = append(statements,
		fmt.Sprintf("DELETE FROM `%s.%s.events` WHERE app_id=@app AND platform_user_id=@user", project, dataset),
		fmt.Sprintf("DELETE FROM `%s.%s.audit` WHERE app_id=@app AND platform_user_id=@user AND NOT STARTS_WITH(action, 'iap.')", project, dataset),
		"COMMIT TRANSACTION")
	return strings.Join(statements, ";\n") + ";"
}
func waitDeletionJob(ctx context.Context, job *bigquery.Job, ref string) (string, error) {
	status, err := job.Wait(ctx)
	if err != nil {
		return ref, err
	}
	// 끝난 실패 작업 ID는 재사용할 수 없다. 다음 시도는 새 작업으로
	// streaming buffer 지연 등을 다시 처리한다. 진행 중 작업만 이어받는다.
	if err = status.Err(); err != nil {
		return "", err
	}
	return ref, nil
}
func isGoogleNotFound(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound
}
func (c *Collector) SubmitAnalyticsDeletion(ctx context.Context, app registry.App, puid string) (time.Time, error) {
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
