package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/bigquery"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// 테이블 이름.
//
// events는 앱 이벤트, audit은 플랫폼 자체 쓰기 기록이다.
// Firestore는 조인·집계·LIKE가 안 되므로 운영 질문은 전부 audit으로 답한다.
const (
	EventsTable = "events"
	AuditTable  = "audit"
)

// MaxBatchSize는 한 요청에 담을 수 있는 이벤트 수다.
const MaxBatchSize = 25

// clampWindow는 클라이언트 시각을 신뢰하는 범위다.
//
// 오프라인 큐 때문에 과거 이벤트가 늦게 올 수 있지만, 시계가 어긋난
// 기기의 값을 그대로 두면 파티션이 엉뚱한 곳에 생긴다.
const clampWindow = 48 * time.Hour

// Row는 BigQuery에 적재할 이벤트 한 건이다.
type Row struct {
	EventID        string
	ReceivedAt     time.Time
	EventTS        time.Time
	AppID          string
	PlatformUserID string
	GA4ClientID    string
	SessionID      string
	EventName      string
	Platform       string
	AppVersion     string
	Locale         string
	Country        string
	Params         map[string]any
	SDKVersion     string
}

// Save는 bigquery.ValueSaver 구현이다.
//
// InsertID를 비워두면 BigQuery가 중복 제거를 하지 않는다.
// 우리는 at-least-once를 받아들이고 중복 제거는 쿼리에서 한다.
// 서버 dedup은 write 비용 대비 가치가 없다.
func (r *Row) Save() (map[string]bigquery.Value, string, error) {
	params := "{}"
	if len(r.Params) > 0 {
		b, err := json.Marshal(r.Params)
		if err != nil {
			// 정규화를 거친 값이라 여기서 실패할 일은 거의 없다.
			// 그래도 이벤트 하나 때문에 배치를 통째로 버리지는 않는다.
			slog.Error("params 직렬화 실패", "event_id", r.EventID, "err", err)
		} else {
			params = string(b)
		}
	}

	return map[string]bigquery.Value{
		"event_id":         r.EventID,
		"received_at":      r.ReceivedAt,
		"event_ts":         r.EventTS,
		"app_id":           r.AppID,
		"platform_user_id": r.PlatformUserID,
		"ga4_client_id":    r.GA4ClientID,
		"session_id":       r.SessionID,
		"event_name":       r.EventName,
		"platform":         r.Platform,
		"app_version":      r.AppVersion,
		"locale":           r.Locale,
		"country":          r.Country,
		"params":           params,
		"sdk_version":      r.SDKVersion,
	}, "", nil
}

// eventsSchema는 events 테이블 스키마다.
//
// docs/03-architecture/events.md의 정의와 같아야 한다.
var eventsSchema = bigquery.Schema{
	{Name: "event_id", Type: bigquery.StringFieldType, Required: true,
		Description: "클라이언트 생성 ULID. at-least-once라 중복 제거는 쿼리에서"},
	{Name: "received_at", Type: bigquery.TimestampFieldType, Required: true,
		Description: "서버 수신 시각. 파티션 기준"},
	{Name: "event_ts", Type: bigquery.TimestampFieldType, Required: true,
		Description: "클라이언트 시각. ±48시간으로 clamp"},
	{Name: "app_id", Type: bigquery.StringFieldType, Required: true},
	{Name: "platform_user_id", Type: bigquery.StringFieldType},
	{Name: "ga4_client_id", Type: bigquery.StringFieldType, Description: "GA4 대조용"},
	{Name: "session_id", Type: bigquery.StringFieldType},
	{Name: "event_name", Type: bigquery.StringFieldType, Required: true,
		Description: "정규화됨. 레지스트리 event_prefix가 제거된 이름"},
	{Name: "platform", Type: bigquery.StringFieldType, Description: "android|ios|web|ait"},
	{Name: "app_version", Type: bigquery.StringFieldType},
	{Name: "locale", Type: bigquery.StringFieldType},
	{Name: "country", Type: bigquery.StringFieldType},
	{Name: "params", Type: bigquery.JSONFieldType},
	{Name: "sdk_version", Type: bigquery.StringFieldType},
}

