//go:build integration

package ledger

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/operational"
	"github.com/seorilabs/platform/server/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newEconomyNotificationLedger(t *testing.T, environment domain.Environment) *Ledger {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("알림 원자성 검증은 격리 Firestore 에뮬레이터에서 실행한다")
	}
	st, err := store.New(context.Background(), os.Getenv("GOOGLE_CLOUD_PROJECT"), uniqueID("stg_crystal_"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	catalog, err := LizardEconomyCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return New(st, environment).WithAppID("lizard-tycoon").WithEconomyCatalog(catalog).
		WithOperationalEvents(operational.NewRepository(st))
}

func economyNotificationEvents(t *testing.T, l *Ledger) []operational.Event {
	t.Helper()
	p, err := fspath.Parse("operational_event_outbox")
	if err != nil {
		t.Fatal(err)
	}
	it, err := l.store.Query(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Stop()
	docs, err := it.GetAll()
	if err != nil {
		t.Fatal(err)
	}
	events := make([]operational.Event, len(docs))
	for i, doc := range docs {
		if err := doc.DataTo(&events[i]); err != nil {
			t.Fatal(err)
		}
	}
	return events
}

func TestEconomyGrantNotificationsCoverPacksMarketsAndTestFlags(t *testing.T) {
	real, test := false, true
	cases := []struct {
		name        string
		platform    domain.Platform
		environment domain.Environment
		flag        *bool
	}{
		{"play-real", domain.PlatformGooglePlay, domain.EnvProduction, &real},
		{"play-test", domain.PlatformGooglePlay, domain.EnvProduction, &test},
		{"play-unknown", domain.PlatformGooglePlay, domain.EnvProduction, nil},
		{"apple-real", domain.PlatformAppStore, domain.EnvProduction, &real},
		{"apple-test", domain.PlatformAppStore, domain.EnvSandbox, &test},
		{"apple-unknown", domain.PlatformAppStore, domain.EnvProduction, nil},
	}
	for _, tc := range cases {
		for _, product := range []string{"crystal_300", "crystal_1000", "crystal_3200", "crystal_5500", "crystal_starter"} {
			t.Run(tc.name+"/"+product, func(t *testing.T) {
				l := newEconomyNotificationLedger(t, tc.environment)
				ctx, puid := context.Background(), uniqueID("pu_crystal")
				purchase := testPurchase(uniqueID("crystal-order"), domain.StateActive, time.Now().UTC())
				purchase.Platform, purchase.ProductID, purchase.IsTestPurchase = tc.platform, product, tc.flag
				if l.isEconomyTestPurchase(purchase) {
					if _, err := l.SetEconomyTester(ctx, puid, "test-operator", true); err != nil {
						t.Fatal(err)
					}
				}
				input := GrantInput{PlatformUserID: puid, EntitlementID: product, Purchase: purchase}
				first, err := l.Grant(ctx, input)
				if err != nil || !first.Granted || first.AlreadyGranted {
					t.Fatalf("first grant = %+v, error = %v", first, err)
				}
				// 복원에서 관측 시각이 바뀌어도 이미 지급한 주문의 알림은 늘지 않는다.
				input.Purchase.ObservedAt = purchase.ObservedAt.Add(time.Minute)
				replay, err := l.Grant(ctx, input)
				if err != nil || replay.Granted || !replay.AlreadyGranted {
					t.Fatalf("replay = %+v, error = %v", replay, err)
				}
				events := economyNotificationEvents(t, l)
				if len(events) != 1 {
					t.Fatalf("new grant and replay notifications = %d, want 1", len(events))
				}
				event := events[0]
				if event.Type != "iap.granted" || event.AppID != "lizard-tycoon" || event.Outcome != "granted" || event.OccurredAt.IsZero() {
					t.Fatalf("notification envelope = %+v", event)
				}
				if event.Attributes["platform"] != string(tc.platform) || event.Attributes["entitlementId"] != product {
					t.Fatalf("notification attributes = %+v", event.Attributes)
				}
				flag, present := event.Attributes["isTestPurchase"]
				wantAttributes := 2
				if tc.flag != nil {
					wantAttributes++
					if !present || flag != *tc.flag {
						t.Fatalf("test purchase flag = %v, present = %v, want %v", flag, present, *tc.flag)
					}
				} else if present {
					t.Fatalf("unobserved test flag was invented: %v", flag)
				}
				if len(event.Attributes) != wantAttributes {
					t.Fatalf("unexpected notification attributes: %+v", event.Attributes)
				}
			})
		}
	}
}

func TestEconomyGrantNotificationsAreOncePerOrderUnderConcurrency(t *testing.T) {
	l := newEconomyNotificationLedger(t, domain.EnvProduction)
	ctx, puid := context.Background(), uniqueID("pu_concurrent_crystal")
	purchase := testPurchase(uniqueID("crystal-order"), domain.StateActive, time.Now().UTC())
	purchase.ProductID = "crystal_300"
	input := GrantInput{PlatformUserID: puid, EntitlementID: purchase.ProductID, Purchase: purchase}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := l.Grant(ctx, input); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if events := economyNotificationEvents(t, l); len(events) != 1 {
		t.Fatalf("concurrent notifications = %d, want 1", len(events))
	}
	// 소모품의 다음 주문은 같은 상품이어도 새 결제로 알린다.
	input.Purchase.CanonicalID = uniqueID("next-crystal-order")
	if _, err := l.Grant(ctx, input); err != nil {
		t.Fatal(err)
	}
	snapshot, err := l.EconomySnapshot(ctx, puid)
	if err != nil || snapshot.State.Paid != 600 {
		t.Fatalf("wallet = %+v, error = %v", snapshot.State, err)
	}
	if events := economyNotificationEvents(t, l); len(events) != 2 {
		t.Fatalf("separate-order notifications = %d, want 2", len(events))
	}
	input.Purchase.State = domain.StateRevoked
	input.Purchase.ObservedAt = input.Purchase.ObservedAt.Add(time.Minute)
	if err := l.RevokeByCanonicalID(ctx, input.Purchase); err != nil {
		t.Fatal(err)
	}
	input.Purchase.State = domain.StateActive
	input.Purchase.ObservedAt = input.Purchase.ObservedAt.Add(time.Minute)
	if _, err := l.Grant(ctx, input); err != nil {
		t.Fatal(err)
	}
	if events := economyNotificationEvents(t, l); len(events) != 2 {
		t.Fatalf("refunded-order replay added a grant notification: %d", len(events))
	}
}

func TestEconomyGrantNotificationWriteFailureRollsBackCreditAndOrder(t *testing.T) {
	l := newEconomyNotificationLedger(t, domain.EnvProduction)
	ctx, puid := context.Background(), uniqueID("pu_atomic_crystal")
	purchase := testPurchase(uniqueID("crystal-order"), domain.StateActive, time.Now().UTC())
	purchase.ProductID = "crystal_300"
	// outbox 기록 실패를 실제 Firestore의 중복 문서 오류로 만든다.
	event := operational.Event{
		EventID:    operational.StableEventID("iap", l.appID, purchase.Key(), puid, purchase.ObservedAt.UTC().Format(time.RFC3339Nano)),
		OccurredAt: time.Now().UTC(), Type: "iap.granted", AppID: l.appID, Outcome: "granted",
		Attributes: map[string]any{"platform": string(purchase.Platform), "entitlementId": purchase.ProductID},
	}
	if err := l.store.RunTransaction(ctx, func(_ context.Context, tx *store.Tx) error {
		return l.operational.EnqueueTx(tx, event)
	}); err != nil {
		t.Fatal(err)
	}
	_, err := l.Grant(ctx, GrantInput{PlatformUserID: puid, EntitlementID: purchase.ProductID, Purchase: purchase})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("outbox conflict error = %v, want AlreadyExists", err)
	}
	op, err := l.paths.order(purchase.Key())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.store.Get(ctx, op); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("order survived failed transaction: %v", err)
	}
	lookup, err := l.LookupEconomy(ctx, puid)
	if err != nil || lookup.Found || lookup.TestFound {
		t.Fatalf("wallet survived failed transaction: %+v, error = %v", lookup, err)
	}
}

