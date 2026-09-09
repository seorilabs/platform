package content

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// 운글의 실제 computeChart 결과 123건에서 내보낸 자료다. 서버는 클라이언트가
// 고른 ID를 신뢰하지 않고 검증된 명식만으로 같은 좌표를 다시 만들어야 한다.
func TestTopicContextMatchesUngeulCharts(t *testing.T) {
	bytes, err := os.ReadFile("testdata/ungeul-topic-context.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Chart      ChartFacts   `json:"chart"`
		IDs        []string     `json:"ids"`
		Sinsal     []SinsalFact `json:"sinsal"`
		SupportIDs []string     `json:"supportIds"`
	}
	if err := json.Unmarshal(bytes, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 100 {
		t.Fatalf("대조 표본 부족: %d", len(fixtures))
	}
	hiddenIDs := map[string]bool{}
	for i, fixture := range fixtures {
		if got := topicSupportIDs(fixture.Sinsal); !reflect.DeepEqual(got, fixture.SupportIDs) {
			t.Fatalf("신살 표본 %d: got %v, want %v", i, got, fixture.SupportIDs)
		}
		if got := topicContextIDs(fixture.Chart); !reflect.DeepEqual(got, fixture.IDs) {
			t.Fatalf("표본 %d: got %v, want %v", i, got, fixture.IDs)
		}
		for _, id := range fixture.IDs {
			if strings.HasPrefix(id, "topic-hidden.") {
				hiddenIDs[id] = true
			}
		}
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
