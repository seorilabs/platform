package main

import "testing"

func TestNewAuditRowPromotesIndexedAdminFields(t *testing.T) {
	detail := map[string]any{
		"actor":      "syous",
		"request_id": "request-1",
		"reason":     "internal_validation",
	}
	row := newAuditRow("iap.operator_grant", "app-a", "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV", "ok", detail)
	if row.Actor != "syous" || row.RequestID != "request-1" {
		t.Fatalf("indexed fields = actor %q, requestID %q", row.Actor, row.RequestID)
	}
	if row.Detail["reason"] != "internal_validation" {
		t.Errorf("detail = %v", row.Detail)
	}
}

func TestNewAuditRowIgnoresNonStringIndexedValues(t *testing.T) {
	row := newAuditRow("iap.verified", "app-a", "", "ok", map[string]any{
		"actor":      1,
		"request_id": true,
	})
	if row.Actor != "" || row.RequestID != "" {
		t.Fatalf("non-string detail promoted: %+v", row)
	}
}