func TestEconomyGrantNotificationsExcludePendingAndRejectedStarter(t *testing.T) {
	l := newEconomyNotificationLedger(t, domain.EnvProduction)
	ctx, puid := context.Background(), uniqueID("pu_pending_crystal")
	purchase := testPurchase(uniqueID("starter-order"), domain.StatePending, time.Now().UTC())
	purchase.ProductID = "crystal_starter"
	input := GrantInput{PlatformUserID: puid, EntitlementID: purchase.ProductID, Purchase: purchase}
	if err := l.RecordPending(ctx, input); err != nil {
		t.Fatal(err)
	}
	if events := economyNotificationEvents(t, l); len(events) != 0 {
		t.Fatalf("pending order generated a grant notification: %d", len(events))
	}
	input.Purchase.State = domain.StateActive
	input.Purchase.ObservedAt = input.Purchase.ObservedAt.Add(time.Minute)
	if _, err := l.Grant(ctx, input); err != nil {
		t.Fatal(err)
	}
	input.Purchase.CanonicalID = uniqueID("second-starter-order")
	if _, err := l.Grant(ctx, input); err == nil {
		t.Fatal("second starter was accepted")
	}
	if events := economyNotificationEvents(t, l); len(events) != 1 {
		t.Fatalf("starter completion and rejected repurchase notifications = %d, want 1", len(events))
	}
}
