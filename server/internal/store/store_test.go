package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/fspath"
)

// resolve는 Firestore 연결 없이 테스트할 수 있다.
// 경로 통제가 이 패키지의 핵심 책임이므로 여기에 테스트를 집중한다.
func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		path    string
		want    fspath.Kind
		expect  string
		wantErr bool
	}{
		{
			name:   "production은 prefix 없이",
			prefix: "",
			path:   "users/pu_1",
			want:   fspath.KindDocument,
			expect: "users/pu_1",
		},
		{
			name:   "staging은 첫 세그먼트에 prefix",
			prefix: "stg_",
			path:   "users/pu_1",
			want:   fspath.KindDocument,
			expect: "stg_users/pu_1",
		},
		{
			name:   "깊은 경로도 첫 세그먼트에만",
			prefix: "stg_",
			path:   "iap_users/pu_1/entitlements/sp_a",
			want:   fspath.KindDocument,
			expect: "stg_iap_users/pu_1/entitlements/sp_a",
		},
		{
			name:   "컬렉션도 동일",
			prefix: "stg_",
			path:   "processed_orders",
			want:   fspath.KindCollection,
			expect: "stg_processed_orders",
		},
		{
			name:    "문서를 기대했는데 컬렉션이면 거부",
			prefix:  "",
			path:    "users",
			want:    fspath.KindDocument,
			wantErr: true,
		},
		{
			name:    "컬렉션을 기대했는데 문서면 거부",
			prefix:  "",
			path:    "users/pu_1",
			want:    fspath.KindCollection,
			wantErr: true,
		},
		{
			name:    "이미 prefix가 붙어 있으면 거부",
			prefix:  "stg_",
			path:    "stg_users/pu_1",
			want:    fspath.KindDocument,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// fs가 nil이어도 resolve는 쓰지 않으므로 안전하다.
			c := NewWithClient(nil, tt.prefix)

			p, err := fspath.Parse(tt.path)
			if err != nil {
				t.Fatalf("테스트 준비 실패: %v", err)
			}

			got, err := c.resolve(p, tt.want)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("에러를 기대했는데 %q를 받았다", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("예상치 못한 에러: %v", err)
			}
			if got != tt.expect {
				t.Errorf("resolve() = %q, want %q", got, tt.expect)
			}
		})
	}
}

// prefix에 슬래시가 들어가면 경로 구조가 깨진다.
// "stg/" 같은 값이 들어오면 세그먼트 수가 바뀌어 문서가 컬렉션이 된다.
func TestNewRejectsSlashInPrefix(t *testing.T) {
	_, err := New(context.Background(), "demo-project", "stg/")
	if err == nil {
		t.Fatal("슬래시가 든 prefix를 거부하지 않았다")
	}
	if !strings.Contains(err.Error(), "슬래시") {
		t.Errorf("에러 메시지가 원인을 설명하지 않는다: %v", err)
	}
}

func TestNewRequiresProjectID(t *testing.T) {
	_, err := New(context.Background(), "", "")
	if err == nil {
		t.Fatal("빈 프로젝트 ID를 거부하지 않았다")
	}
}

func TestPrefix(t *testing.T) {
	c := NewWithClient(nil, "stg_")
	if got := c.Prefix(); got != "stg_" {
		t.Errorf("Prefix() = %q, want %q", got, "stg_")
	}
}

// mapErr는 gRPC 상태를 store sentinel로 바꾼다.
// Firestore는 "없음"을 일반 에러로 주므로 이 변환이 없으면
// 네트워크 실패와 구분되지 않는다.
func TestMapErr(t *testing.T) {
	p, err := fspath.Parse("users/pu_1")
	if err != nil {
		t.Fatalf("테스트 준비 실패: %v", err)
	}

	t.Run("nil은 nil", func(t *testing.T) {
		if got := mapErr(nil, p); got != nil {
			t.Errorf("mapErr(nil) = %v, want nil", got)
		}
	})

	t.Run("일반 에러는 감싸서 전달", func(t *testing.T) {
		cause := errors.New("네트워크 끊김")
		got := mapErr(cause, p)

		if errors.Is(got, ErrNotFound) {
			t.Error("일반 에러를 ErrNotFound로 잘못 분류했다")
		}
		if !errors.Is(got, cause) {
			t.Error("원인이 보존되지 않았다")
		}
		if !strings.Contains(got.Error(), "users/pu_1") {
			t.Errorf("경로가 메시지에 없다: %v", got)
		}
	})
}
