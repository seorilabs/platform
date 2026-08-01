//go:build integration

// 실제 Firestore에 붙는 통합 테스트다.
//
// Firestore 트랜잭션의 실제 동작을 검증해야 의미가 있어 fake를 쓰지 않는다.
// staging prefix(stg_)와 sandbox 환경으로 이중 격리하므로 production
// 데이터를 건드리지 않는다.
//
//	go test -tags=integration ./internal/iap/ledger/
//
// 필요한 환경변수:
//
//	GOOGLE_CLOUD_PROJECT, GOOGLE_APPLICATION_CREDENTIALS
package ledger

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
)

func newTestLedger(t *testing.T) (*Ledger, func()) {
	t.Helper()

	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		t.Skip("GOOGLE_CLOUD_PROJECT가 없어 건너뛴다")
	}

	// stg_ prefix + sandbox 환경으로 이중 격리한다.
	st, err := store.New(context.Background(), project, "stg_")
	if err != nil {
		t.Fatalf("store 생성 실패: %v", err)
	}

	return New(st, domain.EnvSandbox), func() { st.Close() }
}

// uniqueID는 테스트마다 다른 식별자를 만들어 이전 실행과 섞이지 않게 한다.
func uniqueID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func testPurchase(canonicalID string, state domain.State, observedAt time.Time) domain.VerifiedPurchase {
	return domain.VerifiedPurchase{
		Platform:          domain.PlatformGooglePlay,
		ProductID:         "sp_test_product",
		CanonicalID:       canonicalID,
		ProviderOrderID:   "order-" + canonicalID,
		PlatformAccountID: "account-ref",
		PurchasedAt:       observedAt,
		ObservedAt:        observedAt,
		State:             state,
	}
}

// 불변식 2: 첫 지급은 granted, 재지급은 alreadyGranted. 항상 배타적이다.
func TestGrantIdempotency(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueID("pu_test")
	entID := "sp_galaxy_gecko"
	now := time.Now().UTC().Truncate(time.Second)

	in := GrantInput{
		PlatformUserID: puid,
		EntitlementID:  entID,
		Purchase:       testPurchase(uniqueID("token"), domain.StateActive, now),
	}

	first, err := l.Grant(ctx, in)
	if err != nil {
		t.Fatalf("첫 지급 실패: %v", err)
	}
	if !first.Granted || first.AlreadyGranted {
		t.Errorf("첫 지급: granted=%v alreadyGranted=%v, want true/false",
			first.Granted, first.AlreadyGranted)
	}
	if !first.Valid() {
		t.Error("불변식 2 위반: granted와 alreadyGranted가 배타적이지 않다")
	}

	second, err := l.Grant(ctx, in)
	if err != nil {
		t.Fatalf("재지급 실패: %v", err)
	}
	if second.Granted || !second.AlreadyGranted {
		t.Errorf("재지급: granted=%v alreadyGranted=%v, want false/true",
			second.Granted, second.AlreadyGranted)
	}
	if !second.Valid() {
		t.Error("불변식 2 위반")
	}

	// 활성 목록에 들어 있어야 한다
	list, err := l.ListActive(ctx, puid)
	if err != nil {
		t.Fatalf("목록 조회 실패: %v", err)
	}
	if len(list) != 1 || list[0] != entID {
		t.Errorf("활성 목록 = %v, want [%s]", list, entID)
	}
}

// 불변식 4: 다른 사용자의 구매를 자동으로 옮기지 않는다.
func TestGrantRejectsCrossUser(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	token := uniqueID("token-shared")
	now := time.Now().UTC().Truncate(time.Second)

	owner := uniqueID("pu_owner")
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: owner,
		EntitlementID:  "sp_galaxy_gecko",
		Purchase:       testPurchase(token, domain.StateActive, now),
	}); err != nil {
		t.Fatalf("소유자 지급 실패: %v", err)
	}

	// 다른 사용자가 같은 구매 증명을 제시한다
	other := uniqueID("pu_other")
	_, err := l.Grant(ctx, GrantInput{
		PlatformUserID: other,
		EntitlementID:  "sp_galaxy_gecko",
		Purchase:       testPurchase(token, domain.StateActive, now.Add(time.Minute)),
	})

	if err == nil {
		t.Fatal("다른 사용자에게 같은 구매를 지급했다")
	}
	if code := platformerr.CodeOf(err); code != platformerr.CodePurchaseOwnedByAnotherUser {
		t.Errorf("code = %q, want purchase_owned_by_another_user", code)
	}
}

