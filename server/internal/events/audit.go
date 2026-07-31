package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"cloud.google.com/go/bigquery"
)

// 감사 원장이 필요한 이유.
//
// Firestore는 분석 DB가 아니다. 조인 불가, LIKE 검색 불가, 집계는 count
// 수준이고 복합 쿼리는 사전 인덱스가 필수다. 그래서 플랫폼의 모든 쓰기를
// 여기에 한 줄씩 남긴다. "지난주 결제 성공률" 같은 운영 질문은 전부
// SQL로 답한다.
//
// 결제는 정산과 감사 쿼리가 필수라 이게 특히 중요하다.
// docs/03-architecture/overview.md 참고.

// AuditAction은 기록 대상 행위다.
type AuditAction string

const (
	AuditIdentityCreated AuditAction = "identity.created"
	AuditSessionIssued   AuditAction = "session.issued"
	AuditSessionRefresh  AuditAction = "session.refreshed"
	AuditUserDeleted     AuditAction = "user.deleted"

	AuditConfigPublished AuditAction = "config.published"

	AuditIAPVerified  AuditAction = "iap.verified"
	AuditIAPGranted   AuditAction = "iap.granted"
	AuditIAPRevoked   AuditAction = "iap.revoked"
	AuditIAPCompleted AuditAction = "iap.completed"
	AuditIAPWebhook   AuditAction = "iap.webhook"

	AuditOperatorGrant  AuditAction = "operator.grant"
	AuditOperatorRevoke AuditAction = "operator.revoke"
)

// AuditRow는 감사 원장 한 줄이다.
type AuditRow struct {
	TS             time.Time
	Action         AuditAction
	AppID          string
	PlatformUserID string
	// Actor는 운영자 작업일 때 누가 했는지다. 런타임 작업이면 비어 있다.
	Actor string
	// RequestID는 멱등 키다. 같은 요청이 두 번 기록돼도 구분된다.
	RequestID string
	// Outcome은 ok 또는 에러 코드다.
	Outcome string
	// Detail은 행위별 부가 정보다. 토큰이나 영수증 원문을 넣지 않는다.
	Detail map[string]any
}

func (r *AuditRow) Save() (map[string]bigquery.Value, string, error) {
	detail := "{}"
	if len(r.Detail) > 0 {
		if b, err := json.Marshal(r.Detail); err == nil {
			detail = string(b)
		}
	}
	return map[string]bigquery.Value{
		"ts":               r.TS,
		"action":           string(r.Action),
		"app_id":           r.AppID,
		"platform_user_id": r.PlatformUserID,
		"actor":            r.Actor,
		"request_id":       r.RequestID,
		"outcome":          r.Outcome,
		"detail":           detail,
	}, "", nil
}

var auditSchema = bigquery.Schema{
	{Name: "ts", Type: bigquery.TimestampFieldType, Required: true},
	{Name: "action", Type: bigquery.StringFieldType, Required: true},
	{Name: "app_id", Type: bigquery.StringFieldType},
	{Name: "platform_user_id", Type: bigquery.StringFieldType},
	{Name: "actor", Type: bigquery.StringFieldType, Description: "운영자 작업일 때만"},
	{Name: "request_id", Type: bigquery.StringFieldType, Description: "멱등 키"},
	{Name: "outcome", Type: bigquery.StringFieldType, Description: "ok 또는 에러 코드"},
	{Name: "detail", Type: bigquery.JSONFieldType},
}

// Audit은 감사 원장에 한 줄 남긴다.
//
// 실패해도 본 작업을 막지 않는다. 감사 기록이 안 됐다고 결제를 되돌리면
// 더 큰 문제가 된다. 대신 에러 로그를 남겨 나중에 추적할 수 있게 한다.
func (c *Collector) Audit(ctx context.Context, row AuditRow) {
	if row.TS.IsZero() {
		row.TS = c.now()
	}

	ins := c.client.Dataset(c.dataset).Table(AuditTable).Inserter()
	ins.SkipInvalidRows = true
	ins.IgnoreUnknownValues = true

	if err := ins.Put(ctx, []*AuditRow{&row}); err != nil {
		slog.ErrorContext(ctx, "감사 기록 실패",
			"action", row.Action,
			"app_id", row.AppID,
			"request_id", row.RequestID,
			"err", err,
		)
	}
}
