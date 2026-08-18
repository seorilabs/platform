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
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

var integrationIDCounter atomic.Int64

// nextIntegrationID는 시계 해상도가 낮거나 여러 goroutine이 동시에 호출해도
// 같은 process 안에서 반드시 증가하는 식별자를 만든다. 최초 값은 UnixNano라
// 이전 integration 실행이 staging sandbox에 남긴 영구 원장과도 섞이지 않는다.
func nextIntegrationID() int64 {
	for {
		current := integrationIDCounter.Load()
		next := time.Now().UnixNano()
		if next <= current {
			next = current + 1
		}
		if integrationIDCounter.CompareAndSwap(current, next) {
			return next
		}
	}
}

// uniqueID는 테스트마다 다른 식별자를 만들어 이전 실행과 섞이지 않게 한다.
func uniqueID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, nextIntegrationID())
}

func TestIntegrationIDsAreDistinctUnderConcurrency(t *testing.T) {
	const count = 256
	ids := make(chan int64, count)
	for range count {
		go func() { ids <- nextIntegrationID() }()
	}

	seen := make(map[int64]bool, count)
	for range count {
		id := <-ids
		if seen[id] {
			t.Fatalf("동시 integration ID가 중복됐다: %d", id)
		}
		seen[id] = true
	}
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

func TestContentUnitsAreAtomicIdempotentAndReflectRevocation(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueID("pu_content")
	entitlementID := "deep_ticket"
	now := time.Now().UTC().Truncate(time.Second)
	firstPurchase := testPurchase(uniqueID("ticket-a"), domain.StateActive, now)
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid, EntitlementID: entitlementID, Purchase: firstPurchase,
	}); err != nil {
		t.Fatal(err)
	}

	first, err := l.ConsumeUnits(ctx, puid, entitlementID, 2, "reading-a/seun:2026")
	if err != nil || !first.Applied || first.Remaining != 1 || first.SourceKey != firstPurchase.Key() {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	active, err := l.IsSourceActive(ctx, puid, entitlementID, first.SourceKey)
	if err != nil || !active {
		t.Fatalf("active source=%v err=%v", active, err)
	}
	replay, err := l.ConsumeUnits(ctx, puid, entitlementID, 2, "reading-a/seun:2026")
	if err != nil || replay.Applied || replay.Remaining != 1 || replay.SourceKey != first.SourceKey {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	second, err := l.ConsumeUnits(ctx, puid, entitlementID, 2, "reading-a/wolun:2026")
	if err != nil || !second.Applied || second.Remaining != 0 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	_, err = l.ConsumeUnits(ctx, puid, entitlementID, 2, "reading-b/seun:2026")
	if platformerr.CodeOf(err) != platformerr.CodeContentTicketEmpty {
		t.Fatalf("exhausted code=%q err=%v", platformerr.CodeOf(err), err)
	}

	revoked := firstPurchase
	revoked.State = domain.StateRevoked
	revoked.ObservedAt = now.Add(2 * time.Minute)
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid, EntitlementID: entitlementID, Purchase: revoked,
	}); err != nil {
		t.Fatal(err)
	}
	active, err = l.IsSourceActive(ctx, puid, entitlementID, first.SourceKey)
	if err != nil || active {
		t.Fatalf("revoked source=%v err=%v", active, err)
	}
	_, err = l.ConsumeUnits(ctx, puid, entitlementID, 2, "reading-c/seun:2026")
	if platformerr.CodeOf(err) != platformerr.CodeContentTicketEmpty {
		t.Fatalf("revoked source code=%q err=%v", platformerr.CodeOf(err), err)
	}

	// 환불된 source에서 쓴 장수는 새 구매 source의 장수를 깎지 않는다.
	secondPurchase := testPurchase(uniqueID("ticket-b"), domain.StateActive, now.Add(3*time.Minute))
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid, EntitlementID: entitlementID, Purchase: secondPurchase,
	}); err != nil {
		t.Fatal(err)
	}
	afterRepurchase, err := l.ConsumeUnits(ctx, puid, entitlementID, 2, "reading-d/seun:2026")
	if err != nil || !afterRepurchase.Applied || afterRepurchase.Remaining != 1 {
		t.Fatalf("repurchase=%+v err=%v", afterRepurchase, err)
	}
}

func TestContentUnitsFollowPurchaseOwnershipTransfer(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	entitlementID := "deep_ticket"
	now := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("ticket-transfer"), domain.StateActive, now)
	firstOwner := uniqueID("pu_content_owner_a")
	secondOwner := uniqueID("pu_content_owner_b")
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: firstOwner, EntitlementID: entitlementID, Purchase: purchase,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := l.ConsumeUnits(ctx, firstOwner, entitlementID, 2, "reading-a/seun:2026")
	if err != nil || first.Remaining != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}

	transferred := purchase
	transferred.PlatformAccountID = "account-after-reinstall"
	transferred.ObservedAt = now.Add(time.Minute)
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: secondOwner, EntitlementID: entitlementID, Purchase: transferred,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := l.ConsumeUnits(ctx, secondOwner, entitlementID, 2, "reading-b/seun:2026")
	if err != nil || second.Remaining != 0 || second.SourceKey != first.SourceKey {
		t.Fatalf("after transfer=%+v first=%+v err=%v", second, first, err)
	}
	_, err = l.ConsumeUnits(ctx, secondOwner, entitlementID, 2, "reading-c/seun:2026")
	if platformerr.CodeOf(err) != platformerr.CodeContentTicketEmpty {
		t.Fatalf("exhausted after transfer code=%q err=%v", platformerr.CodeOf(err), err)
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

// 소유자가 다른 주문은 새 소유자로 이전된다. ADR 0010
//
// 불변식 4는 "자동 이전 금지"였지만, 그 판정 기준이던 계정 참조는
// 마켓 계정 식별자가 아니라 platform_user_id로 만든 HMAC이었다.
// 앱을 지우면 반드시 달라지므로 재설치한 유저가 영구히 막혔다.
//
// 소유의 근거는 마켓이 이 계정에 발급한 토큰이다. 여기 도달했다는
// 것은 마켓이 이미 소유를 확인해 줬다는 뜻이다.
func TestGrantTransfersOnCrossUser(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	token := uniqueID("token-shared")
	now := time.Now().UTC().Truncate(time.Second)

	owner := uniqueID("pu_owner")
	mine := testPurchase(token, domain.StateActive, now)
	mine.PlatformAccountID = uniqueID("account-owner")
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: owner,
		EntitlementID:  "sp_galaxy_gecko",
		Purchase:       mine,
	}); err != nil {
		t.Fatalf("소유자 지급 실패: %v", err)
	}

	// 다른 마켓 계정의 사용자가 같은 구매 증명을 제시한다
	other := uniqueID("pu_other")
	theirs := testPurchase(token, domain.StateActive, now.Add(time.Minute))
	theirs.PlatformAccountID = uniqueID("account-other")
	_, err := l.Grant(ctx, GrantInput{
		PlatformUserID: other,
		EntitlementID:  "sp_galaxy_gecko",
		Purchase:       theirs,
	})

	if err != nil {
		t.Fatalf("이전이 거부됐다: %v", err)
	}

	// 이전은 이동이지 복제가 아니다. 이전 소유자가 실제로 잃어야 한다.
	oldList, err := l.ListActive(ctx, owner)
	if err != nil {
		t.Fatalf("이전 소유자 목록 조회 실패: %v", err)
	}
	if len(oldList) != 0 {
		t.Errorf("한 구매가 두 계정에서 활성이다. 이전 소유자 목록 = %v", oldList)
	}

	newList, err := l.ListActive(ctx, other)
	if err != nil {
		t.Fatalf("새 소유자 목록 조회 실패: %v", err)
	}
	if len(newList) != 1 {
		t.Errorf("새 소유자 목록 = %v, want 1건", newList)
	}
}

