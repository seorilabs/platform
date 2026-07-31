package events

import (
	"testing"

	"cloud.google.com/go/bigquery"
)

// 파티션·클러스터 필드는 그 테이블의 스키마에 있어야 한다.
//
// 없으면 BigQuery가 테이블 생성을 거부한다. 부팅은 계속되고
// 적재만 조용히 실패하기 때문에 눈치채기 어렵다.
//
// 실제로 두 테이블이 events용 필드(received_at, event_name)를
// 공유하고 있었고, audit에는 그 필드가 없어서 감사 원장이 한 줄도
// 쌓이지 않았다. 웹훅 로그의 "감사 기록 실패 ... notFound"로 드러났다.
func TestTableLayoutFieldsExistInSchema(t *testing.T) {
	tests := []struct {
		name   string
		schema bigquery.Schema
		layout tableLayout
	}{
		{"events", eventsSchema, eventsLayout},
		{"audit", auditSchema, auditLayout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			have := map[string]bigquery.FieldType{}
			for _, f := range tt.schema {
				have[f.Name] = f.Type
			}

			typ, ok := have[tt.layout.partitionField]
			if !ok {
				t.Fatalf("파티션 필드 %q가 스키마에 없다", tt.layout.partitionField)
			}
			// 시간 파티션은 TIMESTAMP나 DATE여야 한다.
			if typ != bigquery.TimestampFieldType && typ != bigquery.DateFieldType {
				t.Errorf("파티션 필드 %q의 타입이 %s다", tt.layout.partitionField, typ)
			}

			if len(tt.layout.clusterFields) == 0 {
				t.Error("클러스터 필드가 비어 있다")
			}
			for _, name := range tt.layout.clusterFields {
				if _, ok := have[name]; !ok {
					t.Errorf("클러스터 필드 %q가 스키마에 없다", name)
				}
			}
		})
	}
}
