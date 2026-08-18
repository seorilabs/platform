package content

import (
	"slices"
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

func validResolveRequest() ResolveRequest {
	flows := make([]FlowFact, 12)
	for i := range flows {
		flows[i] = FlowFact{Sipseong: "siksin", State: "보통"}
	}
	return ResolveRequest{
		SchemaVersion: 1, Scope: []string{"base", "seun", "wolun"},
		Reading: DerivedReadingFacts{
			Kind: "full", Chart: ChartFacts{Year: "甲子", Month: "丙寅", Day: "戊辰", Hour: "庚午"},
			Ilju: "mujin",
			Johap: []JohapFact{
				{Sipseong: "jeongjae", Unseong: "tae"},
				{Sipseong: "pyeongwan", Unseong: "jangsaeng"},
				{Sipseong: "bigyeon", Unseong: "gwandae"},
				{Sipseong: "jeongin", Unseong: "jewang"},
			},
			Sinsal:    []SinsalFact{{Name: "yeokma", Variant: "nyeonju"}},
			Relations: []RelationFact{{Kind: "chung", Pair: "子午"}},
			Daeun:     []FlowFact{{Sipseong: "jeonggwan", State: "보통"}},
			Seun:      SeunFacts{Year: 2026, Flow: FlowFact{Sipseong: "siksin", State: "과다"}, DaeunSipseong: []string{"jeonggwan"}, Samjae: "in"},
			Wolun:     flows,
		},
	}
}

func TestSelectBuildsOnlyDerivedCoordinates(t *testing.T) {
	selection, err := Select(validResolveRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ilju.mujin", "topic.mujin_seonghyang", "hapchung.chung_ja_o"} {
		if !slices.Contains(selection.BaseIDs, want) {
			t.Errorf("base IDs에 %q가 없다: %v", want, selection.BaseIDs)
		}
	}
	if got := selection.DeepIDs["seun"]; !slices.Contains(got, "seun.siksin_과다") {
		t.Errorf("seun IDs = %v", got)
	}
}

func TestSelectRejectsForgedRelation(t *testing.T) {
	req := validResolveRequest()
	req.Reading.Relations = []RelationFact{{Kind: "yukhap", Pair: "子午"}}
	_, err := Select(req)
	if platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("code = %q, err = %v", platformerr.CodeOf(err), err)
	}
}

func TestSelectRejectsForgedJohap(t *testing.T) {
	req := validResolveRequest()
	req.Reading.Johap[0] = JohapFact{Sipseong: "bigyeon", Unseong: "jangsaeng"}
	_, err := Select(req)
	if platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("code = %q, err = %v", platformerr.CodeOf(err), err)
	}
}

func TestSelectRejectsRelationOutsideChart(t *testing.T) {
	req := validResolveRequest()
	req.Reading.Relations = []RelationFact{{Kind: "chung", Pair: "丑未"}}
	_, err := Select(req)
	if platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("code = %q, err = %v", platformerr.CodeOf(err), err)
	}
}

func TestSelectAcceptsThreePillarsAndRejectsForgedHour(t *testing.T) {
	req := validResolveRequest()
	req.Reading.Kind = "three_pillar"
	req.Reading.Chart.Hour = ""
	req.Reading.Johap = req.Reading.Johap[:3]
	// 원래 fixture의 신살과 子午 충은 시지가 있어야 성립한다.
	req.Reading.Sinsal = nil
	req.Reading.Relations = nil
	if _, err := Select(req); err != nil {
		t.Fatalf("three pillar: %v", err)
	}
	req.Reading.Chart.Hour = "庚午"
	if _, err := Select(req); platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("forged hour code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestSelectRejectsSinsalForThreePillars(t *testing.T) {
	req := validResolveRequest()
	req.Reading.Kind = "three_pillar"
	req.Reading.Chart.Hour = ""
	req.Reading.Johap = req.Reading.Johap[:3]
	req.Reading.Relations = nil
	_, err := Select(req)
	if platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestSelectRejectsUnknownSinsalCoordinate(t *testing.T) {
	req := validResolveRequest()
	req.Reading.Sinsal = []SinsalFact{{Name: "invented"}}
	_, err := Select(req)
	if platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestSinsalCoordinateAllowlistHasReleaseAxisSize(t *testing.T) {
	if len(validSinsal) != 46 {
		t.Fatalf("신살 allowlist=%d, want 46", len(validSinsal))
	}
}

func TestSelectRejectsRewardClaimOutsidePublicContract(t *testing.T) {
	req := validResolveRequest()
	req.Unlock = &UnlockRequest{Section: "seun", Kind: "reward_claim", ClaimID: "cl_bad/value"}
	_, err := Select(req)
	if platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestSelectReadingKeyIsStableForSetOrder(t *testing.T) {
	first := validResolveRequest()
	first.Reading.Sinsal = append(first.Reading.Sinsal, SinsalFact{Name: "hwagae", Variant: "ilju"})
	second := first
	second.Reading.Sinsal = []SinsalFact{first.Reading.Sinsal[1], first.Reading.Sinsal[0]}
	a, err := Select(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Select(second)
	if err != nil {
		t.Fatal(err)
	}
	if a.ReadingKey != b.ReadingKey {
		t.Fatalf("reading key differs: %s != %s", a.ReadingKey, b.ReadingKey)
	}
}