// 불변식 4, 5: 소유권 변경과 같은 transaction에서 최소 복구 증거를
// append한다. 두 번째 이전은 sequence 2를 만들고 sequence 1을 덮지 않는다.
func TestOwnershipTransfersAppendDurableEvidence(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	entitlementID := "sp_galaxy_gecko"
	observedAt := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("token-transfer-evidence"), domain.StateActive, observedAt)
	ownerA := uniqueID("pu_transfer_a")
	ownerB := uniqueID("pu_transfer_b")
	ownerC := uniqueID("pu_transfer_c")

	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: ownerA, EntitlementID: entitlementID, Purchase: purchase,
	}); err != nil {
		t.Fatalf("최초 지급 실패: %v", err)
	}
	toB := purchase
	toB.ObservedAt = observedAt.Add(time.Minute)
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: ownerB, EntitlementID: entitlementID, Purchase: toB,
	}); err != nil {
		t.Fatalf("A→B 이전 실패: %v", err)
	}

	readEvidence := func(sequence int64) ownershipTransferDoc {
		t.Helper()
		path, err := l.paths.ownershipTransfer(purchase.Key(), sequence)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := l.store.Get(ctx, path)
		if err != nil {
			t.Fatalf("sequence %d evidence 조회 실패: %v", sequence, err)
		}
		var doc ownershipTransferDoc
		if err := snap.DataTo(&doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}

	first := readEvidence(1)
	if first.OrderKey != purchase.Key() || first.Sequence != 1 ||
		first.FromPlatformUserID != ownerA || first.ToPlatformUserID != ownerB ||
		first.EntitlementID != entitlementID || first.Platform != purchase.Platform ||
		first.State != domain.StateActive || !first.ObservedAt.Equal(toB.ObservedAt) ||
		first.CreatedAt.IsZero() {
		t.Fatalf("sequence 1 evidence가 불완전하다: %+v", first)
	}

	toC := purchase
	toC.ObservedAt = observedAt.Add(2 * time.Minute)
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: ownerC, EntitlementID: entitlementID, Purchase: toC,
	}); err != nil {
		t.Fatalf("B→C 이전 실패: %v", err)
	}
	second := readEvidence(2)
	if second.OrderKey != purchase.Key() || second.Sequence != 2 ||
		second.FromPlatformUserID != ownerB || second.ToPlatformUserID != ownerC ||
		second.EntitlementID != entitlementID || second.State != domain.StateActive ||
		!second.ObservedAt.Equal(toC.ObservedAt) {
		t.Fatalf("sequence 2 evidence가 불완전하다: %+v", second)
	}

	// sequence 2 append 뒤에도 최초 evidence가 그대로 남아야 한다.
	firstAfterSecond := readEvidence(1)
	if firstAfterSecond != first {
		t.Fatalf("sequence 1 evidence가 덮였다: before=%+v after=%+v", first, firstAfterSecond)
	}
	orderPath, err := l.paths.order(purchase.Key())
	if err != nil {
		t.Fatal(err)
	}
	orderSnap, err := l.store.Get(ctx, orderPath)
	if err != nil {
		t.Fatal(err)
	}
	var storedOrder orderDoc
	if err := orderSnap.DataTo(&storedOrder); err != nil {
		t.Fatal(err)
	}
	if storedOrder.TransferSequence != 2 || storedOrder.PlatformUserID != ownerC {
		t.Fatalf("최종 order 이전 상태가 올바르지 않다: %+v", storedOrder)
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
	puid := uniqueOperatorPUID()
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
	if _, err := l.MarkSandboxReset(ctx, SandboxResetInput{
		RequestID: uniqueID("req"), PlatformUserID: puid, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}); err != nil {
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
	puid := uniqueOperatorPUID()
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
	reset := SandboxResetInput{
		RequestID: uniqueID("req"), PlatformUserID: puid, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}
	initialKeys, err := l.MarkSandboxReset(ctx, reset)
	if err != nil {
		t.Fatalf("초기화 표식 실패: %v", err)
	}
	replayKeys, found, err := l.FindSandboxResetReplay(ctx, reset)
	if err != nil || !found || len(replayKeys) != len(initialKeys) {
		t.Fatalf("초기화 replay 조회 keys=%v found=%v err=%v", replayKeys, found, err)
	}
	changedReset := reset
	changedReset.Reason = AdminReasonIncidentRecovery
	if _, _, err := l.FindSandboxResetReplay(ctx, changedReset); platformerr.CodeOf(err) != platformerr.CodeOperatorReplayMismatch {
		t.Fatalf("다른 초기화 replay payload code=%q", platformerr.CodeOf(err))
	}
	if _, err := l.MarkSandboxReset(ctx, changedReset); platformerr.CodeOf(err) != platformerr.CodeOperatorReplayMismatch {
		t.Fatalf("같은 requestId의 다른 초기화 payload code=%q", platformerr.CodeOf(err))
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

	// HTTP 응답 유실로 같은 requestId를 재시도해도 최초 결과만 돌려주고,
	// 초기화 이후의 새 구매는 건드리지 않는다.
	retryKeys, err := l.MarkSandboxReset(ctx, reset)
	if err != nil {
		t.Fatalf("초기화 멱등 재시도 실패: %v", err)
	}
	if len(retryKeys) != len(initialKeys) || retryKeys[0] != initialKeys[0] {
		t.Fatalf("초기화 결과가 바뀌었다: first=%v retry=%v", initialKeys, retryKeys)
	}
	active, err := l.ListActive(ctx, puid)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0] != entID {
		t.Fatalf("멱등 재시도가 새 구매를 회수했다: %v", active)
	}
}

// 재설치 시나리오를 그대로 재현한다. ADR 0010
//
// 앱을 지우면 익명 uid가 새로 생겨 platform_user도 새로 생긴다.
// 마켓은 같은 구매를 그대로 돌려주므로 복원이 그것을 새 소유자에게
// 옮겨야 한다. Apple은 비소비성에 복원 수단 제공을 심사에서 요구한다.
//
// 이전은 이동이지 복제가 아니다. 한 구매가 두 계정에서 동시에 활성이면
// 원장이 깨지므로, 이전 소유자가 실제로 잃는지까지 본다.
func TestReinstallTransfersOwnership(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	entID := "sp_galaxy_gecko"
	now := time.Now().UTC().Truncate(time.Second)
	account := uniqueID("google-account")

	purchase := testPurchase(uniqueID("token"), domain.StateActive, now)
	purchase.PlatformAccountID = account

	oldUser := uniqueID("pu_old")
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: oldUser, EntitlementID: entID, Purchase: purchase,
	}); err != nil {
		t.Fatalf("최초 지급 실패: %v", err)
	}

	// 재설치. 같은 구글 계정, 새 익명 uid.
	newUser := uniqueID("pu_new")
	later := purchase
	later.ObservedAt = now.Add(time.Minute)
	res, err := l.Grant(ctx, GrantInput{
		PlatformUserID: newUser, EntitlementID: entID, Purchase: later,
	})
	if err != nil {
		t.Fatalf("같은 계정인데 이전이 거부됐다: %v", err)
	}
	if res.TransferredFrom != oldUser {
		t.Errorf("transferredFrom = %q, want %q", res.TransferredFrom, oldUser)
	}
	if !res.Granted {
		t.Errorf("granted = %v, want true", res.Granted)
	}

	newList, err := l.ListActive(ctx, newUser)
	if err != nil {
		t.Fatalf("새 소유자 목록 조회 실패: %v", err)
	}
	if len(newList) != 1 || newList[0] != entID {
		t.Errorf("새 소유자 목록 = %v, want [%s]", newList, entID)
	}

	// 여기가 핵심이다. 이전 소유자가 실제로 잃어야 한다.
	oldList, err := l.ListActive(ctx, oldUser)
	if err != nil {
		t.Fatalf("이전 소유자 목록 조회 실패: %v", err)
	}
	if len(oldList) != 0 {
		t.Errorf("한 구매가 두 계정에서 활성이다. 이전 소유자 목록 = %v", oldList)
	}
}