// 불변식 3: 늦게 도착한 grant가 이미 처리된 환불을 되돌리지 못한다.
func TestStaleGrantDoesNotRevive(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueID("pu_stale")
	entID := "sp_galaxy_gecko"
	token := uniqueID("token-stale")

	early := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	late := early.Add(30 * time.Minute)

	// 먼저 지급
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid,
		EntitlementID:  entID,
		Purchase:       testPurchase(token, domain.StateActive, early),
	}); err != nil {
		t.Fatalf("지급 실패: %v", err)
	}

	// 나중에 환불이 도착
	if err := l.RevokeByCanonicalID(ctx, domain.PlatformGooglePlay, token, late); err != nil {
		t.Fatalf("환불 반영 실패: %v", err)
	}

	list, err := l.ListActive(ctx, puid)
	if err != nil {
		t.Fatalf("목록 조회 실패: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("환불 후에도 활성이다: %v", list)
	}

	// 환불보다 이른 시각의 grant가 뒤늦게 도착한다.
	// 클라이언트 재시도나 오프라인 큐에서 실제로 일어난다.
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid,
		EntitlementID:  entID,
		Purchase:       testPurchase(token, domain.StateActive, early),
	}); err != nil {
		t.Fatalf("stale grant 처리 실패: %v", err)
	}

	list, err = l.ListActive(ctx, puid)
	if err != nil {
		t.Fatalf("목록 조회 실패: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("불변식 3 위반: 늦게 온 grant가 환불을 되돌렸다. 활성 = %v", list)
	}
}

// 불변식 6: entitlement active는 sources의 OR다.
// 환불된 구매와 별개 구매가 함께 있으면 활성이 유지된다.
func TestActiveIsOrOfSources(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueID("pu_or")
	entID := "sp_galaxy_gecko"
	now := time.Now().UTC().Truncate(time.Second)

	tokenA := uniqueID("token-a")
	tokenB := uniqueID("token-b")

	for _, tok := range []string{tokenA, tokenB} {
		if _, err := l.Grant(ctx, GrantInput{
			PlatformUserID: puid,
			EntitlementID:  entID,
			Purchase:       testPurchase(tok, domain.StateActive, now),
		}); err != nil {
			t.Fatalf("지급 실패 %s: %v", tok, err)
		}
	}

	// 하나만 환불한다
	if err := l.RevokeByCanonicalID(ctx, domain.PlatformGooglePlay, tokenA, now.Add(time.Minute)); err != nil {
		t.Fatalf("환불 실패: %v", err)
	}

	list, err := l.ListActive(ctx, puid)
	if err != nil {
		t.Fatalf("목록 조회 실패: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("불변식 6 위반: 남은 구매가 있는데 비활성이다. 활성 = %v", list)
	}

	// 나머지도 환불하면 비활성
	if err := l.RevokeByCanonicalID(ctx, domain.PlatformGooglePlay, tokenB, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("환불 실패: %v", err)
	}
	list, err = l.ListActive(ctx, puid)
	if err != nil {
		t.Fatalf("목록 조회 실패: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("전부 환불했는데 활성이다: %v", list)
	}
}

// 같은 주문 키에 다른 상품이 오면 거부한다.
func TestGrantRejectsReplayMismatch(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueID("pu_replay")
	token := uniqueID("token-replay")
	now := time.Now().UTC().Truncate(time.Second)

	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid,
		EntitlementID:  "sp_galaxy_gecko",
		Purchase:       testPurchase(token, domain.StateActive, now),
	}); err != nil {
		t.Fatalf("지급 실패: %v", err)
	}

	// 같은 토큰인데 상품이 다르다
	bad := testPurchase(token, domain.StateActive, now.Add(time.Minute))
	bad.ProductID = "sp_다른상품"

	_, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid,
		EntitlementID:  "sp_galaxy_gecko",
		Purchase:       bad,
	})
	if err == nil {
		t.Fatal("상품이 바뀐 replay를 통과시켰다")
	}
	if code := platformerr.CodeOf(err); code != platformerr.CodePurchaseReplayMismatch {
		t.Errorf("code = %q, want purchase_replay_mismatch", code)
	}
}

// 소유자를 모르는 환불은 tombstone으로 남고, 나중에 그 구매가 와도 살아나지 않는다.
// 불변식 10: 알림만으로 신규 지급을 하지 않는다.
func TestRevokeUnknownCreatesTombstone(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	token := uniqueID("token-unknown")
	now := time.Now().UTC().Truncate(time.Second)

	// 우리가 모르는 구매의 환불이 먼저 온다
	if err := l.RevokeByCanonicalID(ctx, domain.PlatformGooglePlay, token, now); err != nil {
		t.Fatalf("tombstone 생성 실패: %v", err)
	}

	// 나중에 그 구매의 검증이 도착한다. 환불보다 이른 시각이다.
	puid := uniqueID("pu_tomb")
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid,
		EntitlementID:  "sp_galaxy_gecko",
		Purchase:       testPurchase(token, domain.StateActive, now.Add(-time.Minute)),
	}); err != nil {
		t.Fatalf("지급 처리 실패: %v", err)
	}

	list, err := l.ListActive(ctx, puid)
	if err != nil {
		t.Fatalf("목록 조회 실패: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("환불된 구매가 되살아났다: %v", list)
	}
}

