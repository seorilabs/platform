//go:build integration

package ledger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

func uniqueOperatorPUID() string {
	return fmt.Sprintf("pu_%026d", time.Now().UnixNano())
}

func TestOperatorGrantIsAtomicAndPayloadBound(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	ctx := context.Background()

	req := validOperatorInput()
	req.RequestID = uniqueID("operator-grant")
	req.PlatformUserID = uniqueOperatorPUID()
	res, err := l.OperatorGrant(ctx, req)
	if err != nil || !res.Applied {
		t.Fatalf("최초 지급 = %+v, err=%v", res, err)
	}
	res, err = l.OperatorGrant(ctx, req)
	if err != nil || res.Applied {
		t.Fatalf("멱등 재요청 = %+v, err=%v", res, err)
	}

	changed := req
	changed.Reason = AdminReasonIncidentRecovery
	if _, err := l.OperatorGrant(ctx, changed); platformerr.CodeOf(err) != platformerr.CodeOperatorReplayMismatch {
		t.Fatalf("다른 payload code=%q", platformerr.CodeOf(err))
	}

	// entitlement 경로 오류와 감사 레코드 생성이 같은 트랜잭션이라면,
	// 실패 뒤 같은 requestId를 올바른 payload로 재사용할 수 있다.
	bad := req
	bad.RequestID = uniqueID("operator-atomic")
	bad.EntitlementID = "bad/entitlement"
	if _, err := l.OperatorGrant(ctx, bad); err == nil {
		t.Fatal("깨진 entitlement 경로가 통과했다")
	}
	bad.EntitlementID = "sp_a"
	if res, err := l.OperatorGrant(ctx, bad); err != nil || !res.Applied {
		t.Fatalf("실패가 requestId를 오염시켰다: res=%+v err=%v", res, err)
	}
}

func TestOperatorRevokeTargetsOnlyOperatorGrantSource(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	puid := uniqueOperatorPUID()

	// 같은 entitlement의 실제 마켓 source를 먼저 둔다.
	market := testPurchase(uniqueID("market-token"), domain.StateActive, now)
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid, EntitlementID: "sp_a", Purchase: market,
	}); err != nil {
		t.Fatalf("마켓 지급 실패: %v", err)
	}

	grant := validOperatorInput()
	grant.RequestID = uniqueID("operator-grant")
	grant.PlatformUserID = puid
	if _, err := l.OperatorGrant(ctx, grant); err != nil {
		t.Fatalf("운영 지급 실패: %v", err)
	}

	revoke := validOperatorInput()
	revoke.RequestID = uniqueID("operator-revoke")
	revoke.GrantRequestID = grant.RequestID
	revoke.PlatformUserID = puid
	res, err := l.OperatorRevoke(ctx, revoke)
	if err != nil || !res.Applied {
		t.Fatalf("회수 = %+v, err=%v", res, err)
	}
	// operator source만 revoked고 마켓 source는 남아 entitlement가 active다.
	if len(res.Entitlements) != 1 || res.Entitlements[0] != "sp_a" {
		t.Fatalf("마켓 source까지 회수됐다: %v", res.Entitlements)
	}

	records, err := l.ListOperatorRevocations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range records {
		if rec.RequestID == revoke.RequestID {
			found = true
			if rec.GrantRequestID != grant.RequestID {
				t.Errorf("grantRequestId=%q", rec.GrantRequestID)
			}
		}
	}
	if !found {
		t.Error("회수 감사 레코드를 찾지 못했다")
	}
}

func TestOperatorRequestIDIsUniqueAcrossGrantAndRevoke(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	ctx := context.Background()
	puid := uniqueOperatorPUID()

	grant := validOperatorInput()
	grant.RequestID = uniqueID("cross-grant")
	grant.PlatformUserID = puid
	if _, err := l.OperatorGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	revokeWithGrantID := grant
	revokeWithGrantID.GrantRequestID = grant.RequestID
	revokeWithGrantID.Reason = AdminReasonIncorrectGrantCorrection
	if _, err := l.OperatorRevoke(ctx, revokeWithGrantID); platformerr.CodeOf(err) != platformerr.CodeOperatorReplayMismatch {
		t.Fatalf("grant requestId를 revoke에 재사용한 code=%q", platformerr.CodeOf(err))
	}

	revoke := grant
	revoke.RequestID = uniqueID("cross-revoke")
	revoke.GrantRequestID = grant.RequestID
	revoke.Reason = AdminReasonIncorrectGrantCorrection
	if _, err := l.OperatorRevoke(ctx, revoke); err != nil {
		t.Fatal(err)
	}
	grantWithRevokeID := grant
	grantWithRevokeID.RequestID = revoke.RequestID
	if _, err := l.OperatorGrant(ctx, grantWithRevokeID); platformerr.CodeOf(err) != platformerr.CodeOperatorReplayMismatch {
		t.Fatalf("revoke requestId를 grant에 재사용한 code=%q", platformerr.CodeOf(err))
	}
}

func TestAdminMutationRateGateIsDurable(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	ctx := context.Background()
	principal := uniqueID("admin-principal") + "@example.com"

	for i := 0; i < adminMutationsPerMinute; i++ {
		if err := l.CheckAdminMutationRate(ctx, principal); err != nil {
			t.Fatalf("%d번째 허용 요청 실패: %v", i+1, err)
		}
	}
	if err := l.CheckAdminMutationRate(ctx, principal); platformerr.CodeOf(err) != platformerr.CodeRateLimited {
		t.Fatalf("한도 초과 code=%q, want rate_limited", platformerr.CodeOf(err))
	}
}