// 불변식 3, 5, 9: reset이 대상 snapshot을 만든 뒤 provider 검증이 끝난
// 지연 구매가 도착해도 사용자 barrier가 지급을 막는다. 기존 구현은 주문이
// 없을 때 reset 결과를 빈 목록으로 확정해 이 구매를 active로 살려냈다.
func TestSandboxResetBarrierBlocksDelayedUnknownPurchase(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueOperatorPUID()
	resetAt := time.Now().UTC().Truncate(time.Second)
	reset := SandboxResetInput{
		RequestID: uniqueID("req-barrier"), PlatformUserID: puid, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}
	keys, err := l.MarkSandboxReset(ctx, reset)
	if err != nil {
		t.Fatalf("빈 사용자 reset 실패: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("초기 대상 = %v, want []", keys)
	}

	delayed := testPurchase(uniqueID("orig-delayed"), domain.StateActive, resetAt.Add(-time.Minute))
	delayed.Platform = domain.PlatformAppStore
	delayed.Completion = domain.CompletionAppleFinish
	res, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid, EntitlementID: "sp_galaxy_gecko", Purchase: delayed,
	})
	if err != nil {
		t.Fatalf("지연 구매 처리 실패: %v", err)
	}
	if !res.BlockedBySandboxReset || res.Granted || res.AlreadyGranted {
		t.Fatalf("지연 구매 결과 = %+v, want sandbox block", res)
	}
}

// 불변식 3, 5, 9: pre-reset 구매 지급과 reset이 실제로 겹쳐도 reset이
// 성공한 뒤에는 그 구매가 active로 남지 않는다. 지급이 먼저 끝나면 reset
// query가 회수하고, reset이 먼저 끝나면 user barrier가 지급을 차단한다.
func TestConcurrentSandboxResetLeavesNoActivePreResetPurchase(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueOperatorPUID()
	startedAt := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("orig-concurrent-reset"), domain.StateActive, startedAt.Add(-time.Hour))
	purchase.Platform = domain.PlatformAppStore
	purchase.Completion = domain.CompletionAppleFinish

	start := make(chan struct{})
	grantDone := make(chan error, 1)
	resetDone := make(chan error, 1)
	go func() {
		<-start
		_, err := l.Grant(ctx, GrantInput{
			PlatformUserID: puid, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
		})
		grantDone <- err
	}()
	go func() {
		<-start
		_, err := l.MarkSandboxReset(ctx, SandboxResetInput{
			RequestID: uniqueID("req-concurrent-reset"), PlatformUserID: puid, AppID: "app-a",
			ActorLogin: "operator", Reason: AdminReasonInternalValidation,
		})
		resetDone <- err
	}()
	close(start)

	if err := <-grantDone; err != nil {
		t.Fatalf("동시 지급 실패: %v", err)
	}
	if err := <-resetDone; err != nil {
		t.Fatalf("동시 reset 실패: %v", err)
	}
	active, err := l.ListActive(ctx, puid)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("reset 완료 뒤 pre-reset 구매가 active다: %v", active)
	}
}

