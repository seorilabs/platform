package platformerr

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strconv"
	"testing"
)

// TestEveryCodeHasStatus는 선언된 모든 Code 상수가 statusByCode에 있는지 검증한다.
//
// 코드가 60개 넘어 새 코드를 추가하면서 대응표 등록을 빠뜨리기 쉽다.
// 빠뜨리면 statusOf가 조용히 500을 주는데, 그건 4xx여야 할 클라이언트 실수를
// 서버 장애로 보고하게 만든다.
//
// code.go를 AST로 파싱해 Code 타입 상수를 전부 뽑아 비교한다.
// 목록을 테스트에 손으로 나열하면 그 목록 자체가 또 갱신 대상이 되므로 그렇게 하지 않는다.
func TestEveryCodeHasStatus(t *testing.T) {
	declared := parseDeclaredCodes(t, "code.go")

	if len(declared) < 50 {
		t.Fatalf("선언된 코드가 %d개뿐이다. 파싱이 잘못됐을 수 있다", len(declared))
	}

	for _, c := range declared {
		if _, ok := statusByCode[Code(c)]; !ok {
			t.Errorf("Code %q가 statusByCode에 없다. 등록하지 않으면 500이 된다", c)
		}
	}

	// 반대 방향도 본다. 맵에만 있고 선언이 사라진 코드는 죽은 항목이다.
	declaredSet := make(map[string]bool, len(declared))
	for _, c := range declared {
		declaredSet[c] = true
	}
	for c := range statusByCode {
		if !declaredSet[string(c)] {
			t.Errorf("statusByCode의 %q에 대응하는 Code 상수 선언이 없다", c)
		}
	}
}

// TestStatusesAreClientOrServerErrors는 모든 상태가 4xx 또는 5xx인지 본다.
// 에러에 2xx나 3xx가 들어가면 httpx가 성공으로 응답해버린다.
func TestStatusesAreClientOrServerErrors(t *testing.T) {
	for c, s := range statusByCode {
		if s < 400 || s > 599 {
			t.Errorf("Code %q의 상태가 %d다. 4xx 또는 5xx여야 한다", c, s)
		}
	}
}

// TestInvariantCodeStatuses는 불변식과 직접 연결된 코드의 상태를 고정한다.
// 이 값들이 바뀌면 클라이언트 분기가 깨진다.
func TestInvariantCodeStatuses(t *testing.T) {
	tests := []struct {
		code Code
		want int
		why  string
	}{
		{CodePurchaseOwnedByAnotherUser, http.StatusConflict, "불변식 4 cross-uid 자동 이전 금지"},
		{CodePurchaseReplayMismatch, http.StatusConflict, "replay 불일치"},
		{CodeProductTypeMismatch, http.StatusUnprocessableEntity, "불변식 9 NON_CONSUMABLE 강제"},
		{CodeEnvironmentMismatch, http.StatusUnprocessableEntity, "불변식 9 환경 fallback 금지"},
		{CodeProviderCompletionPending, http.StatusServiceUnavailable, "불변식 7 지급은 롤백하지 않는다"},
		{CodeAnonymousNotAllowed, http.StatusForbidden, "anonymous 신원은 IAP 금지"},
		{CodeAppPaused, http.StatusForbidden, "kill switch"},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			if got := statusOf(tt.code); got != tt.want {
				t.Errorf("statusOf(%q) = %d, want %d (%s)", tt.code, got, tt.want, tt.why)
			}
		})
	}
}

func TestNewAndWrap(t *testing.T) {
	t.Run("New는 코드에서 상태를 정한다", func(t *testing.T) {
		e := New(CodeRateLimited, "너무 잦아요")
		if e.Status != http.StatusTooManyRequests {
			t.Errorf("Status = %d, want 429", e.Status)
		}
		if e.Code != CodeRateLimited {
			t.Errorf("Code = %q", e.Code)
		}
	})

	t.Run("Wrap은 원인을 보존한다", func(t *testing.T) {
		cause := errors.New("바닥 원인")
		e := Wrap(cause, CodeProviderUnavailable, "마켓 응답 없음")

		if !errors.Is(e, cause) {
			t.Error("errors.Is로 원인을 찾지 못했다")
		}
		if e.Unwrap() != cause {
			t.Error("Unwrap이 원인을 돌려주지 않았다")
		}
	})

	t.Run("등록되지 않은 코드는 500", func(t *testing.T) {
		e := New(Code("존재하지_않는_코드"), "x")
		if e.Status != http.StatusInternalServerError {
			t.Errorf("Status = %d, want 500", e.Status)
		}
	})

	t.Run("WithStatus는 사본을 만든다", func(t *testing.T) {
		orig := New(CodePlatformMismatch, "플랫폼 불일치")
		modified := orig.WithStatus(http.StatusInternalServerError)

		if orig.Status != http.StatusBadRequest {
			t.Errorf("원본이 오염됐다. Status = %d, want 400", orig.Status)
		}
		if modified.Status != http.StatusInternalServerError {
			t.Errorf("사본 Status = %d, want 500", modified.Status)
		}
	})
}

func TestCodeOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Code
	}{
		{"nil은 빈 코드", nil, ""},
		{"플랫폼 에러", New(CodeAuthInvalid, "x"), CodeAuthInvalid},
		{"감싼 플랫폼 에러", fmt.Errorf("바깥: %w", New(CodeAppPaused, "x")), CodeAppPaused},
		{"일반 에러는 internal", errors.New("그냥 에러"), CodeInternal},
		{
			"이중으로 감싸도 찾는다",
			fmt.Errorf("a: %w", fmt.Errorf("b: %w", New(CodeEventBusy, "x"))),
			CodeEventBusy,
		},
		{
			"바깥 플랫폼 에러가 우선한다",
			Wrap(New(CodePurchaseNotFound, "안쪽"), CodeProviderUnavailable, "바깥"),
			CodeProviderUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CodeOf(tt.err); got != tt.want {
				t.Errorf("CodeOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAs(t *testing.T) {
	t.Run("플랫폼 에러면 그대로", func(t *testing.T) {
		orig := New(CodeRateLimited, "x")
		got, ok := As(orig)
		if !ok {
			t.Error("ok = false, want true")
		}
		if got != orig {
			t.Error("같은 포인터를 돌려주지 않았다")
		}
	})

	t.Run("일반 에러는 internal로 감싼다", func(t *testing.T) {
		cause := errors.New("바닥")
		got, ok := As(cause)
		if ok {
			t.Error("ok = true, want false")
		}
		if got.Code != CodeInternal {
			t.Errorf("Code = %q, want internal", got.Code)
		}
		if !errors.Is(got, cause) {
			t.Error("원인이 보존되지 않았다")
		}
	})
}

func TestIsRetryable(t *testing.T) {
	retryable := []Code{
		CodeEventBusy, CodeProviderTimeout, CodeProviderUnavailable,
		CodePurchaseNotFound, CodeProviderCompletionPending,
	}
	for _, c := range retryable {
		if !IsRetryable(c) {
			t.Errorf("IsRetryable(%q) = false, want true", c)
		}
	}

	// 4xx는 다시 보내도 결과가 같다
	permanent := []Code{
		CodeRequestInvalid, CodeAuthInvalid, CodeProductTypeMismatch,
		CodePurchaseOwnedByAnotherUser, CodeAnonymousNotAllowed,
	}
	for _, c := range permanent {
		if IsRetryable(c) {
			t.Errorf("IsRetryable(%q) = true, want false", c)
		}
	}
}

func TestIs(t *testing.T) {
	a := New(CodeAuthInvalid, "메시지 A")
	b := New(CodeAuthInvalid, "메시지 B")
	c := New(CodeAppPaused, "다른 코드")

	if !errors.Is(a, b) {
		t.Error("같은 코드인데 Is가 false")
	}
	if errors.Is(a, c) {
		t.Error("다른 코드인데 Is가 true")
	}
}

// parseDeclaredCodes는 소스 파일에서 Code 타입 상수의 문자열 값을 뽑는다.
func parseDeclaredCodes(t *testing.T, filename string) []string {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("%s 파싱 실패: %v", filename, err)
	}

	var codes []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			// `CodeX Code = "x"` 형태만 본다.
			if ident, ok := vs.Type.(*ast.Ident); !ok || ident.Name != "Code" {
				continue
			}
			for _, val := range vs.Values {
				lit, ok := val.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("문자열 리터럴 해석 실패 %s: %v", lit.Value, err)
				}
				codes = append(codes, s)
			}
		}
	}
	return codes
}