// Collector는 이벤트를 BigQuery에 적재한다.
type Collector struct {
	client  *bigquery.Client
	dataset string
	now     func() time.Time
}

// NewCollector는 수집기를 만든다.
func NewCollector(ctx context.Context, projectID, dataset string) (*Collector, error) {
	c, err := bigquery.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("events: BigQuery 클라이언트 생성 실패: %w", err)
	}
	return &Collector{client: c, dataset: dataset, now: time.Now}, nil
}

func (c *Collector) Close() error { return c.client.Close() }

// EnsureTables는 테이블이 없으면 만든다.
//
// 부팅 시 한 번 부른다. 스키마리스가 아니라 스키마가 필요하므로
// 마이그레이션 대신 이 방식을 쓴다. 이미 있으면 아무것도 하지 않는다.
func (c *Collector) EnsureTables(ctx context.Context) error {
	if err := c.ensureTable(ctx, EventsTable, eventsSchema); err != nil {
		return err
	}
	return c.ensureTable(ctx, AuditTable, auditSchema)
}

func (c *Collector) ensureTable(ctx context.Context, name string, schema bigquery.Schema) error {
	t := c.client.Dataset(c.dataset).Table(name)

	if _, err := t.Metadata(ctx); err == nil {
		return nil // 이미 있다
	}

	meta := &bigquery.TableMetadata{
		Schema: schema,
		// received_at 기준으로 파티션한다.
		// event_ts로 하면 지각 이벤트가 과거 파티션을 건드려
		// 프루닝이 불안정해진다.
		TimePartitioning: &bigquery.TimePartitioning{
			Field:      "received_at",
			Type:       bigquery.DayPartitioningType,
			Expiration: 400 * 24 * time.Hour,
		},
		Clustering: &bigquery.Clustering{
			Fields: []string{"app_id", "event_name"},
		},
	}

	if err := t.Create(ctx, meta); err != nil {
		return fmt.Errorf("events: %s 테이블 생성 실패: %w", name, err)
	}
	slog.InfoContext(ctx, "BigQuery 테이블 생성", "dataset", c.dataset, "table", name)
	return nil
}

// Insert는 이벤트를 적재한다.
//
// legacy streaming insert를 쓴다. Storage Write API가 무료 한도가 더
// 크지만 protobuf 디스크립터 준비가 필요해 복잡하다. 현재 규모에서는
// 둘 다 무료 범위이므로 단순한 쪽을 골랐다.
// 적재량이 늘면 managedwriter로 교체한다. 이 함수만 바뀐다.
func (c *Collector) Insert(ctx context.Context, rows []*Row) error {
	if len(rows) == 0 {
		return nil
	}

	ins := c.client.Dataset(c.dataset).Table(EventsTable).Inserter()
	// 일부 행이 스키마와 안 맞아도 나머지는 넣는다.
	// 이벤트 하나 때문에 배치를 통째로 버리면 손실이 커진다.
	ins.SkipInvalidRows = true
	ins.IgnoreUnknownValues = true

	if err := ins.Put(ctx, rows); err != nil {
		return platformerr.Wrap(err, platformerr.CodeInternal, "이벤트를 저장하지 못했어요")
	}
	return nil
}

// ClampEventTime은 클라이언트 시각을 신뢰 범위로 자른다.
func (c *Collector) ClampEventTime(t time.Time) time.Time {
	now := c.now()
	if t.IsZero() {
		return now
	}
	if t.Before(now.Add(-clampWindow)) {
		return now.Add(-clampWindow)
	}
	if t.After(now.Add(clampWindow)) {
		return now
	}
	return t
}

// Now는 수집기의 시계를 돌려준다.
func (c *Collector) Now() time.Time { return c.now() }
