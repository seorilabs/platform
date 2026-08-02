package ledger

import (
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// 초기화 차단 판정은 결제 경로에 직접 닿는다.
//
// production 원장이나 다른 마켓이 이 판정에 들어오면 실사용자 구매가
// 조용히 회수된다. 그 조합을 하나씩 못 박아 둔다.
func TestBlockingSandboxResetAt(t *testing.T) {
	resetAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	before := resetAt.Add(-time.Hour)
	after := resetAt.Add(time.Hour)

	marked := func() orderDoc {
		return orderDoc{SandboxReset: &sandboxResetMark{RequestID: "req-1", ResetAt: resetAt}}
	}
	applePurchase := func(purchasedAt time.Time) domain.VerifiedPurchase {
		return domain.VerifiedPurchase{
			Platform:    domain.PlatformAppStore,
			PurchasedAt: purchasedAt,
			State:       domain.StateActive,
		}
	}

	tests := []struct {
		name    string
		env     domain.Environment
		order   orderDoc
		p       domain.VerifiedPurchase
		want    time.Time
		wantErr platformerr.Code
	}{
		{
			name:  "초기화 이전 거래는 차단한다",
			env:   domain.EnvSandbox,
			order: marked(),
			p:     applePurchase(before),
			want:  resetAt,
		},
		{
			name:  "초기화 시각과 같으면 차단한다",
			env:   domain.EnvSandbox,
			order: marked(),
			p:     applePurchase(resetAt),
			want:  resetAt,
		},
		{
			name:  "초기화 이후 거래는 진짜 새 구매다",
			env:   domain.EnvSandbox,
			order: marked(),
			p:     applePurchase(after),
		},
		{
			// 이게 뚫리면 실사용자 결제가 회수된다.
			name:  "production 원장은 표식이 있어도 통과시킨다",
			env:   domain.EnvProduction,
			order: marked(),
			p:     applePurchase(before),
		},
		{
			name:  "Play 구매는 표식이 있어도 통과시킨다",
			env:   domain.EnvSandbox,
			order: marked(),
			p: domain.VerifiedPurchase{
				Platform:    domain.PlatformGooglePlay,
				PurchasedAt: before,
				State:       domain.StateActive,
			},
		},
		{
			name:  "AIT 구매는 표식이 있어도 통과시킨다",
			env:   domain.EnvSandbox,
			order: marked(),
			p: domain.VerifiedPurchase{
				Platform:    domain.PlatformAppsInToss,
				PurchasedAt: before,
				State:       domain.StateActive,
			},
		},
		{
			name:  "표식이 없으면 판정하지 않는다",
			env:   domain.EnvSandbox,
			order: orderDoc{},
			p:     applePurchase(before),
		},
		{
			name:  "표식의 시각이 비어 있으면 판정하지 않는다",
			env:   domain.EnvSandbox,
			order: orderDoc{SandboxReset: &sandboxResetMark{RequestID: "req-1"}},
			p:     applePurchase(before),
		},
		{
			// 모른 채 통과시키면 초기화한 거래를 다시 지급하게 된다.
			name:    "구매 시각을 모르면 거부한다",
			env:     domain.EnvSandbox,
			order:   marked(),
			p:       applePurchase(time.Time{}),
			wantErr: platformerr.CodeProviderResponseInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := blockingSandboxResetAt(tt.env, tt.order, tt.p)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("에러를 기대했는데 got = %v", got)
				}
				if code := platformerr.CodeOf(err); code != tt.wantErr {
					t.Errorf("code = %v, want %v", code, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("예상치 못한 에러: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("got = %v, want %v", got, tt.want)
			}
		})
	}
}

// 차단 결과는 지급도 중복 지급도 아니다.
//
// 불변식 2를 그대로 적용하면 차단 결과가 런타임 검사에서 튕긴다.
func TestGrantResultValidAllowsSandboxBlock(t *testing.T) {
	tests := []struct {
		name string
		res  domain.GrantResult
		want bool
	}{
		{
			name: "차단은 둘 다 false여야 한다",
			res:  domain.GrantResult{BlockedBySandboxReset: true},
			want: true,
		},
		{
			name: "차단인데 지급했다고 하면 모순이다",
			res:  domain.GrantResult{BlockedBySandboxReset: true, Granted: true},
			want: false,
		},
		{
			name: "차단인데 이미 지급했다고 하면 모순이다",
			res:  domain.GrantResult{BlockedBySandboxReset: true, AlreadyGranted: true},
			want: false,
		},
		{
			name: "평소에는 여전히 배타적이어야 한다",
			res:  domain.GrantResult{},
			want: false,
		},
		{
			name: "정상 지급",
			res:  domain.GrantResult{Granted: true},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.res.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}
func TestSandboxResetInputAndPayloadBinding(t *testing.T) {
	base := SandboxResetInput{
		RequestID:      "reset-1",
		PlatformUserID: "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		AppID:          "app-a",
		ActorLogin:     "operator",
		Reason:         AdminReasonInternalValidation,
	}
	if err := base.validate(); err != nil {
		t.Fatalf("유효한 입력 거부: %v", err)
	}
	doc := sandboxResetRequestDoc{
		RequestID: base.RequestID, PlatformUserID: base.PlatformUserID,
		AppID: base.AppID, ActorLogin: base.ActorLogin, Reason: base.Reason,
	}
	if !sameSandboxResetRequest(doc, base) {
		t.Fatal("같은 reset payload를 다르다고 판정했다")
	}

	tests := []struct {
		name   string
		mutate func(*SandboxResetInput)
	}{
		{"requestId", func(in *SandboxResetInput) { in.RequestID = "" }},
		{"사용자", func(in *SandboxResetInput) { in.PlatformUserID = "" }},
		{"앱", func(in *SandboxResetInput) { in.AppID = "" }},
		{"이메일 actor", func(in *SandboxResetInput) { in.ActorLogin = "person@example.com" }},
		{"자유 서술 reason", func(in *SandboxResetInput) { in.Reason = "테스트 초기화" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := base
			tt.mutate(&changed)
			if code := platformerr.CodeOf(changed.validate()); code != platformerr.CodeRequestInvalid {
				t.Errorf("validation code=%q", code)
			}
			if sameSandboxResetRequest(doc, changed) {
				t.Error("같은 requestId의 다른 reset payload를 허용했다")
			}
		})
	}
}

func TestValidSandboxResetRecordFailsClosed(t *testing.T) {
	base := sandboxResetRequestDoc{
		RequestID:      "reset-1",
		PlatformUserID: "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		AppID:          "app-a",
		ActorLogin:     "operator",
		Reason:         AdminReasonInternalValidation,
		OrderKeys:      []string{strings.Repeat("a", 64), strings.Repeat("b", 64)},
		ResetAt:        time.Unix(1, 0).UTC(),
	}
	if !validSandboxResetRecord(base) {
		t.Fatal("정상 sandbox reset 기록을 거부했다")
	}
	for _, tt := range []struct {
		name   string
		mutate func(*sandboxResetRequestDoc)
	}{
		{"requestId PII", func(doc *sandboxResetRequestDoc) { doc.RequestID = "person@example.com" }},
		{"PUID PII", func(doc *sandboxResetRequestDoc) { doc.PlatformUserID = "person@example.com" }},
		{"actor 이메일", func(doc *sandboxResetRequestDoc) { doc.ActorLogin = "person@example.com" }},
		{"reason 자유 서술", func(doc *sandboxResetRequestDoc) { doc.Reason = "customer asked" }},
		{"orderKey PII", func(doc *sandboxResetRequestDoc) { doc.OrderKeys = []string{"person@example.com"} }},
		{"orderKey 중복", func(doc *sandboxResetRequestDoc) {
			doc.OrderKeys = []string{strings.Repeat("a", 64), strings.Repeat("a", 64)}
		}},
		{"resetAt 없음", func(doc *sandboxResetRequestDoc) { doc.ResetAt = time.Time{} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := base
			doc.OrderKeys = append([]string{}, base.OrderKeys...)
			tt.mutate(&doc)
			if validSandboxResetRecord(doc) {
				t.Error("브라우저 응답에 부적합한 reset 기록을 허용했다")
			}
		})
	}
}