// 불변식 3, 9: reset 요청 뒤 실제로 산 신규 구매는 동시 transaction의
// query에 보이더라도 cutoff 이후 구매이므로 회수하지 않는다.
func TestConcurrentSandboxResetPreservesPostResetPurchase(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueOperatorPUID()
	purchase := testPurchase(uniqueID("orig-post-reset"), domain.StateActive,
		time.Now().UTC().Add(time.Hour))
	purchase.Platform = domain.PlatformAppStore
	purchase.Completion = domain.CompletionAppleFinish

	start := make(chan struct{})
	grantDone := make(chan error, 1)
	resetDone := make(chan error, 1)
	go func() {
		<-start
		_, err := l.Grant(ctx, GrantInput{
			PlatformUserID: puid, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
		})
		grantDone <- err
	}()
	go func() {
		<-start
		_, err := l.MarkSandboxReset(ctx, SandboxResetInput{
			RequestID: uniqueID("req-post-reset"), PlatformUserID: puid, AppID: "app-a",
			ActorLogin: "operator", Reason: AdminReasonInternalValidation,
		})
		resetDone <- err
	}()
	close(start)

	if err := <-grantDone; err != nil {
		t.Fatalf("신규 지급 실패: %v", err)
	}
	if err := <-resetDone; err != nil {
		t.Fatalf("동시 reset 실패: %v", err)
	}
	active, err := l.ListActive(ctx, puid)
	if err != nil || len(active) != 1 || active[0] != "sp_galaxy_gecko" {
		t.Fatalf("cutoff 이후 구매가 회수됐다: active=%v err=%v", active, err)
	}
}

// ADR 0012: phase 1 뒤 프로세스가 중단돼도 immutable intent와 active barrier가
// 남아 같은 requestId로 phase 2를 재개해야 한다. prepared를 미적용으로 닫거나
// 새 requestId를 만들면 request-start-wins 경계가 사라진다.
func TestSandboxResetPreparedIntentResumesAfterInterruption(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueOperatorPUID()
	cutoff := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("orig-prepared-resume"), domain.StateActive,
		cutoff.Add(-time.Hour))
	purchase.Platform = domain.PlatformAppStore
	purchase.Completion = domain.CompletionAppleFinish
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: puid, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	}); err != nil {
		t.Fatal(err)
	}
	reset := SandboxResetInput{
		RequestID: uniqueID("req-prepared-resume"), PlatformUserID: puid, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}
	if _, err := l.prepareSandboxResetIntentWithClock(ctx, reset,
		func() time.Time { return cutoff }, nil, nil); err != nil {
		t.Fatalf("phase 1 준비 실패: %v", err)
	}
	status, err := l.GetSandboxResetStatus(ctx, reset.RequestID)
	if err != nil || status.State != SandboxResetPrepared || len(status.OrderKeys) != 0 {
		t.Fatalf("prepared 상태=%+v err=%v", status, err)
	}

	other := reset
	other.RequestID = uniqueID("req-prepared-busy")
	if _, err := l.MarkSandboxReset(ctx, other); platformerr.CodeOf(err) != platformerr.CodeSandboxResetBusy {
		t.Fatalf("다른 requestId code=%q, want busy", platformerr.CodeOf(err))
	}

	keys, err := l.ResumeSandboxReset(ctx, reset.RequestID)
	if err != nil || len(keys) != 1 || keys[0] != purchase.Key() {
		t.Fatalf("resume keys=%v err=%v", keys, err)
	}
	status, err = l.GetSandboxResetStatus(ctx, reset.RequestID)
	if err != nil || status.State != SandboxResetCompleted || len(status.OrderKeys) != 1 {
		t.Fatalf("completed 상태=%+v err=%v", status, err)
	}
	// 완료 응답만 유실된 재호출도 immutable completion을 그대로 돌려준다.
	replay, err := l.ResumeSandboxReset(ctx, reset.RequestID)
	if err != nil || len(replay) != 1 || replay[0] != keys[0] {
		t.Fatalf("completion replay=%v err=%v", replay, err)
	}
}

