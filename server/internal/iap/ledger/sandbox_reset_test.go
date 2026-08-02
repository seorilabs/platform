package ledger

import (
	"math"
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
			got, err := blockingSandboxResetAt(tt.env, tt.order, sandboxResetBarrierDoc{}, tt.p)

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

// 불변식 3, 5, 9: reset transaction이 보지 못한 지연 구매도 사용자 barrier가
// 차단한다. barrier는 sandbox 원장에 영구 문서로 남고 production에는 영향을
// 주지 않는다.
func TestBlockingSandboxResetAtUsesUserBarrier(t *testing.T) {
	resetAt := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	barrier := sandboxResetBarrierDoc{
		LastCompletedRequestID: "reset-1",
		LastCompletedResetAt:   resetAt,
	}
	purchase := domain.VerifiedPurchase{
		Platform:    domain.PlatformAppStore,
		PurchasedAt: resetAt.Add(-time.Minute),
		State:       domain.StateActive,
	}

	got, err := blockingSandboxResetAt(domain.EnvSandbox, orderDoc{}, barrier, purchase)
	if err != nil {
		t.Fatalf("barrier 판정 실패: %v", err)
	}
	if !got.Equal(resetAt) {
		t.Fatalf("resetAt = %v, want %v", got, resetAt)
	}

	got, err = blockingSandboxResetAt(domain.EnvProduction, orderDoc{}, barrier, purchase)
	if err != nil || !got.IsZero() {
		t.Fatalf("production barrier got=%v err=%v, want zero", got, err)
	}
}

func TestBlockingSandboxResetAtUsesPreviousOwnerActiveBarrier(t *testing.T) {
	completedAt := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	activeAt := completedAt.Add(time.Hour)
	newOwnerBarrier := sandboxResetBarrierDoc{
		LastCompletedRequestID: "reset-new-owner",
		LastCompletedResetAt:   completedAt,
	}
	previousOwnerBarrier := sandboxResetBarrierDoc{
		ActiveRequestID: "reset-previous-owner",
		ActiveResetAt:   activeAt,
		ActiveStartedAt: activeAt,
	}
	purchase := domain.VerifiedPurchase{
		Platform: domain.PlatformAppStore, PurchasedAt: activeAt.Add(-time.Minute),
		State: domain.StateActive,
	}

	got, err := blockingSandboxResetAt(
		domain.EnvSandbox, orderDoc{}, newOwnerBarrier, purchase, previousOwnerBarrier,
	)
	if err != nil || !got.Equal(activeAt) {
		t.Fatalf("이전 소유자 active barrier got=%v err=%v", got, err)
	}
}

func TestSandboxResetBarrierValidationFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	base := sandboxResetBarrierDoc{
		Revision: 1, ActiveRequestID: "reset-active", ActiveResetAt: now,
		ActiveStartedAt: now, LastCompletedRequestID: "reset-completed",
		LastCompletedResetAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	if !validSandboxResetBarrier(base) {
		t.Fatal("정상 barrier를 거부했다")
	}
	for _, tt := range []struct {
		name   string
		mutate func(*sandboxResetBarrierDoc)
	}{
		{"revision 없음", func(doc *sandboxResetBarrierDoc) { doc.Revision = 0 }},
		{"active request만 있음", func(doc *sandboxResetBarrierDoc) { doc.ActiveResetAt = time.Time{} }},
		{"active startedAt 없음", func(doc *sandboxResetBarrierDoc) { doc.ActiveStartedAt = time.Time{} }},
		{"completed cutoff만 있음", func(doc *sandboxResetBarrierDoc) { doc.LastCompletedRequestID = "" }},
		{"active cutoff 역행", func(doc *sandboxResetBarrierDoc) {
			doc.ActiveResetAt = doc.LastCompletedResetAt.Add(-time.Second)
		}},
		{"requestId PII", func(doc *sandboxResetBarrierDoc) { doc.ActiveRequestID = "person@example.com" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := base
			tt.mutate(&doc)
			if validSandboxResetBarrier(doc) {
				t.Error("손상된 barrier를 허용했다")
			}
		})
	}
}

// 불변식 5: 소유권 이전의 복구 증거는 토큰·영수증 없이 최소 필드만
// append-only 원장에 저장한다.
func TestOwnershipTransferEvidenceContainsRecoverableMinimum(t *testing.T) {
	now := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)
	purchase := domain.VerifiedPurchase{
		Platform:   domain.PlatformAppStore,
		State:      domain.StateRevoked,
		ObservedAt: now.Add(-time.Minute),
		// 저장 금지 값이 evidence에 들어가지 않는지 아래에서 구조 자체로 확인한다.
		CanonicalID:       "receipt-token-must-not-be-stored",
		ProviderOrderID:   "provider-order-must-not-be-stored",
		PlatformAccountID: "market-account-must-not-be-stored",
	}
	doc := newOwnershipTransferDoc(
		strings.Repeat("a", 64), 2,
		"pu_01ARZ3NDEKTSV4RRFFQ69G5FAV", "pu_01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"sp_galaxy_gecko", purchase, now,
	)

	if doc.Sequence != 2 || doc.FromPlatformUserID == "" || doc.ToPlatformUserID == "" ||
		doc.EntitlementID != "sp_galaxy_gecko" || doc.Platform != domain.PlatformAppStore ||
		doc.State != domain.StateRevoked || !doc.CreatedAt.Equal(now) {
		t.Fatalf("복구 증거가 불완전하다: %+v", doc)
	}
}

