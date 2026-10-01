package ledger

import (
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/boxes"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

func bloomhandBoxes(t *testing.T) *boxes.Catalog {
	t.Helper()
	c, ok, err := boxes.Load("bloomhand")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	return c
}

// 5개짜리 구매 두 건 중 하나가 3개 쓴 뒤 환불되면, 그 3개는 부채로 남고
// 활성 구매의 남은 수에서 빠진다. ADR 0029 6항.
func TestBoxUnitsCountsRefundDebt(t *testing.T) {
	cat := bloomhandBoxes(t)
	ents := map[string]entitlementDoc{
		"friend_box_5": {Sources: map[string]domain.Source{
			"a": {State: domain.StateActive, ContentUnitsConsumed: 1},
			"b": {State: domain.StateRevoked, ContentUnitsConsumed: 3},
			"p": {State: domain.StatePending},
		}},
		"friend_box_1": {Sources: map[string]domain.Source{
			"c": {State: domain.StateActive},
		}},
	}
	remaining, debt, err := boxUnits(cat, ents)
	if err != nil || remaining != 5 || debt != 3 {
		t.Fatalf("remaining=%d debt=%d err=%v", remaining, debt, err)
	}
}

func TestBoxUnitsRejectsOverusedSource(t *testing.T) {
	cat := bloomhandBoxes(t)
	_, _, err := boxUnits(cat, map[string]entitlementDoc{
		"friend_box_1": {Sources: map[string]domain.Source{"a": {State: domain.StateActive, ContentUnitsConsumed: 2}}},
	})
	if platformerr.CodeOf(err) != platformerr.CodeLedgerStateInvalid {
		t.Fatalf("err=%v", err)
	}
}

func TestChooseBoxSourceIsOrderedAndSkipsExhausted(t *testing.T) {
	cat := bloomhandBoxes(t)
	ents := map[string]entitlementDoc{
		"friend_box_1": {Sources: map[string]domain.Source{"z": {State: domain.StateActive, ContentUnitsConsumed: 1}}},
		"friend_box_5": {Sources: map[string]domain.Source{
			"b": {State: domain.StateActive},
			"a": {State: domain.StateRevoked},
		}},
	}
	ent, key, ok := chooseBoxSource(cat, ents)
	if !ok || ent != "friend_box_5" || key != "b" {
		t.Fatalf("%s %s %v", ent, key, ok)
	}
	if _, _, ok := chooseBoxSource(cat, map[string]entitlementDoc{}); ok {
		t.Fatal("no source must not be chosen")
	}
}

func TestBoxViewDerivesLevels(t *testing.T) {
	v := boxView(boxStateDoc{Copies: map[string]int{"B19": 4, "B02": 1, "gone": 0}}, 2, 3)
	if v.Available != -1 || v.Levels["B19"] != 3 || v.Levels["B02"] != 1 || len(v.Copies) != 2 {
		t.Fatalf("%+v", v)
	}
}