// ADR 0012: prepare transaction이 barrier를 읽은 뒤 Grant가 먼저 commit하면
// Firestore가 prepare를 재시도한다. 두 번째 attempt는 cutoff를 새로 잡아 먼저
// commit된 구매를 reset 대상에 포함해야 한다. 함수 진입 때 한 번만 시각을
// 잡으면 이 구매가 post-cutoff로 오판되어 active로 남는다.
func TestSandboxResetPrepareRetryRefreshesCutoffAfterGrantCommit(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	puid := uniqueOperatorPUID()
	base := time.Now().UTC().Truncate(time.Second)
	firstCutoff := base
	purchasedAt := base.Add(time.Second)
	retriedCutoff := base.Add(2 * time.Second)
	purchase := testPurchase(uniqueID("orig-prepare-retry"), domain.StateActive, purchasedAt)
	purchase.Platform = domain.PlatformAppStore
	purchase.Completion = domain.CompletionAppleFinish
	reset := SandboxResetInput{
		RequestID: uniqueID("req-prepare-retry"), PlatformUserID: puid, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}

	var clockCalls atomic.Int64
	clock := func() time.Time {
		if clockCalls.Add(1) == 1 {
			return firstCutoff
		}
		return retriedCutoff
	}
	firstAttemptAborted := make(chan struct{})
	retryMayRead := make(chan struct{})
	type prepareResult struct {
		intent sandboxResetRequestDoc
		err    error
	}
	prepareDone := make(chan prepareResult, 1)
	go func() {
		intent, err := l.prepareSandboxResetIntentWithClock(ctx, reset, clock,
			func(attempt int) error {
				if attempt == 0 {
					return nil
				}
				// 첫 attempt가 lock을 해제한 뒤 Grant가 먼저 commit할 때까지
				// 다음 attempt가 barrier를 읽지 않게 한다.
				select {
				case <-retryMayRead:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
			func(attempt int, cutoff time.Time) error {
				if attempt != 0 {
					return nil
				}
				if !cutoff.Equal(firstCutoff) {
					return fmt.Errorf("첫 cutoff=%s, want %s", cutoff, firstCutoff)
				}
				close(firstAttemptAborted)
				// Firestore client가 callback의 Aborted를 같은 transaction의
				// 새 attempt로 재시도하게 해 실제 retry clock 경계를 검증한다.
				return status.Error(codes.Aborted, "force prepare retry")
			})
		prepareDone <- prepareResult{intent: intent, err: err}
	}()

	select {
	case <-firstAttemptAborted:
	case <-ctx.Done():
		t.Fatalf("prepare 첫 attempt 대기 실패: %v", ctx.Err())
	}
	// 첫 attempt가 중단된 사이 Grant를 먼저 commit한다.
	grantLedger := New(l.store, domain.EnvSandbox).WithClock(func() time.Time { return purchasedAt })
	if _, err := grantLedger.Grant(ctx, GrantInput{
		PlatformUserID: puid, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	}); err != nil {
		close(retryMayRead)
		t.Fatalf("prepare보다 먼저 commit할 지급 실패: %v", err)
	}
	close(retryMayRead)

	var prepared prepareResult
	select {
	case prepared = <-prepareDone:
	case <-ctx.Done():
		t.Fatalf("prepare retry 완료 대기 실패: %v", ctx.Err())
	}
	if prepared.err != nil {
		t.Fatalf("prepare retry 실패: %v", prepared.err)
	}
	if clockCalls.Load() < 2 || !prepared.intent.ResetAt.Equal(retriedCutoff) {
		t.Fatalf("retry cutoff=%s clockCalls=%d, want %s and >=2",
			prepared.intent.ResetAt, clockCalls.Load(), retriedCutoff)
	}
	keys, err := l.ResumeSandboxReset(ctx, reset.RequestID)
	if err != nil || len(keys) != 1 || keys[0] != purchase.Key() {
		t.Fatalf("먼저 commit된 구매 reset 결과 keys=%v err=%v", keys, err)
	}
	active, err := l.ListActive(ctx, puid)
	if err != nil || len(active) != 0 {
		t.Fatalf("prepare retry 뒤 구매가 active다: active=%v err=%v", active, err)
	}
}

// GET 404 뒤 수동 unlock 전에 늦은 prepare가 도착하는 경합은 immutable
// closure와 intent가 서로의 경로를 읽는 transaction으로 닫는다. 두 commit
// 순서를 각각 고정해 어느 쪽도 closure와 intent를 함께 만들지 못함을 검증한다.
func TestSandboxResetClosureAndPrepareCommitOrders(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	ctx := context.Background()
	actor := "operator"

	t.Run("closure commit이 먼저면 늦은 prepare를 영구 거부", func(t *testing.T) {
		requestID := uniqueID("req-closure-first")
		closure := SandboxResetClosureInput{
			RequestID: requestID, AppID: "app-a", ActorLogin: actor,
		}
		applied, err := l.CloseSandboxResetNotStarted(ctx, closure)
		if err != nil || !applied {
			t.Fatalf("최초 closure applied=%v err=%v", applied, err)
		}
		applied, err = l.CloseSandboxResetNotStarted(ctx, closure)
		if err != nil || applied {
			t.Fatalf("closure exact replay applied=%v err=%v", applied, err)
		}
		changed := closure
		changed.ActorLogin = "other-operator"
		if _, err := l.CloseSandboxResetNotStarted(ctx, changed); platformerr.CodeOf(err) != platformerr.CodeOperatorReplayMismatch {
			t.Fatalf("다른 actor closure code=%q", platformerr.CodeOf(err))
		}

		statusResult, err := l.GetSandboxResetStatus(ctx, requestID)
		if err != nil || statusResult.State != SandboxResetClosedNotStarted ||
			statusResult.RequestID != requestID || statusResult.AppID != closure.AppID ||
			statusResult.PlatformUserID != "" || !statusResult.ResetAt.IsZero() ||
			len(statusResult.OrderKeys) != 0 {
			t.Fatalf("closure status=%+v err=%v", statusResult, err)
		}
		reset := SandboxResetInput{
			RequestID: requestID, PlatformUserID: uniqueOperatorPUID(), AppID: closure.AppID,
			ActorLogin: actor, Reason: AdminReasonInternalValidation,
		}
		if _, _, err := l.FindSandboxResetReplay(ctx, reset); platformerr.CodeOf(err) != platformerr.CodeSandboxResetClosed {
			t.Fatalf("closure 뒤 replay code=%q", platformerr.CodeOf(err))
		}
		if _, err := l.MarkSandboxReset(ctx, reset); platformerr.CodeOf(err) != platformerr.CodeSandboxResetClosed {
			t.Fatalf("closure 뒤 prepare code=%q", platformerr.CodeOf(err))
		}
		if _, err := l.ResumeSandboxReset(ctx, requestID); platformerr.CodeOf(err) != platformerr.CodeSandboxResetClosed {
			t.Fatalf("closure 뒤 resume code=%q", platformerr.CodeOf(err))
		}

		closurePath, err := l.paths.sandboxResetClosure(requestID)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := l.store.Get(ctx, closurePath)
		if err != nil {
			t.Fatal(err)
		}
		raw := snap.Data()
		for _, forbidden := range []string{"platformUserId", "reason", "orderKeys", "resetAt"} {
			if _, exists := raw[forbidden]; exists {
				t.Errorf("PII-free closure에 금지 필드 %q가 있다: %v", forbidden, raw)
			}
		}
	})

	t.Run("prepare commit이 먼저면 closure를 거부하고 intent를 유지", func(t *testing.T) {
		requestID := uniqueID("req-prepare-first")
		reset := SandboxResetInput{
			RequestID: requestID, PlatformUserID: uniqueOperatorPUID(), AppID: "app-a",
			ActorLogin: actor, Reason: AdminReasonInternalValidation,
		}
		cutoff := time.Now().UTC().Truncate(time.Second)
		if _, err := l.prepareSandboxResetIntentWithClock(ctx, reset,
			func() time.Time { return cutoff }, nil, nil); err != nil {
			t.Fatalf("prepare-first intent 실패: %v", err)
		}
		if _, err := l.CloseSandboxResetNotStarted(ctx, SandboxResetClosureInput{
			RequestID: requestID, AppID: reset.AppID, ActorLogin: actor,
		}); platformerr.CodeOf(err) != platformerr.CodeSandboxResetAlreadyStarted {
			t.Fatalf("prepare 뒤 closure code=%q", platformerr.CodeOf(err))
		}
		statusResult, err := l.GetSandboxResetStatus(ctx, requestID)
		if err != nil || statusResult.State != SandboxResetPrepared {
			t.Fatalf("prepare-first status=%+v err=%v", statusResult, err)
		}
		if _, err := l.ResumeSandboxReset(ctx, requestID); err != nil {
			t.Fatalf("closure 거부 뒤 intent resume 실패: %v", err)
		}
	})
}

func TestSandboxResetClosureKeepsOperatorRequestIDUnique(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()
	ctx := context.Background()

	closureFirst := SandboxResetClosureInput{
		RequestID: uniqueID("req-closure-operator"), AppID: "app-a", ActorLogin: "operator",
	}
	if _, err := l.CloseSandboxResetNotStarted(ctx, closureFirst); err != nil {
		t.Fatal(err)
	}
	grant := validOperatorInput()
	grant.RequestID = closureFirst.RequestID
	grant.PlatformUserID = uniqueOperatorPUID()
	if _, err := l.OperatorGrant(ctx, grant); platformerr.CodeOf(err) != platformerr.CodeOperatorReplayMismatch {
		t.Fatalf("closure requestId를 operator grant에 재사용한 code=%q", platformerr.CodeOf(err))
	}

	grant.RequestID = uniqueID("req-operator-closure")
	if _, err := l.OperatorGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CloseSandboxResetNotStarted(ctx, SandboxResetClosureInput{
		RequestID: grant.RequestID, AppID: grant.AppID, ActorLogin: grant.ActorLogin,
	}); platformerr.CodeOf(err) != platformerr.CodeOperatorReplayMismatch {
		t.Fatalf("operator requestId를 closure에 재사용한 code=%q", platformerr.CodeOf(err))
	}
}

// ADR 0012, 불변식 4·5: A reset intent가 먼저 commit되면 A→B 이전은 이전
// 소유자 A의 active barrier를 읽고 차단해야 한다. B barrier만 읽으면 주문이
// A의 phase 2 query에서 빠져나가 active로 살아남는다.
func TestSandboxResetIntentWinsBeforeCrossUserTransfer(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	oldUser := uniqueOperatorPUID()
	newUser := uniqueOperatorPUID()
	cutoff := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("orig-reset-first-transfer"), domain.StateActive,
		cutoff.Add(-time.Hour))
	purchase.Platform = domain.PlatformAppStore
	purchase.Completion = domain.CompletionAppleFinish
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: oldUser, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	}); err != nil {
		t.Fatal(err)
	}
	reset := SandboxResetInput{
		RequestID: uniqueID("req-reset-first-transfer"), PlatformUserID: oldUser, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}
	if _, err := l.prepareSandboxResetIntentWithClock(ctx, reset,
		func() time.Time { return cutoff }, nil, nil); err != nil {
		t.Fatal(err)
	}

	res, err := l.Grant(ctx, GrantInput{
		PlatformUserID: newUser, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	})
	if err != nil || !res.BlockedBySandboxReset || res.TransferredFrom != "" {
		t.Fatalf("reset-first 이전 결과=%+v err=%v", res, err)
	}
	if _, err := l.ResumeSandboxReset(ctx, reset.RequestID); err != nil {
		t.Fatal(err)
	}
	for _, puid := range []string{oldUser, newUser} {
		active, err := l.ListActive(ctx, puid)
		if err != nil || len(active) != 0 {
			t.Fatalf("reset-first puid=%s active=%v err=%v", puid, active, err)
		}
	}
}

