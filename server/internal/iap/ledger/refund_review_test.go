package ledger

import (
	"testing"
	"time"
)

func TestRefundReviewLeaseActiveOnlyBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	if !refundReviewLeaseActive(refundReviewDoc{
		LeaseID: "lease", ClaimExpiresAt: now.Add(time.Second),
	}, now) {
		t.Fatal("진행 중인 Google 호출 lease를 만료 sweep에서 보호하지 않았다")
	}
	if refundReviewLeaseActive(refundReviewDoc{
		LeaseID: "lease", ClaimExpiresAt: now,
	}, now) {
		t.Fatal("만료된 lease를 계속 활성 상태로 보았다")
	}
	if refundReviewLeaseActive(refundReviewDoc{ClaimExpiresAt: now.Add(time.Hour)}, now) {
		t.Fatal("lease ID 없는 시각만으로 활성 상태로 보았다")
	}
}
