package events

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conformance 벡터로 정규화를 검증한다.
//
// 같은 벡터를 TS SDK와 GDScript SDK도 통과해야 한다.
// 세 구현이 동일한 출력을 내는 게 목표다.
//
// 조직에서 실제로 일어난 일이라 이 장치를 둔다. 같은 목적의 GA4 MP
// 클라이언트 3벌이 boolean을 1/0, "true"/"false", 미처리로 서로 다르게
// 다뤄 동작이 갈렸다. 문서로 적어두면 6개월 뒤 갈라지지만 벡터는 CI가 지킨다.

type conformanceFile struct {
	Version int      `json:"version"`
	PIIKeys []string `json:"pii_keys"`
	Cases   []struct {
		Name string         `json:"name"`
		In   map[string]any `json:"in"`
		Out  map[string]any `json:"out"`
	} `json:"cases"`
}

func loadConformance(t *testing.T) conformanceFile {
	t.Helper()

	// 벡터는 spec/ 에 있다. 서버 모듈 밖이라 상대 경로로 올라간다.
	path := filepath.Join("..", "..", "..", "spec", "conformance", "param-normalization.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("벡터 파일을 읽지 못했다 %s: %v", path, err)
	}

	var f conformanceFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("벡터 파싱 실패: %v", err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("벡터에 케이스가 없다")
	}
	return f
}

// resolveSentinels는 JSON으로 표현할 수 없는 값을 실제 값으로 바꾼다.
// JSON에는 NaN과 Infinity가 없어 문자열 센티넬을 쓴다.
func resolveSentinels(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		s, isStr := v.(string)
		if !isStr {
			out[k] = v
			continue
		}
		switch s {
		case "__nan__":
			out[k] = math.NaN()
		case "__pos_inf__":
			out[k] = math.Inf(1)
		case "__neg_inf__":
			out[k] = math.Inf(-1)
		case "__null__":
			out[k] = nil
		default:
			out[k] = v
		}
	}
	return out
}

func TestNormalizeParamsConformance(t *testing.T) {
	f := loadConformance(t)

	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := NormalizeParams(resolveSentinels(c.In))

			if len(got) != len(c.Out) {
				t.Fatalf("키 개수 = %d, want %d\ngot  = %s\nwant = %s",
					len(got), len(c.Out), dump(got), dump(c.Out))
			}
			for k, want := range c.Out {
				g, ok := got[k]
				if !ok {
					t.Errorf("키 %q가 없다\ngot = %s", k, dump(got))
					continue
				}
				if !sameValue(g, want) {
					t.Errorf("키 %q = %v (%T), want %v (%T)", k, g, g, want, want)
				}
			}
		})
	}
}

// 벡터의 PII 키 목록과 구현이 일치해야 한다.
// 한쪽만 늘어나면 SDK와 서버가 다르게 거른다.
func TestPIIKeysMatchConformance(t *testing.T) {
	f := loadConformance(t)

	for _, k := range f.PIIKeys {
		if !IsPIIKey(k) {
			t.Errorf("벡터의 PII 키 %q를 구현이 거르지 않는다", k)
		}
	}
	if len(f.PIIKeys) != len(piiKeys) {
		t.Errorf("PII 키 개수가 다르다. 벡터 %d개, 구현 %d개", len(f.PIIKeys), len(piiKeys))
	}
}

func TestNormalizeParamsEdgeCases(t *testing.T) {
	t.Run("nil 맵은 빈 맵", func(t *testing.T) {
		if got := NormalizeParams(nil); len(got) != 0 {
			t.Errorf("got = %v, want 빈 맵", got)
		}
	})

	t.Run("이모지는 룬 단위로 자른다", func(t *testing.T) {
		// 바이트로 자르면 깨진 문자가 남는다.
		long := strings.Repeat("🦎", MaxParamValueLen+10)
		got := NormalizeParams(map[string]any{"emoji": long})

		s, ok := got["emoji"].(string)
		if !ok {
			t.Fatalf("문자열이 아니다: %T", got["emoji"])
		}
		if !isValidUTF8(s) {
			t.Error("자른 결과가 깨진 UTF-8이다")
		}
		if runeLen(s) != MaxParamValueLen {
			t.Errorf("길이 = %d 룬, want %d", runeLen(s), MaxParamValueLen)
		}
	})

	t.Run("정수로 떨어지는 실수는 정수로", func(t *testing.T) {
		got := NormalizeParams(map[string]any{"n": float64(42)})
		if _, ok := got["n"].(int64); !ok {
			t.Errorf("타입 = %T, want int64", got["n"])
		}
	})

	t.Run("소수는 실수 유지", func(t *testing.T) {
		got := NormalizeParams(map[string]any{"n": 0.5})
		if _, ok := got["n"].(float64); !ok {
			t.Errorf("타입 = %T, want float64", got["n"])
		}
	})

	t.Run("중첩 맵은 제거", func(t *testing.T) {
		got := NormalizeParams(map[string]any{
			"meta": map[string]any{"a": 1},
			"keep": 1,
		})
		if _, has := got["meta"]; has {
			t.Error("중첩 맵이 남았다")
		}
		if _, has := got["keep"]; !has {
			t.Error("정상 값까지 지웠다")
		}
	})
}

func TestNormalizeEventName(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"purchase_verified", "purchase_verified", true},
		{"  spaced  ", "spaced", true},
		{"", "", false},
		{"   ", "", false},
		{strings.Repeat("a", MaxEventNameLen), strings.Repeat("a", MaxEventNameLen), true},
		{strings.Repeat("a", MaxEventNameLen+1), "", false},
	}

	for _, tt := range tests {
		got, ok := NormalizeEventName(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("NormalizeEventName(%q) = (%q, %v), want (%q, %v)",
				tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// sameValue는 JSON에서 온 기대값과 Go 값을 비교한다.
// JSON 숫자는 전부 float64라 정수 비교가 어긋나는 걸 흡수한다.
func sameValue(got, want any) bool {
	gf, gok := toFloat(got)
	wf, wok := toFloat(want)
	if gok && wok {
		return gf == wf
	}
	return fmt.Sprint(got) == fmt.Sprint(want)
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case float64:
		return t, true
	case float32:
		return float64(t), true
	default:
		return 0, false
	}
}

func dump(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