// 이전 commit이 먼저면 이후 A reset은 이미 B로 이동한 구매를 회수하지 않는다.
// 두 serial order를 모두 고정해야 동시 실행의 어느 commit 순서도 안전하다.
func TestCrossUserTransferWinsBeforeSandboxResetIntent(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	oldUser := uniqueOperatorPUID()
	newUser := uniqueOperatorPUID()
	now := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("orig-transfer-first-reset"), domain.StateActive,
		now.Add(-time.Hour))
	purchase.Platform = domain.PlatformAppStore
	purchase.Completion = domain.CompletionAppleFinish
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: oldUser, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: newUser, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	}); err != nil {
		t.Fatal(err)
	}
	keys, err := l.MarkSandboxReset(ctx, SandboxResetInput{
		RequestID: uniqueID("req-transfer-first-reset"), PlatformUserID: oldUser, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	})
	if err != nil || len(keys) != 0 {
		t.Fatalf("transfer-first reset keys=%v err=%v", keys, err)
	}
	active, err := l.ListActive(ctx, newUser)
	if err != nil || len(active) != 1 || active[0] != "sp_galaxy_gecko" {
		t.Fatalf("먼저 완료된 이전이 회수됐다: active=%v err=%v", active, err)
	}
}

// intent cutoff 뒤의 신규 구매는 이전 소유자 barrier가 active여도 허용한다.
func TestSandboxResetIntentAllowsPostCutoffCrossUserTransfer(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	oldUser := uniqueOperatorPUID()
	newUser := uniqueOperatorPUID()
	cutoff := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("orig-post-cutoff-transfer"), domain.StateActive,
		cutoff.Add(time.Hour))
	purchase.Platform = domain.PlatformAppStore
	purchase.Completion = domain.CompletionAppleFinish
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: oldUser, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	}); err != nil {
		t.Fatal(err)
	}
	reset := SandboxResetInput{
		RequestID: uniqueID("req-post-cutoff-transfer"), PlatformUserID: oldUser, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}
	if _, err := l.prepareSandboxResetIntentWithClock(ctx, reset,
		func() time.Time { return cutoff }, nil, nil); err != nil {
		t.Fatal(err)
	}
	res, err := l.Grant(ctx, GrantInput{
		PlatformUserID: newUser, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	})
	if err != nil || res.BlockedBySandboxReset || res.TransferredFrom != oldUser {
		t.Fatalf("post-cutoff 이전 결과=%+v err=%v", res, err)
	}
	keys, err := l.ResumeSandboxReset(ctx, reset.RequestID)
	if err != nil || len(keys) != 0 {
		t.Fatalf("post-cutoff resume keys=%v err=%v", keys, err)
	}
	active, err := l.ListActive(ctx, newUser)
	if err != nil || len(active) != 1 {
		t.Fatalf("post-cutoff 구매가 사라졌다: active=%v err=%v", active, err)
	}
}