// 불변식 5: append-only sequence와 barrier revision은 음수나 overflow로
// 되감기면 기존 evidence ID를 다시 가리킬 수 있으므로 fail-closed한다.
func TestNextLedgerSequenceFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name    string
		current int64
		want    int64
		wantErr bool
	}{
		{name: "최초", current: 0, want: 1},
		{name: "다음", current: 41, want: 42},
		{name: "음수", current: -1, wantErr: true},
		{name: "overflow", current: math.MaxInt64, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextLedgerSequence(tt.current)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("nextLedgerSequence(%d)=(%d,%v), want=(%d,err=%v)",
					tt.current, got, err, tt.want, tt.wantErr)
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

func TestSandboxResetClosurePayloadBinding(t *testing.T) {
	base := SandboxResetClosureInput{
		RequestID: "reset-close-1", AppID: "app-a", ActorLogin: "operator",
	}
	if err := base.validate(); err != nil {
		t.Fatalf("유효한 closure 입력 거부: %v", err)
	}
	closedAt := time.Unix(1, 0).UTC()
	doc := sandboxResetClosureDoc{
		SchemaVersion: sandboxResetClosureVersion,
		RequestID:     base.RequestID,
		AppID:         base.AppID,
		ActorLogin:    base.ActorLogin,
		ClosedAt:      closedAt,
	}
	if !validSandboxResetClosure(doc) || !sameSandboxResetClosure(doc, base) {
		t.Fatal("정상 closure 또는 exact replay fingerprint를 거부했다")
	}

	changedActor := base
	changedActor.ActorLogin = "other-operator"
	if sameSandboxResetClosure(doc, changedActor) {
		t.Fatal("같은 requestId의 다른 actor를 exact closure replay로 허용했다")
	}
	changedApp := base
	changedApp.AppID = "app-b"
	if sameSandboxResetClosure(doc, changedApp) {
		t.Fatal("같은 requestId의 다른 app을 exact closure replay로 허용했다")
	}

	for _, tt := range []struct {
		name   string
		mutate func(*sandboxResetClosureDoc)
	}{
		{"schema 누락", func(doc *sandboxResetClosureDoc) { doc.SchemaVersion = 0 }},
		{"requestId PII", func(doc *sandboxResetClosureDoc) { doc.RequestID = "person@example.com" }},
		{"appId 선행 하이픈", func(doc *sandboxResetClosureDoc) { doc.AppID = "-app" }},
		{"actor 이메일", func(doc *sandboxResetClosureDoc) { doc.ActorLogin = "person@example.com" }},
		{"closedAt 없음", func(doc *sandboxResetClosureDoc) { doc.ClosedAt = time.Time{} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changed := doc
			tt.mutate(&changed)
			if validSandboxResetClosure(changed) {
				t.Error("영구 보존에 부적합한 closure를 허용했다")
			}
		})
	}
}

func TestValidSandboxResetIntentFailsClosed(t *testing.T) {
	preparedAt := time.Unix(1, 0).UTC()
	base := sandboxResetRequestDoc{
		SchemaVersion:   sandboxResetSchemaVersion,
		RequestID:       "reset-1",
		PlatformUserID:  "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		AppID:           "app-a",
		ActorLogin:      "operator",
		Reason:          AdminReasonInternalValidation,
		ResetAt:         preparedAt,
		PreparedAt:      preparedAt,
		BarrierRevision: 1,
	}
	if !validSandboxResetIntent(base) {
		t.Fatal("정상 sandbox reset intent를 거부했다")
	}
	for _, tt := range []struct {
		name   string
		mutate func(*sandboxResetRequestDoc)
	}{
		{"requestId PII", func(doc *sandboxResetRequestDoc) { doc.RequestID = "person@example.com" }},
		{"PUID PII", func(doc *sandboxResetRequestDoc) { doc.PlatformUserID = "person@example.com" }},
		{"actor 이메일", func(doc *sandboxResetRequestDoc) { doc.ActorLogin = "person@example.com" }},
		{"reason 자유 서술", func(doc *sandboxResetRequestDoc) { doc.Reason = "customer asked" }},
		{"schema 누락", func(doc *sandboxResetRequestDoc) { doc.SchemaVersion = 0 }},
		{"resetAt 없음", func(doc *sandboxResetRequestDoc) { doc.ResetAt = time.Time{} }},
		{"preparedAt 없음", func(doc *sandboxResetRequestDoc) { doc.PreparedAt = time.Time{} }},
		{"barrier revision 없음", func(doc *sandboxResetRequestDoc) { doc.BarrierRevision = 0 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := base
			tt.mutate(&doc)
			if validSandboxResetIntent(doc) {
				t.Error("영구 보존에 부적합한 reset intent를 허용했다")
			}
		})
	}
}

func TestValidSandboxResetCompletionFailsClosed(t *testing.T) {
	preparedAt := time.Unix(1, 0).UTC()
	intent := sandboxResetRequestDoc{
		SchemaVersion: sandboxResetSchemaVersion, RequestID: "reset-1",
		PlatformUserID: "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV", AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
		ResetAt: preparedAt.Add(time.Second), PreparedAt: preparedAt, BarrierRevision: 1,
	}
	base := sandboxResetCompletionDoc{
		SchemaVersion: sandboxResetSchemaVersion, RequestID: intent.RequestID,
		PlatformUserID: intent.PlatformUserID, AppID: intent.AppID,
		OrderKeys: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)},
		ResetAt:   intent.ResetAt, CompletedAt: intent.ResetAt.Add(time.Second), BarrierRevision: 2,
	}
	if !validSandboxResetCompletion(base, intent) {
		t.Fatal("정상 sandbox reset completion을 거부했다")
	}
	for _, tt := range []struct {
		name   string
		mutate func(*sandboxResetCompletionDoc)
	}{
		{"intent 대상 불일치", func(doc *sandboxResetCompletionDoc) { doc.AppID = "app-b" }},
		{"orderKey PII", func(doc *sandboxResetCompletionDoc) { doc.OrderKeys = []string{"person@example.com"} }},
		{"orderKey 중복", func(doc *sandboxResetCompletionDoc) {
			doc.OrderKeys = []string{strings.Repeat("a", 64), strings.Repeat("a", 64)}
		}},
		{"completion revision 역행", func(doc *sandboxResetCompletionDoc) { doc.BarrierRevision = 1 }},
		{"completedAt 없음", func(doc *sandboxResetCompletionDoc) { doc.CompletedAt = time.Time{} }},
		{"completedAt cutoff 이전", func(doc *sandboxResetCompletionDoc) { doc.CompletedAt = preparedAt }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := base
			doc.OrderKeys = append([]string{}, base.OrderKeys...)
			tt.mutate(&doc)
			if validSandboxResetCompletion(doc, intent) {
				t.Error("intent와 맞지 않는 completion을 허용했다")
			}
		})
	}
}
