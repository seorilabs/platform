package ledger

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

func validOperatorInput() OperatorInput {
	return OperatorInput{
		RequestID:      "request-1",
		PlatformUserID: "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		EntitlementID:  "sp_a",
		ActorLogin:     "operator",
		Reason:         AdminReasonCustomerSupportCompensation,
		AppID:          "app-a",
	}
}

func TestOperatorInputValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OperatorInput)
	}{
		{"requestId 없음", func(in *OperatorInput) { in.RequestID = "" }},
		{"사용자 없음", func(in *OperatorInput) { in.PlatformUserID = "" }},
		{"entitlement 없음", func(in *OperatorInput) { in.EntitlementID = "" }},
		{"운영자 없음", func(in *OperatorInput) { in.ActorLogin = "" }},
		{"이메일 원문 실행자", func(in *OperatorInput) { in.ActorLogin = "operator@example.com" }},
		{"사유 없음", func(in *OperatorInput) { in.Reason = "" }},
		{"자유 서술 사유", func(in *OperatorInput) { in.Reason = "person@example.com" }},
		{"앱 없음", func(in *OperatorInput) { in.AppID = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validOperatorInput()
			tt.mutate(&in)
			if code := platformerr.CodeOf(in.validate()); code != platformerr.CodeRequestInvalid {
				t.Errorf("code = %q, want request_invalid", code)
			}
		})
	}
}

func TestSameOperatorRequestRejectsChangedPayload(t *testing.T) {
	base := validOperatorInput()
	doc := newOperatorDoc(base, time.Unix(1, 0).UTC())
	if !sameOperatorRequest(doc, base) {
		t.Fatal("같은 요청을 다르다고 판정했다")
	}

	tests := []struct {
		name   string
		mutate func(*OperatorInput)
	}{
		{"grantRequestId", func(in *OperatorInput) { in.GrantRequestID = "grant-1" }},
		{"사용자", func(in *OperatorInput) { in.PlatformUserID = "pu_other" }},
		{"상품", func(in *OperatorInput) { in.EntitlementID = "sp_b" }},
		{"운영자", func(in *OperatorInput) { in.ActorLogin = "other" }},
		{"사유", func(in *OperatorInput) { in.Reason = AdminReasonIncidentRecovery }},
		{"앱", func(in *OperatorInput) { in.AppID = "app-b" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := base
			tt.mutate(&changed)
			if sameOperatorRequest(doc, changed) {
				t.Error("같은 requestId의 다른 payload를 허용했다")
			}
		})
	}
}

func TestOperatorPurchaseUsesGrantRequestAsSourceKey(t *testing.T) {
	now := time.Unix(10, 0).UTC()
	p := operatorPurchase("grant-1", "sp_a", domain.StateActive, now)
	if p.Platform != domain.PlatformOperator || p.CanonicalID != "grant-1" || p.ProductID != "sp_a" {
		t.Errorf("purchase = %+v", p)
	}
	if p.Key() != domain.OrderKey(domain.PlatformOperator, "grant-1") {
		t.Error("operator source가 grantRequestId로 고정되지 않았다")
	}
}

func TestInvalidOperatorFieldsNamesTheViolation(t *testing.T) {
	base := newOperatorDoc(validOperatorInput(), time.Unix(1, 0).UTC())
	if f := invalidOperatorFields(base, "grant"); len(f) > 0 {
		t.Fatalf("정상 지급 기록을 거부했다: %v", f)
	}
	revoke := base
	revoke.GrantRequestID = "grant-1"
	if f := invalidOperatorFields(revoke, "revoke"); len(f) > 0 {
		t.Fatalf("정상 회수 기록을 거부했다: %v", f)
	}

	// 어긴 필드 "이름"까지 고정한다. 원장 접근 권한이 없는 운영자에게는
	// 이 이름이 유일한 진단 단서다.
	tests := []struct {
		name   string
		kind   string
		mutate func(*operatorDoc)
		want   string
	}{
		{"requestId PII", "grant", func(doc *operatorDoc) { doc.RequestID = "person@example.com" }, "requestId"},
		{"grantRequestId PII", "revoke", func(doc *operatorDoc) { doc.GrantRequestID = "person@example.com" }, "grantRequestId"},
		{"PUID PII", "grant", func(doc *operatorDoc) { doc.PlatformUserID = "person@example.com" }, "platformUserId"},
		{"entitlement PII", "grant", func(doc *operatorDoc) { doc.EntitlementID = "person@example.com" }, "entitlementId"},
		{"actor 이메일", "grant", func(doc *operatorDoc) { doc.ActorLogin = "person@example.com" }, "actorLogin"},
		{"reason 자유 서술", "grant", func(doc *operatorDoc) { doc.Reason = "customer asked" }, "reason"},
		{"appId PII", "grant", func(doc *operatorDoc) { doc.AppID = "person@example.com" }, "appId"},
		{"appId 없음", "grant", func(doc *operatorDoc) { doc.AppID = "" }, "appId"},
		{"createdAt 없음", "grant", func(doc *operatorDoc) { doc.CreatedAt = time.Time{} }, "createdAt"},
		{"지급에 grantRequestId", "grant", func(doc *operatorDoc) { doc.GrantRequestID = "grant-1" }, "grantRequestId"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := base
			if tt.kind == "revoke" {
				doc.GrantRequestID = "grant-1"
			}
			tt.mutate(&doc)

			fields := invalidOperatorFields(doc, tt.kind)
			if len(fields) == 0 {
				t.Fatal("브라우저 응답에 부적합한 기록을 허용했다")
			}
			if !slices.Contains(fields, tt.want) {
				t.Errorf("어긴 필드로 %q를 지목하지 않았다: %v", tt.want, fields)
			}
		})
	}
}

// 로그가 값을 흘리면 fail-closed의 의미가 없어진다. 자유 서술 사유와
// 이메일 원문 actor를 브라우저에서 막아 놓고 로그로 내보내면 같은 값이
// Cloud Logging에 그대로 남는다.
func TestInvalidOperatorFieldsLeaksNoValues(t *testing.T) {
	doc := newOperatorDoc(validOperatorInput(), time.Unix(1, 0).UTC())
	doc.ActorLogin = "person@example.com"
	doc.Reason = "고객이 환불을 요청했고 전화번호는 010-0000-0000"

	joined := strings.Join(invalidOperatorFields(doc, "grant"), ",")
	if strings.Contains(joined, "person@example.com") ||
		strings.Contains(joined, "010-0000-0000") ||
		strings.Contains(joined, "고객이") {
		t.Errorf("어긴 값이 필드 목록에 섞였다: %s", joined)
	}
}

func TestSafeDocIDHidesOffContractIDs(t *testing.T) {
	if got := safeDocID("req-01JABCDE"); got != "req-01JABCDE" {
		t.Errorf("정상 ID를 가렸다: %s", got)
	}
	// 계약 밖 ID는 무엇이 들어 있을지 알 수 없으므로 원문을 남기지 않는다.
	got := safeDocID("person@example.com")
	if strings.Contains(got, "person@example.com") {
		t.Errorf("계약 밖 ID 원문이 로그로 나간다: %s", got)
	}
	if !strings.Contains(got, "18") {
		t.Errorf("길이 단서가 없다: %s", got)
	}
}