// Firestore transaction 상한을 넘기기 전에 201번째 entitlement에서
// fail-closed하고, intent는 prepared로 남아 운영자가 같은 ID를 대조하게 한다.
func TestSandboxResetRejectsMoreThanTwoHundredEntitlements(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	puid := uniqueOperatorPUID()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i <= maxSandboxResetEntitlements; i++ {
		entitlementID := fmt.Sprintf("sp_limit_%03d", i)
		path, err := l.paths.internalEntitlement(puid, entitlementID)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.store.Set(ctx, path, entitlementDoc{
			EntitlementID: entitlementID, Sources: map[string]domain.Source{}, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("entitlement %d 준비 실패: %v", i, err)
		}
	}
	reset := SandboxResetInput{
		RequestID: uniqueID("req-entitlement-limit"), PlatformUserID: puid, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}
	if _, err := l.prepareSandboxResetIntentWithClock(ctx, reset,
		func() time.Time { return now }, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ResumeSandboxReset(ctx, reset.RequestID); platformerr.CodeOf(err) != platformerr.CodeSandboxResetPending {
		t.Fatalf("201개 resume code=%q, want pending", platformerr.CodeOf(err))
	}
	status, err := l.GetSandboxResetStatus(ctx, reset.RequestID)
	if err != nil || status.State != SandboxResetPrepared {
		t.Fatalf("상한 실패 뒤 status=%+v err=%v", status, err)
	}
}

// 불변식 4, 6: reset된 주문을 새 PUID가 제시해도 새 사용자에게 revoked
// shadow source를 복제하지 않는다. 이후 그 사용자의 reset도 소유자 불일치로
// 실패하지 않아야 한다.
func TestCrossUserSandboxBlockLeavesNoShadowSource(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	oldUser := uniqueOperatorPUID()
	newUser := uniqueOperatorPUID()
	now := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("orig-shadow"), domain.StateActive, now.Add(-time.Hour))
	purchase.Platform = domain.PlatformAppStore
	purchase.Completion = domain.CompletionAppleFinish
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: oldUser, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.MarkSandboxReset(ctx, SandboxResetInput{
		RequestID: uniqueID("req-old"), PlatformUserID: oldUser, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := l.Grant(ctx, GrantInput{
		PlatformUserID: newUser, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	})
	if err != nil || !res.BlockedBySandboxReset {
		t.Fatalf("cross-user 차단 결과=%+v err=%v", res, err)
	}
	ents, err := l.ListUserEntitlements(ctx, newUser)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		for _, src := range ent.Sources {
			if src.OrderKey == purchase.Key() {
				t.Fatalf("새 사용자에게 shadow source가 남았다: %+v", src)
			}
		}
	}
	if _, err := l.MarkSandboxReset(ctx, SandboxResetInput{
		RequestID: uniqueID("req-new"), PlatformUserID: newUser, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}); err != nil {
		t.Fatalf("새 사용자 reset이 shadow source 때문에 실패했다: %v", err)
	}
}

// 불변식 4, 6: 과거 구현이 이미 남긴 cross-PUID revoked shadow도 reset이
// 소유자 불일치로 실패하지 않고 안전하게 제거해야 한다.
func TestSandboxResetCleansLegacyCrossUserRevokedShadow(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	oldUser := uniqueOperatorPUID()
	newUser := uniqueOperatorPUID()
	entitlementID := "sp_galaxy_gecko"
	now := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("orig-legacy-shadow"), domain.StateActive, now.Add(-time.Hour))
	purchase.Platform = domain.PlatformAppStore
	purchase.Completion = domain.CompletionAppleFinish
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: oldUser, EntitlementID: entitlementID, Purchase: purchase,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.MarkSandboxReset(ctx, SandboxResetInput{
		RequestID: uniqueID("req-legacy-old"), PlatformUserID: oldUser, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	}); err != nil {
		t.Fatal(err)
	}

	// 수정 전 cross-PUID block이 만들던 새 사용자 쪽 revoked source를 재현한다.
	if err := l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		return l.writeEntitlement(tx, newUser, entitlementDoc{
			EntitlementID: entitlementID,
			Sources: map[string]domain.Source{
				purchase.Key(): {
					Platform: purchase.Platform, ProductID: purchase.ProductID,
					State: domain.StateRevoked, PurchasedAt: purchase.PurchasedAt,
					ObservedAt: now, UpdatedAt: now,
				},
			},
		}, now)
	}); err != nil {
		t.Fatalf("legacy shadow 준비 실패: %v", err)
	}

	keys, err := l.MarkSandboxReset(ctx, SandboxResetInput{
		RequestID: uniqueID("req-legacy-new"), PlatformUserID: newUser, AppID: "app-a",
		ActorLogin: "operator", Reason: AdminReasonInternalValidation,
	})
	if err != nil {
		t.Fatalf("legacy shadow 정리 reset 실패: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("다른 소유자 order가 reset 대상에 포함됐다: %v", keys)
	}
	ents, err := l.ListUserEntitlements(ctx, newUser)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		for _, src := range ent.Sources {
			if src.OrderKey == purchase.Key() {
				t.Fatalf("legacy shadow가 남았다: %+v", src)
			}
		}
	}
}

// 불변식 3, 4, 6: active→active cross-PUID 복원은 observedAt만 과거라는
// 이유로 성공 처리만 하고 소유권 이전을 건너뛰면 안 된다. state 회귀가
// 없으므로 이전하되, revoked→active stale 차단은 기존대로 보존한다.
func TestCrossUserActiveRestoreTransfersDespiteOlderObservedAt(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	oldUser := uniqueID("pu_old_stale")
	newUser := uniqueID("pu_new_stale")
	late := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("token-stale-transfer"), domain.StateActive, late)
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: oldUser, EntitlementID: "sp_galaxy_gecko", Purchase: purchase,
	}); err != nil {
		t.Fatal(err)
	}

	olderObservation := purchase
	olderObservation.ObservedAt = late.Add(-time.Minute)
	res, err := l.Grant(ctx, GrantInput{
		PlatformUserID: newUser, EntitlementID: "sp_galaxy_gecko", Purchase: olderObservation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Granted || res.AlreadyGranted || res.TransferredFrom != oldUser {
		t.Fatalf("복원 결과 = %+v", res)
	}
	active, err := l.ListActive(ctx, newUser)
	if err != nil || len(active) != 1 || active[0] != "sp_galaxy_gecko" {
		t.Fatalf("새 사용자 entitlement=%v err=%v", active, err)
	}

	orderPath, err := l.paths.order(purchase.Key())
	if err != nil {
		t.Fatal(err)
	}
	snap, err := l.store.Get(ctx, orderPath)
	if err != nil {
		t.Fatal(err)
	}
	var storedOrder orderDoc
	if err := snap.DataTo(&storedOrder); err != nil {
		t.Fatal(err)
	}
	if storedOrder.PlatformUserID != newUser || storedOrder.State != domain.StateActive ||
		!storedOrder.ObservedAt.Equal(late) {
		t.Fatalf("이전 후 order 최신 상태가 손실됐다: %+v", storedOrder)
	}

	entPath, err := l.paths.internalEntitlement(newUser, "sp_galaxy_gecko")
	if err != nil {
		t.Fatal(err)
	}
	entSnap, err := l.store.Get(ctx, entPath)
	if err != nil {
		t.Fatal(err)
	}
	var storedEnt entitlementDoc
	if err := entSnap.DataTo(&storedEnt); err != nil {
		t.Fatal(err)
	}
	src := storedEnt.Sources[purchase.Key()]
	if src.State != domain.StateActive || !src.ObservedAt.Equal(late) {
		t.Fatalf("이전 후 source 최신 상태가 손실됐다: %+v", src)
	}
}

// 불변식 4, 10: 웹훅은 transaction 밖에서 본 과거 owner를 소유권 근거로
// 사용할 수 없다. A를 읽은 뒤 앱 복원이 A→B로 끝났다면 stale active 웹훅이
// B→A로 되돌리지 않고 owner 변경을 감지해야 한다.
func TestWebhookExpectedOwnerFenceNeverReversesRestore(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	ownerA := uniqueID("pu_webhook_owner_a")
	ownerB := uniqueID("pu_webhook_owner_b")
	entitlementID := "sp_galaxy_gecko"
	observedAt := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("token-webhook-owner-fence"), domain.StateActive, observedAt)

	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: ownerA, EntitlementID: entitlementID, Purchase: purchase,
	}); err != nil {
		t.Fatalf("최초 지급 실패: %v", err)
	}
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: ownerB, EntitlementID: entitlementID, Purchase: purchase,
	}); err != nil {
		t.Fatalf("앱 복원 이전 실패: %v", err)
	}

	staleWebhook := purchase
	staleWebhook.ObservedAt = observedAt.Add(-time.Minute)
	_, err := l.grantExpectedOwner(ctx, GrantInput{
		PlatformUserID: ownerA, EntitlementID: entitlementID, Purchase: staleWebhook,
	}, ownerA)
	if !errors.Is(err, errReconcileOwnerChanged) {
		t.Fatalf("과거 owner fence 오류 = %v, want owner changed", err)
	}

	oldActive, err := l.ListActive(ctx, ownerA)
	if err != nil || len(oldActive) != 0 {
		t.Fatalf("웹훅이 과거 owner를 되살렸다: active=%v err=%v", oldActive, err)
	}
	newActive, err := l.ListActive(ctx, ownerB)
	if err != nil || len(newActive) != 1 || newActive[0] != entitlementID {
		t.Fatalf("현재 owner 권리가 손실됐다: active=%v err=%v", newActive, err)
	}

	res, err := l.ReconcileByCanonicalID(ctx, staleWebhook)
	if err != nil {
		t.Fatalf("현재 owner 재조정 실패: %v", err)
	}
	if !res.Known || res.PlatformUserID != ownerB || res.EntitlementID != entitlementID {
		t.Fatalf("재조정 대상 = %+v, want current owner %q", res, ownerB)
	}
}

