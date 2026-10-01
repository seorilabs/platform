package verify

import (
	"context"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/boxes"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type boxFakeLedger struct {
	fakeLedger
	opened []string
}

func (f *boxFakeLedger) BoxSnapshot(context.Context, string, *boxes.Catalog) (ledger.BoxState, error) {
	return ledger.BoxState{Available: 3}, nil
}

func (f *boxFakeLedger) OpenBox(_ context.Context, _ string, _ *boxes.Catalog, v *boxes.Version, requestID string, r boxes.Random) (ledger.BoxReceipt, error) {
	d, err := v.Draw(r, nil, 0)
	if err != nil {
		return ledger.BoxReceipt{}, err
	}
	f.opened = append(f.opened, requestID)
	return ledger.BoxReceipt{RequestID: requestID, Applied: true, CatalogVersion: v.Version, FriendID: d.FriendID, Rarity: d.Rarity}, nil
}

type boxApps struct{ app registry.App }

func (a boxApps) GetUsable(context.Context, string) (registry.App, error) { return a.app, nil }

type fixedRandom []int

func (f *fixedRandom) Intn(n int) (int, error) { v := (*f)[0]; *f = (*f)[1:]; return v % n, nil }

func newBoxService(t *testing.T, entitlements []string, skuJSON string) (*Service, *boxFakeLedger, *fakeAuditor) {
	t.Helper()
	cat, err := catalog.Parse([]byte(skuJSON), nil)
	if err != nil {
		t.Fatal(err)
	}
	bc, _, err := boxes.Load("bloomhand")
	if err != nil {
		t.Fatal(err)
	}
	led := &boxFakeLedger{}
	aud := &fakeAuditor{}
	app := registry.App{AppID: "bloomhand", Features: map[string]bool{"iap": true}}
	app.IAP.EntitlementIDs = entitlements
	svc, err := New(Config{
		Ledger: led, AppLedgers: map[string]Ledger{"bloomhand": led}, Catalog: cat,
		Auditor: aud, Apps: boxApps{app: app}, Boxes: map[string]*boxes.Catalog{"bloomhand": bc},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := fixedRandom{9200, 1}
	svc.withBoxClock(func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }, &r)
	return svc, led, aud
}

const bloomhandSKUs = `{"version":2,"apps":{"bloomhand":{"entitlements":{
 "friend_box_1":{"type":"consumable","google_play":"bloomhand_friend_box_1","app_store":"bloomhand_friend_box_1"},
 "friend_box_5":{"type":"consumable","google_play":"bloomhand_friend_box_5","app_store":"bloomhand_friend_box_5"}}}}}`

func TestOpenBoxUsesServerRandomAndAudits(t *testing.T) {
	svc, led, aud := newBoxService(t, []string{"friend_box_1", "friend_box_5"}, bloomhandSKUs)
	r, err := svc.OpenBox(context.Background(), "bloomhand", "pu_1", "open-req-1")
	if err != nil || r.FriendID != "B19" || r.Rarity != "rare" || r.CatalogVersion != "2026-10-01" {
		t.Fatalf("%+v %v", r, err)
	}
	if len(led.opened) != 1 || len(aud.records) == 0 || aud.records[len(aud.records)-1] != (auditRecord{action: "iap.box_opened", outcome: "ok"}) {
		t.Fatalf("opened=%v audit=%v", led.opened, aud.records)
	}
	snap, err := svc.BoxSnapshot(context.Background(), "bloomhand", "pu_1")
	if err != nil || snap.State.Available != 3 || snap.Catalog.Pity != 10 || len(snap.Catalog.Friends) != 20 || len(snap.Catalog.Products) != 2 {
		t.Fatalf("%+v %v", snap, err)
	}
}

// 레지스트리 허용 목록이나 SKU 카탈로그에 상자 상품이 빠지면 구매와 개봉이
// 어긋나므로 열지 않는다.
func TestOpenBoxFailsClosedOnConfigMismatch(t *testing.T) {
	svc, led, _ := newBoxService(t, []string{"friend_box_1"}, bloomhandSKUs)
	if _, err := svc.OpenBox(context.Background(), "bloomhand", "pu_1", "open-req-1"); platformerr.CodeOf(err) != platformerr.CodeRuntimeConfigInvalid {
		t.Fatalf("err=%v", err)
	}
	if len(led.opened) != 0 {
		t.Fatal("opened despite mismatch")
	}
	if _, err := svc.BoxSnapshot(context.Background(), "other-app", "pu_1"); platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
		t.Fatalf("other app: %v", err)
	}
}
