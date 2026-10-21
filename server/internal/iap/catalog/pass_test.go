package catalog

import (
	"reflect"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

const passCatalogJSON = `{"version":2,"apps":{
 "merge-lizard":{"entitlements":{
  "amber_pass_30d":{"type":"consumable","google_play":"ml_pass","app_store":"ml_pass",
   "pass":{"immediate_amount":300,"daily_amount":50,"days":30,"missed_days":"forfeit"}},
  "amber_v2_s":{"type":"consumable","google_play":"ml_amber_s","app_store":"ml_amber_s"}}},
 "bloomhand":{"entitlements":{
  "friend_box_1":{"type":"consumable","google_play":"bh_box_1"}}}}}`

// 이용권 일정은 카탈로그 항목의 선택 필드다. 다른 항목과 다른 앱은 그대로 해석한다.
func TestPassScheduleIsOptionalPerEntry(t *testing.T) {
	c := mustParse(t, passCatalogJSON)
	got, ok := c.PassForApp("merge-lizard", "amber_pass_30d")
	want := domain.PassSchedule{ImmediateAmount: 300, DailyAmount: 50, Days: 30, MissedDays: domain.MissedDaysForfeit}
	if !ok || got != want {
		t.Fatalf("PassForApp=%+v %v", got, ok)
	}
	if ids := c.PassEntitlementsForApp("merge-lizard"); !reflect.DeepEqual(ids, []string{"amber_pass_30d"}) {
		t.Fatalf("pass ids=%v", ids)
	}
	for _, q := range []struct{ app, ent string }{
		{"merge-lizard", "amber_v2_s"}, {"bloomhand", "friend_box_1"}, {"bloomhand", "amber_pass_30d"},
	} {
		if _, ok := c.PassForApp(q.app, q.ent); ok {
			t.Fatalf("%s/%s에 일정이 생겼다", q.app, q.ent)
		}
	}
	if ids := c.PassEntitlementsForApp("bloomhand"); len(ids) != 0 {
		t.Fatalf("bloomhand pass ids=%v", ids)
	}
	// 구매 검증 경로는 일정과 무관하게 기존 소모성 그대로다.
	product, err := c.ProductForApp("merge-lizard", domain.PlatformGooglePlay, "ml_pass")
	if err != nil || product.EntitlementID != "amber_pass_30d" || product.Type != domain.ProductConsumable {
		t.Fatalf("product=%+v err=%v", product, err)
	}
	// 기존 전역 형식에는 일정이 없다.
	if ids := mustParse(t, validJSON).PassEntitlementsForApp(""); len(ids) != 0 {
		t.Fatalf("legacy pass ids=%v", ids)
	}
}

func TestPassScheduleRejectsInvalidEntries(t *testing.T) {
	cases := map[string]string{
		"비소모성":    `{"type":"non_consumable","google_play":"p","pass":{"daily_amount":50,"days":30,"missed_days":"forfeit"}}`,
		"유형 생략":   `{"google_play":"p","pass":{"daily_amount":50,"days":30,"missed_days":"forfeit"}}`,
		"정책 누락":   `{"type":"consumable","google_play":"p","pass":{"daily_amount":50,"days":30}}`,
		"일수 0":    `{"type":"consumable","google_play":"p","pass":{"daily_amount":50,"days":0,"missed_days":"forfeit"}}`,
		"모르는 필드":  `{"type":"consumable","google_play":"p","pass":{"daily_amount":50,"days":30,"missed_days":"forfeit","stack":true}}`,
		"하루 지급 0": `{"type":"consumable","google_play":"p","pass":{"days":30,"missed_days":"forfeit"}}`,
	}
	for name, entry := range cases {
		raw := `{"version":2,"apps":{"merge-lizard":{"entitlements":{"amber_pass_30d":` + entry + `}}}}`
		if _, err := Parse([]byte(raw), nil); platformerr.CodeOf(err) != platformerr.CodeCatalogInvalid {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}
