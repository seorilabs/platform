package content

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// 운글의 기존 computeChart 결과 123건과 조합 원고 중복 회귀 6건에서 내보낸 자료다.
// 앱의 실제 요청 전체를 검증한 뒤, 명식에서 같은 해설 좌표를 다시 만들어야 한다.
func TestTopicContextMatchesUngeulCharts(t *testing.T) {
	bytes, err := os.ReadFile("testdata/ungeul-topic-context.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Chart      ChartFacts          `json:"chart"`
		IDs        []string            `json:"ids"`
		Sinsal     []SinsalFact        `json:"sinsal"`
		SupportIDs []string            `json:"supportIds"`
		Reading    DerivedReadingFacts `json:"reading"`
	}
	if err := json.Unmarshal(bytes, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 129 {
		t.Fatalf("대조 표본 수: %d, want 129", len(fixtures))
	}
	hiddenIDs := map[string]bool{}
	duplicates := map[string]int{}
	for i, fixture := range fixtures {
		if fixture.Reading.Chart != fixture.Chart {
			t.Fatalf("표본 %d: 실제 요청과 대조 명식이 다르다", i)
		}
		selection, err := Select(ResolveRequest{
			SchemaVersion: SupportedSchemaVersion,
			Reading:       fixture.Reading,
			Scope:         []string{"base"},
		})
		if err != nil {
			t.Fatalf("표본 %d: 앱이 만든 요청을 서버가 거절했다: %v", i, err)
		}
		seenJohap := map[JohapFact]bool{}
		for _, johap := range fixture.Reading.Johap {
			if seenJohap[johap] {
				duplicates[fixture.Reading.Kind]++
			}
			seenJohap[johap] = true
			if !slices.Contains(selection.BaseIDs, "johap."+johap.Sipseong+"_"+johap.Unseong) {
				t.Fatalf("표본 %d: 기둥의 조합 해설 누락: %v", i, johap)
			}
		}
		if got := topicSupportIDs(fixture.Sinsal); !reflect.DeepEqual(got, fixture.SupportIDs) {
			t.Fatalf("신살 표본 %d: got %v, want %v", i, got, fixture.SupportIDs)
		}
		if got := topicContextIDs(fixture.Chart); !reflect.DeepEqual(got, fixture.IDs) {
			t.Fatalf("표본 %d: got %v, want %v", i, got, fixture.IDs)
		}
		for _, id := range fixture.IDs {
			if !slices.Contains(selection.OptionalBaseIDs, id) {
				t.Fatalf("표본 %d: 실제 요청의 종합 해설 누락: %s", i, id)
			}
			if strings.HasPrefix(id, "topic-hidden.") {
				hiddenIDs[id] = true
			}
		}
		for _, id := range fixture.SupportIDs {
			if !slices.Contains(selection.OptionalBaseIDs, id) {
				t.Fatalf("표본 %d: 실제 요청의 신살 연결 해설 누락: %s", i, id)
			}
		}
	}
	if duplicates["full"] == 0 || duplicates["three_pillar"] == 0 {
		t.Fatalf("기둥별 중복 조합 대조 누락: %v", duplicates)
	}
	if len(hiddenIDs) != 12 {
		t.Fatalf("지장간 해설 대조 범위: %d, want 12", len(hiddenIDs))
	}
}

func TestTopicContextIsAdditiveAndNotClientSelected(t *testing.T) {
	req := validResolveRequest()
	selection, err := Select(req)
	if err != nil {
		t.Fatal(err)
	}
	ids := append(topicContextIDs(req.Reading.Chart), topicSupportIDs(req.Reading.Sinsal)...)
	for _, id := range ids {
		if !slices.Contains(selection.OptionalBaseIDs, id) {
			t.Errorf("추가 해설 누락: %s", id)
		}
		if slices.Contains(selection.BaseIDs, id) {
			t.Errorf("구 릴리스의 필수 항목으로 추가됨: %s", id)
		}
	}
}
