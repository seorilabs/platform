package fspath

import (
	"errors"
	"testing"
)

// 이 파일은 완성된 상태다. path.go의 TODO를 채워 이 테스트를 통과시키면 된다.
//
// Go 테이블 드리븐 테스트의 표준 형태다.
//   - 케이스를 구조체 슬라이스로 나열한다
//   - t.Run으로 서브테스트를 만들어 실패한 케이스 이름이 바로 보이게 한다
//   - 에러는 errors.Is로 판정한다. 문자열 비교를 하지 않는다
//   - 변수 이름은 got/want를 쓴다. Go 커뮤니티 관용구다

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string // String()의 기대값
		wantKnd Kind
		wantErr error // nil이면 성공을 기대한다
	}{
		{
			name:    "단일 세그먼트는 컬렉션",
			raw:     "users",
			want:    "users",
			wantKnd: KindCollection,
		},
		{
			name:    "두 세그먼트는 문서",
			raw:     "users/pu_01JXYZ",
			want:    "users/pu_01JXYZ",
			wantKnd: KindDocument,
		},
		{
			name:    "세 세그먼트는 컬렉션",
			raw:     "users/pu_01JXYZ/entitlements",
			want:    "users/pu_01JXYZ/entitlements",
			wantKnd: KindCollection,
		},
		{
			name:    "네 세그먼트는 문서",
			raw:     "users/pu_01JXYZ/entitlements/sp_galaxy_gecko",
			want:    "users/pu_01JXYZ/entitlements/sp_galaxy_gecko",
			wantKnd: KindDocument,
		},
		{
			name:    "점과 밑줄과 하이픈은 허용",
			raw:     "processed_orders/a.b-c_d",
			want:    "processed_orders/a.b-c_d",
			wantKnd: KindDocument,
		},
		{
			name:    "환경 prefix가 붙은 경로도 파싱된다",
			raw:     "iap_environments/sandbox/processed_orders/abc",
			want:    "iap_environments/sandbox/processed_orders/abc",
			wantKnd: KindDocument,
		},
		{
			name:    "빈 경로",
			raw:     "",
			wantErr: ErrEmptyPath,
		},
		{
			name:    "공백만 있는 경로",
			raw:     "   ",
			wantErr: ErrEmptyPath,
		},
		{
			name:    "앞에 슬래시",
			raw:     "/users",
			wantErr: ErrEmptySegment,
		},
		{
			name:    "뒤에 슬래시",
			raw:     "users/",
			wantErr: ErrEmptySegment,
		},
		{
			name:    "연속 슬래시",
			raw:     "users//pu_1",
			wantErr: ErrEmptySegment,
		},
		{
			name:    "슬래시 하나만",
			raw:     "/",
			wantErr: ErrEmptySegment,
		},
		{
			name:    "공백이 들어간 세그먼트",
			raw:     "users/pu 1",
			wantErr: ErrInvalidSegment,
		},
		{
			name:    "한글 세그먼트는 거부",
			raw:     "users/사용자",
			wantErr: ErrInvalidSegment,
		},
		{
			name:    "콜론은 거부",
			raw:     "users/pu:1",
			wantErr: ErrInvalidSegment,
		},
		{
			name:    "현재 디렉토리 표기",
			raw:     "users/.",
			wantErr: ErrReservedSegment,
		},
		{
			name:    "상위 디렉토리 표기",
			raw:     "users/..",
			wantErr: ErrReservedSegment,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.raw)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Parse(%q) 에러 = %v, want %v", tt.raw, err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("Parse(%q) 예상치 못한 에러 = %v", tt.raw, err)
			}
			if got.String() != tt.want {
				t.Errorf("String() = %q, want %q", got.String(), tt.want)
			}
			if got.Kind() != tt.wantKnd {
				t.Errorf("Kind() = %v, want %v", got.Kind(), tt.wantKnd)
			}
		})
	}
}

func TestWithEnvPrefix(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		prefix  string
		want    string
		wantErr error
	}{
		{
			name:   "prefix가 빈 문자열이면 그대로",
			raw:    "users/pu_1",
			prefix: "",
			want:   "users/pu_1",
		},
		{
			name:   "첫 세그먼트에만 붙는다",
			raw:    "users/pu_1",
			prefix: "stg_",
			want:   "stg_users/pu_1",
		},
		{
			name:   "단일 세그먼트에도 붙는다",
			raw:    "users",
			prefix: "stg_",
			want:   "stg_users",
		},
		{
			name:   "깊은 경로도 첫 세그먼트에만",
			raw:    "users/pu_1/entitlements/sp_a",
			prefix: "stg_",
			want:   "stg_users/pu_1/entitlements/sp_a",
		},
		{
			name:    "이미 붙어 있으면 에러",
			raw:     "stg_users/pu_1",
			prefix:  "stg_",
			wantErr: ErrPrefixNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mustParse(t, tt.raw)

			got, err := p.WithEnvPrefix(tt.prefix)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("WithEnvPrefix(%q) 에러 = %v, want %v", tt.prefix, err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("WithEnvPrefix(%q) 예상치 못한 에러 = %v", tt.prefix, err)
			}
			if got.String() != tt.want {
				t.Errorf("String() = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

// prefix를 적용해도 컬렉션/문서 구분은 바뀌지 않아야 한다.
// 세그먼트 수가 그대로이기 때문인데, 구현이 세그먼트를 추가하는 방식이면 깨진다.
func TestWithEnvPrefixKeepsKind(t *testing.T) {
	p := mustParse(t, "users/pu_1")

	got, err := p.WithEnvPrefix("stg_")
	if err != nil {
		t.Fatalf("예상치 못한 에러 = %v", err)
	}
	if got.Kind() != KindDocument {
		t.Errorf("Kind() = %v, want %v", got.Kind(), KindDocument)
	}
}

// mustParse는 테스트 준비 단계에서 쓰는 헬퍼다.
//
// t.Helper()를 호출하면 실패 시 이 함수가 아니라 호출한 쪽 줄 번호가 표시된다.
func mustParse(t *testing.T, raw string) Path {
	t.Helper()
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("테스트 준비 실패: Parse(%q) = %v", raw, err)
	}
	return p
}
