package play

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/refundreview"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

func TestReviewRefundSendsOnlyRequiredEvidence(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		wantPath := "/androidpublisher/v3/applications/" + testPackage +
			"/orders/GPA.1234-5678:reviewrefund"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	verifier, err := New(testPackage, server.Client(), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	err = verifier.ReviewRefund(t.Context(), refundreview.Submission{
		PackageName: testPackage, OrderID: "GPA.1234-5678",
		PendingRefundToken: "pending-secret", RefundPreference: "DECLINE",
		SampleContentProvided: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got["pendingRefundToken"] != "pending-secret" ||
		got["refundPreference"] != "DECLINE" || got["sampleContentProvided"] != true {
		t.Fatalf("body = %#v", got)
	}
	for _, forbidden := range []string{
		"consumptionPercentageMilliunits", "consumptionUsageEvents", "ipAddress", "location",
	} {
		if _, exists := got[forbidden]; exists {
			t.Fatalf("선택 증거 %q가 전송됐다: %#v", forbidden, got)
		}
	}
}

func TestReviewRefundRejectsPackageOrPreferenceMismatch(t *testing.T) {
	verifier, err := New(testPackage, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	tests := []refundreview.Submission{
		{PackageName: "com.other.app", OrderID: "order", PendingRefundToken: "token", RefundPreference: "DECLINE"},
		{PackageName: testPackage, OrderID: "order", PendingRefundToken: "token", RefundPreference: "MAYBE"},
		{PackageName: testPackage, OrderID: "", PendingRefundToken: "token", RefundPreference: "APPROVE"},
	}
	for _, input := range tests {
		if err := verifier.ReviewRefund(t.Context(), input); platformerr.CodeOf(err) != platformerr.CodeRequestInvalid {
			t.Fatalf("input=%#v code=%s", input, platformerr.CodeOf(err))
		}
	}
}

func TestReviewRefundEscapesOrderID(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	verifier, err := New(testPackage, server.Client(), WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.ReviewRefund(t.Context(), refundreview.Submission{
		PackageName: testPackage, OrderID: "order/with/slash",
		PendingRefundToken: "token", RefundPreference: "NEUTRAL",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "order%2Fwith%2Fslash:reviewrefund") {
		t.Fatalf("escaped path = %q", path)
	}
}