// pending은 확정 상태를 덮지 않는다.
func TestRecordPendingDoesNotOverwriteFinal(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueID("pu_pending")
	entID := "sp_galaxy_gecko"
	token := uniqueID("token-pending")
	now := time.Now().UTC().Truncate(time.Second)

	in := GrantInput{
		PlatformUserID: puid,
		EntitlementID:  entID,
		Purchase:       testPurchase(token, domain.StateActive, now),
	}
	if _, err := l.Grant(ctx, in); err != nil {
		t.Fatalf("지급 실패: %v", err)
	}

	pending := in
	pending.Purchase.State = domain.StatePending
	pending.Purchase.ObservedAt = now.Add(time.Minute)
	if err := l.RecordPending(ctx, pending); err != nil {
		t.Fatalf("pending 기록 실패: %v", err)
	}

	list, err := l.ListActive(ctx, puid)
	if err != nil {
		t.Fatalf("목록 조회 실패: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("pending이 active를 덮었다. 활성 = %v", list)
	}
}

// sandbox 초기화 뒤 같은 거래를 다시 제시해도 지급하지 않는다.
//
// 실기기에서 겪은 상황을 그대로 재현한다. App Store Connect에서
// 구매내역을 지워도 기기에는 미완료 거래가 남아 있어 다음 구매가
// 구매 시트 없이 같은 거래를 돌려준다.
//
// 특히 stale 억제(불변식 3)보다 먼저 판정해야 한다. 한 번 차단해
// revoked가 된 뒤에는 revoked(rank 3) > active(rank 2)라 stale로
// 걸리고, 그러면 alreadyGranted=true가 나가 앱이 받은 적 없는
// 상품을 가진 걸로 안다.
func TestSandboxResetBlocksRepurchase(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueID("pu_sandbox")
	entID := "sp_galaxy_gecko"
	now := time.Now().UTC().Truncate(time.Second)

	purchase := testPurchase(uniqueID("orig-tx"), domain.StateActive, now)
	purchase.Platform = domain.PlatformAppStore
	purchase.Completion = domain.CompletionAppleFinish
	in := GrantInput{PlatformUserID: puid, EntitlementID: entID, Purchase: purchase}

	if _, err := l.Grant(ctx, in); err != nil {
		t.Fatalf("최초 지급 실패: %v", err)
	}

	// 운영자가 App Store sandbox 구매내역을 지웠다.
	if err := l.MarkSandboxReset(ctx, puid, uniqueID("req"), []string{purchase.Key()}); err != nil {
		t.Fatalf("초기화 표식 실패: %v", err)
	}

	list, err := l.ListActive(ctx, puid)
	if err != nil {
		t.Fatalf("목록 조회 실패: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("초기화 후 활성 목록 = %v, want []", list)
	}

	// 기기에 남은 거래를 다시 제시한다. 두 번 눌러도 결과가 같아야 한다.
	for attempt := 1; attempt <= 2; attempt++ {
		res, err := l.Grant(ctx, in)
		if err != nil {
			t.Fatalf("%d번째 재제시 실패: %v", attempt, err)
		}
		if !res.BlockedBySandboxReset {
			t.Fatalf("%d번째: 초기화 이전 거래를 차단하지 않았다", attempt)
		}
		if res.Granted || res.AlreadyGranted {
			t.Errorf("%d번째: granted=%v alreadyGranted=%v, want false/false",
				attempt, res.Granted, res.AlreadyGranted)
		}
		if !res.Valid() {
			t.Errorf("%d번째: 결과가 불변식을 깼다", attempt)
		}
		if len(res.Entitlements) != 0 {
			t.Errorf("%d번째: 차단인데 소유 목록에 %v가 남았다", attempt, res.Entitlements)
		}
	}
}

// 초기화 이후에 산 거래는 진짜 새 구매다. 정상 지급해야 한다.
//
// 차단이 여기까지 번지면 초기화한 테스터는 영영 아무것도 살 수 없다.
func TestSandboxResetAllowsLaterPurchase(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueID("pu_sandbox_new")
	entID := "sp_galaxy_gecko"
	now := time.Now().UTC().Truncate(time.Second)

	canonical := uniqueID("orig-tx")
	old := testPurchase(canonical, domain.StateActive, now.Add(-time.Hour))
	old.Platform = domain.PlatformAppStore
	old.Completion = domain.CompletionAppleFinish

	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid, EntitlementID: entID, Purchase: old,
	}); err != nil {
		t.Fatalf("최초 지급 실패: %v", err)
	}
	if err := l.MarkSandboxReset(ctx, puid, uniqueID("req"), []string{old.Key()}); err != nil {
		t.Fatalf("초기화 표식 실패: %v", err)
	}

	// 초기화 이후 시각에 산 새 거래다. canonicalId도 새로 발급된다.
	fresh := testPurchase(uniqueID("orig-tx-new"), domain.StateActive, now.Add(time.Hour))
	fresh.Platform = domain.PlatformAppStore
	fresh.Completion = domain.CompletionAppleFinish

	res, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid, EntitlementID: entID, Purchase: fresh,
	})
	if err != nil {
		t.Fatalf("새 구매 지급 실패: %v", err)
	}
	if res.BlockedBySandboxReset {
		t.Fatal("초기화 이후 새 구매를 차단했다")
	}
	if !res.Granted {
		t.Errorf("granted=%v, want true", res.Granted)
	}
}