// 불변식 4, 10: owner를 읽은 바로 다음 앱 복원이 커밋되는 실제 간격을
// 강제한다. 첫 expected-owner transaction은 sentinel로 중단되고 두 번째
// 시도는 현재 owner B를 재조회해 그 사용자에게만 상태를 반영해야 한다.
func TestReconcileRetriesOwnerChangedBetweenReadAndTransaction(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	ownerA := uniqueID("pu_reconcile_race_a")
	ownerB := uniqueID("pu_reconcile_race_b")
	entitlementID := "sp_galaxy_gecko"
	observedAt := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("token-reconcile-race"), domain.StateActive, observedAt)
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: ownerA, EntitlementID: entitlementID, Purchase: purchase,
	}); err != nil {
		t.Fatalf("최초 지급 실패: %v", err)
	}

	staleWebhook := purchase
	staleWebhook.ObservedAt = observedAt.Add(-time.Minute)
	reads := 0
	res, err := l.reconcileByCanonicalID(ctx, staleWebhook,
		func(attempt int, owner string) error {
			reads++
			if attempt != 0 {
				return nil
			}
			if owner != ownerA {
				t.Fatalf("첫 owner = %q, want %q", owner, ownerA)
			}
			_, err := l.Grant(ctx, GrantInput{
				PlatformUserID: ownerB, EntitlementID: entitlementID, Purchase: staleWebhook,
			})
			return err
		})
	if err != nil {
		t.Fatalf("owner 재조회 reconcile 실패: %v", err)
	}
	if reads != 2 || !res.Known || res.PlatformUserID != ownerB {
		t.Fatalf("reconcile 결과=%+v ownerReads=%d, want owner B 재조회", res, reads)
	}
	oldActive, err := l.ListActive(ctx, ownerA)
	if err != nil || len(oldActive) != 0 {
		t.Fatalf("과거 owner가 되살아났다: active=%v err=%v", oldActive, err)
	}
	newActive, err := l.ListActive(ctx, ownerB)
	if err != nil || len(newActive) != 1 || newActive[0] != entitlementID {
		t.Fatalf("현재 owner 권리가 손실됐다: active=%v err=%v", newActive, err)
	}
}

// 불변식 4, 10: A→B→A처럼 owner가 매 시도 바뀌면 한 사용자를 임의로
// 선택하지 않는다. 세 번의 fence 충돌 뒤 retryable event_busy로 lease를
// 풀어야 하며, 마지막 실제 owner만 entitlement를 가진다.
func TestReconcileOwnerABAExhaustionReturnsRetryableBusy(t *testing.T) {
	l, done := newTestLedger(t)
	defer done()

	ctx := context.Background()
	ownerA := uniqueID("pu_reconcile_aba_a")
	ownerB := uniqueID("pu_reconcile_aba_b")
	entitlementID := "sp_galaxy_gecko"
	observedAt := time.Now().UTC().Truncate(time.Second)
	purchase := testPurchase(uniqueID("token-reconcile-aba"), domain.StateActive, observedAt)
	if _, err := l.Grant(ctx, GrantInput{
		PlatformUserID: ownerA, EntitlementID: entitlementID, Purchase: purchase,
	}); err != nil {
		t.Fatalf("최초 지급 실패: %v", err)
	}

	staleWebhook := purchase
	staleWebhook.ObservedAt = observedAt.Add(-time.Minute)
	reads := 0
	_, err := l.reconcileByCanonicalID(ctx, staleWebhook,
		func(_ int, owner string) error {
			reads++
			nextOwner := ownerA
			if owner == ownerA {
				nextOwner = ownerB
			}
			_, err := l.Grant(ctx, GrantInput{
				PlatformUserID: nextOwner,
				EntitlementID:  entitlementID,
				Purchase:       staleWebhook,
			})
			return err
		})
	if code := platformerr.CodeOf(err); code != platformerr.CodeEventBusy {
		t.Fatalf("ABA reconcile 오류=%v code=%q, want event_busy", err, code)
	}
	if !platformerr.IsRetryableErr(err) {
		t.Fatalf("event_busy가 retryable이 아니다: %v", err)
	}
	if reads != reconcileOwnerRetryLimit {
		t.Fatalf("owner read=%d, want %d", reads, reconcileOwnerRetryLimit)
	}

	activeA, err := l.ListActive(ctx, ownerA)
	if err != nil {
		t.Fatal(err)
	}
	activeB, err := l.ListActive(ctx, ownerB)
	if err != nil {
		t.Fatal(err)
	}
	if len(activeA) != 0 || len(activeB) != 1 || activeB[0] != entitlementID {
		t.Fatalf("ABA 뒤 단일 owner 위반: A=%v B=%v", activeA, activeB)
	}
}
