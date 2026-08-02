package ledger

import (
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
