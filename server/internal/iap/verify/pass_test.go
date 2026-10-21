package verify

import (
	"context"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type passFakeLedger struct {
	fakeLedger
	schedules map[string]domain.PassSchedule
	claims    []string
	claimErr  error
}

func (f *passFakeLedger) PassSnapshot(_ context.Context, _ string, ent string, s domain.PassSchedule) (ledger.PassStatus, error) {
	f.schedules[ent] = s
	return ledger.PassStatus{EntitlementID: ent, DailyAmount: s.DailyAmount, Claimable: true}, nil
}

func (f *passFakeLedger) ClaimPass(_ context.Context, _ string, ent string, s domain.PassSchedule, requestID string) (ledger.PassReceipt, error) {
	if f.claimErr != nil {
		return ledger.PassReceipt{}, f.claimErr
	}
	f.schedules[ent] = s
	f.claims = append(f.claims, ent+"/"+requestID)
	return ledger.PassReceipt{RequestID: requestID, Applied: true, EntitlementID: ent, Days: 1, Amount: s.DailyAmount}, nil
}

const mergeLizardPassSKUs = `{"version":2,"apps":{"merge-lizard":{"entitlements":{
 "amber_pass_30d":{"type":"consumable","google_play":"ml_pass","app_store":"ml_pass",
  "pass":{"immediate_amount":300,"daily_amount":50,"days":30,"missed_days":"forfeit"}},
 "amber_v2_s":{"type":"consumable","google_play":"ml_amber_s","app_store":"ml_amber_s"}}}}}`

func newPassService(t *testing.T, allowed []string, skuJSON string) (*Service, *passFakeLedger, *fakeAuditor) {
	t.Helper()
	cat, err := catalog.Parse([]byte(skuJSON), nil)
	if err != nil {
		t.Fatal(err)
	}
	led := &passFakeLedger{schedules: map[string]domain.PassSchedule{}}
	aud := &fakeAuditor{}
	app := registry.App{AppID: "merge-lizard", Features: map[string]bool{"iap": true}}
	app.IAP.EntitlementIDs = allowed
	svc, err := New(Config{
		Ledger: led, AppLedgers: map[string]Ledger{"merge-lizard": led}, Catalog: cat,
		Auditor: aud, Apps: boxApps{app: app},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) }
	return svc, led, aud
}

// 지급 일정은 클라이언트가 아니라 서버 카탈로그에서 온다.
func TestPassServiceUsesCatalogScheduleAndAudits(t *testing.T) {
	svc, led, aud := newPassService(t, []string{"amber_pass_30d", "amber_v2_s"}, mergeLizardPassSKUs)
	snap, err := svc.PassSnapshot(context.Background(), "merge-lizard", "pu_1")
	if err != nil || len(snap.Passes) != 1 || snap.Passes[0].EntitlementID != "amber_pass_30d" ||
		snap.ServerTime != time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("%+v %v", snap, err)
	}
	r, err := svc.ClaimPass(context.Background(), "merge-lizard", "pu_1", "amber_pass_30d", "claim-req-1")
	if err != nil || r.Amount != 50 || led.schedules["amber_pass_30d"].Days != 30 {
		t.Fatalf("%+v %v", r, err)
	}
	if last := aud.records[len(aud.records)-1]; last != (auditRecord{action: "iap.pass_claimed", outcome: "ok"}) {
		t.Fatalf("audit=%v", aud.records)
	}
	led.claimErr = platformerr.New(platformerr.CodePassAlreadyClaimed, "x")
	if _, err := svc.ClaimPass(context.Background(), "merge-lizard", "pu_1", "amber_pass_30d", "claim-req-2"); platformerr.CodeOf(err) != platformerr.CodePassAlreadyClaimed {
		t.Fatalf("err=%v", err)
	}
	if last := aud.records[len(aud.records)-1]; last.outcome != string(platformerr.CodePassAlreadyClaimed) {
		t.Fatalf("audit=%v", aud.records)
	}
}

// 이용권이 아닌 상품, 레지스트리가 허용하지 않은 이용권, 이용권이 없는 앱은 받지 않는다.
func TestPassServiceRejectsNonPassProducts(t *testing.T) {
	svc, led, _ := newPassService(t, []string{"amber_pass_30d", "amber_v2_s"}, mergeLizardPassSKUs)
	for _, ent := range []string{"amber_v2_s", "unknown"} {
		if _, err := svc.ClaimPass(context.Background(), "merge-lizard", "pu_1", ent, "claim-req-1"); platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
			t.Fatalf("%s: err=%v", ent, err)
		}
	}
	if len(led.claims) != 0 {
		t.Fatalf("claims=%v", led.claims)
	}
	svc, led, _ = newPassService(t, []string{"amber_v2_s"}, mergeLizardPassSKUs)
	if _, err := svc.ClaimPass(context.Background(), "merge-lizard", "pu_1", "amber_pass_30d", "claim-req-1"); platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
		t.Fatalf("registry 미허용: err=%v", err)
	}
	if _, err := svc.PassSnapshot(context.Background(), "merge-lizard", "pu_1"); platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
		t.Fatalf("이용권 없는 앱: err=%v", err)
	}
	if len(led.claims) != 0 {
		t.Fatalf("claims=%v", led.claims)
	}
}
