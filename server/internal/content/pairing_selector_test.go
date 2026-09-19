package content

import (
	"regexp"
	"slices"
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// 기대값은 앱 파이썬 오라클(design/saju-reader/calc/pairing.py)로 뽑았다. A 丁巳일(화 6개
// 과다, 금·수 없음)과 B 癸酉일 시각 미상(화 없음). 巳酉는 삼합 반합이고 丁癸는 천간합이
// 아니다. 화는 A 과다 × B 부족이라 fill_a 다.
func validPairingRequest() ResolvePairingRequest {
	return ResolvePairingRequest{
		SchemaVersion: 1,
		A:             PairingSideFacts{Kind: "full", Chart: ChartFacts{Year: "丙午", Month: "乙未", Day: "丁巳", Hour: "丙午"}},
		B:             PairingSideFacts{Kind: "three_pillar", Chart: ChartFacts{Year: "乙丑", Month: "戊寅", Day: "癸酉"}},
		Pair: PairFacts{
			Ilgan: PairIlganFacts{AToB: "pyeonjae", BToA: "pyeongwan", Hap: false},
			Ilji:  PairIljiFacts{Tags: []string{"samhap"}, Primary: "samhap"},
			Ohaeng: []PairOhaengFact{
				{Name: "mok", A: "보통", B: "보통", Kind: "plain"},
				{Name: "hwa", A: "과다", B: "부족", Kind: "fill_a"},
				{Name: "to", A: "보통", B: "보통", Kind: "plain"},
				{Name: "geum", A: "부족", B: "보통", Kind: "plain"},
				{Name: "su", A: "부족", B: "보통", Kind: "plain"},
			},
			Close: PairCloseFacts{Stem: "sanggeuk", Branch: "hap"},
		},
	}
}

// swappedPairingRequest는 같은 두 사람을 반대 순서로 보낸 요청이다. 앱이 어느 쪽을 A로
// 두든 pairKey와 좌표가 같아야 한다.
func swappedPairingRequest(req ResolvePairingRequest) ResolvePairingRequest {
	fillSwap := map[string]string{"fill_a": "fill_b", "fill_b": "fill_a", "overlap": "overlap", "gap": "gap", "plain": "plain"}
	out := req
	out.A, out.B = req.B, req.A
	out.Pair.Ilgan = PairIlganFacts{AToB: req.Pair.Ilgan.BToA, BToA: req.Pair.Ilgan.AToB, Hap: req.Pair.Ilgan.Hap}
	out.Pair.Ohaeng = make([]PairOhaengFact, 0, len(req.Pair.Ohaeng))
	for _, row := range req.Pair.Ohaeng {
		out.Pair.Ohaeng = append(out.Pair.Ohaeng, PairOhaengFact{Name: row.Name, A: row.B, B: row.A, Kind: fillSwap[row.Kind]})
	}
	return out
}

var pairKeyPattern = regexp.MustCompile(`^pk_[a-f0-9]{64}$`)

func TestSelectPairingBuildsOnlyDerivedCoordinates(t *testing.T) {
	selection, err := SelectPairing(validPairingRequest())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"gung-close.sanggeuk_hap", "gung-ilgan.pyeongwan", "gung-ilgan.pyeonjae",
		"gung-ilji.samhap", "gung-ohaeng.fill_hwa",
	}
	if !slices.Equal(selection.DeepIDs, want) {
		t.Fatalf("deep IDs = %v, want %v", selection.DeepIDs, want)
	}
	if !pairKeyPattern.MatchString(selection.PairKey) {
		t.Fatalf("pairKey = %q", selection.PairKey)
	}
}

func TestSelectPairingRejectsForgedPair(t *testing.T) {
	for _, tc := range []struct {
		name  string
		forge func(*ResolvePairingRequest)
	}{
		{name: "십성 방향 뒤집기", forge: func(r *ResolvePairingRequest) {
			r.Pair.Ilgan.AToB, r.Pair.Ilgan.BToA = r.Pair.Ilgan.BToA, r.Pair.Ilgan.AToB
		}},
		{name: "없는 천간합", forge: func(r *ResolvePairingRequest) { r.Pair.Ilgan.Hap = true }},
		{name: "없는 육합 태그", forge: func(r *ResolvePairingRequest) {
			r.Pair.Ilji.Tags = append(r.Pair.Ilji.Tags, "yukhap")
		}},
		{name: "대표 관계 바꿔치기", forge: func(r *ResolvePairingRequest) { r.Pair.Ilji.Primary = "none" }},
		{name: "채움을 plain으로", forge: func(r *ResolvePairingRequest) { r.Pair.Ohaeng[1].Kind = "plain" }},
		{name: "채움 방향 뒤집기", forge: func(r *ResolvePairingRequest) { r.Pair.Ohaeng[1].Kind = "fill_b" }},
		{name: "오행 순서 뒤섞기", forge: func(r *ResolvePairingRequest) {
			r.Pair.Ohaeng[0], r.Pair.Ohaeng[1] = r.Pair.Ohaeng[1], r.Pair.Ohaeng[0]
		}},
		{name: "오행 줄 누락", forge: func(r *ResolvePairingRequest) { r.Pair.Ohaeng = r.Pair.Ohaeng[:4] }},
		{name: "마무리 갈래 바꾸기", forge: func(r *ResolvePairingRequest) { r.Pair.Close.Stem = "bihwa" }},
		{name: "모르는 십성", forge: func(r *ResolvePairingRequest) { r.Pair.Ilgan.AToB = "invented" }},
		{name: "음양이 어긋난 간지", forge: func(r *ResolvePairingRequest) { r.A.Chart.Day = "丁午" }},
		{name: "모르는 명식 종류", forge: func(r *ResolvePairingRequest) { r.B.Kind = "two_pillar" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validPairingRequest()
			tc.forge(&req)
			_, err := SelectPairing(req)
			if platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
				t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
			}
		})
	}
}

func TestSelectPairingKeyAndCoordinatesAreSymmetric(t *testing.T) {
	first, err := SelectPairing(validPairingRequest())
	if err != nil {
		t.Fatal(err)
	}
	second, err := SelectPairing(swappedPairingRequest(validPairingRequest()))
	if err != nil {
		t.Fatal(err)
	}
	if first.PairKey != second.PairKey {
		t.Fatalf("pairKey differs: %s != %s", first.PairKey, second.PairKey)
	}
	if !slices.Equal(first.DeepIDs, second.DeepIDs) {
		t.Fatalf("deep IDs differ: %v != %v", first.DeepIDs, second.DeepIDs)
	}
	other := validPairingRequest()
	other.B.Chart.Year = "乙亥"
	third, err := SelectPairing(other)
	if err != nil {
		t.Fatal(err)
	}
	if third.PairKey == first.PairKey {
		t.Fatal("다른 명식 쌍이 같은 pairKey를 만들었다")
	}
}

func TestSelectPairingThreePillarSideHasNoHour(t *testing.T) {
	req := validPairingRequest()
	req.B.Chart.Hour = "壬子"
	if _, err := SelectPairing(req); platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("시각 미상에 시주: code=%q err=%v", platformerr.CodeOf(err), err)
	}
	req = validPairingRequest()
	req.A.Chart.Hour = ""
	if _, err := SelectPairing(req); platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("네 기둥에 시주 없음: code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

// 巳申은 육합·형·파가 동시에 성립한다. 태그는 고정 순서로 셋 다 오고, 대표는 형이다 —
// 부딪힘이 합보다 먼저다.
func TestSelectPairingSaSinHasThreeTagsWithHyeongPrimary(t *testing.T) {
	req := ResolvePairingRequest{
		SchemaVersion: 1,
		A:             PairingSideFacts{Kind: "full", Chart: ChartFacts{Year: "甲子", Month: "丙寅", Day: "己巳", Hour: "庚午"}},
		B:             PairingSideFacts{Kind: "full", Chart: ChartFacts{Year: "乙丑", Month: "戊寅", Day: "庚申", Hour: "壬午"}},
		Pair: PairFacts{
			Ilgan: PairIlganFacts{AToB: "jeongin", BToA: "sanggwan", Hap: false},
			Ilji:  PairIljiFacts{Tags: []string{"yukhap", "hyeong", "pa"}, Primary: "hyeong"},
			Ohaeng: []PairOhaengFact{
				{Name: "mok", A: "보통", B: "보통", Kind: "plain"},
				{Name: "hwa", A: "보통", B: "보통", Kind: "plain"},
				{Name: "to", A: "보통", B: "보통", Kind: "plain"},
				{Name: "geum", A: "보통", B: "보통", Kind: "plain"},
				{Name: "su", A: "보통", B: "보통", Kind: "plain"},
			},
			Close: PairCloseFacts{Stem: "sangsaeng", Branch: "chung"},
		},
	}
	selection, err := SelectPairing(req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gung-close.sangsaeng_chung", "gung-ilgan.jeongin", "gung-ilgan.sanggwan", "gung-ilji.hyeong"}
	if !slices.Equal(selection.DeepIDs, want) {
		t.Fatalf("deep IDs = %v, want %v", selection.DeepIDs, want)
	}
	// 순서가 다르면 같은 집합이어도 거절한다 — 앱과 서버의 고정 순서 계약이다.
	req.Pair.Ilji.Tags = []string{"hyeong", "yukhap", "pa"}
	if _, err := SelectPairing(req); platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("순서 뒤섞임: code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestSelectPairingSameDayBranchIsSameAndHap(t *testing.T) {
	req := validPairingRequest()
	req.B.Chart.Day = "癸巳"
	// 癸巳일로 바꾸면 巳巳 — 자형은 두 사람 사이에서 내지 않으므로 태그 없이 same 이고
	// 마무리는 hap 이다. 일간은 그대로라 십성·오행 줄은 변하지 않는다(癸酉→癸巳: 금 1 → 화 1).
	req.Pair.Ilji = PairIljiFacts{Tags: []string{}, Primary: "same"}
	req.Pair.Ohaeng[1] = PairOhaengFact{Name: "hwa", A: "과다", B: "보통", Kind: "plain"}
	req.Pair.Ohaeng[3] = PairOhaengFact{Name: "geum", A: "부족", B: "부족", Kind: "gap"}
	selection, err := SelectPairing(req)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(selection.DeepIDs, "gung-ilji.same") || !slices.Contains(selection.DeepIDs, "gung-ohaeng.gap_geum") ||
		!slices.Contains(selection.DeepIDs, "gung-close.sanggeuk_hap") {
		t.Fatalf("deep IDs = %v", selection.DeepIDs)
	}
}

func TestSelectPairingUnlockMustTargetGunghap(t *testing.T) {
	req := validPairingRequest()
	req.Unlock = &UnlockRequest{Section: "seun", Kind: "ticket"}
	if _, err := SelectPairing(req); platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("seun 섹션: code=%q err=%v", platformerr.CodeOf(err), err)
	}
	req.Unlock = &UnlockRequest{Section: "gunghap", Kind: "reward_claim", ClaimID: "cl_bad/value"}
	if _, err := SelectPairing(req); platformerr.CodeOf(err) != platformerr.CodeContentSelectorInvalid {
		t.Fatalf("잘못된 claim: code=%q err=%v", platformerr.CodeOf(err), err)
	}
	req.Unlock = &UnlockRequest{Section: "gunghap", Kind: "ticket"}
	if _, err := SelectPairing(req); err != nil {
		t.Fatalf("ticket: %v", err)
	}
}

func TestSelectPairingRejectsUnsupportedSchema(t *testing.T) {
	req := validPairingRequest()
	req.SchemaVersion = 2
	if _, err := SelectPairing(req); platformerr.CodeOf(err) != platformerr.CodeContentSchemaMismatch {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}
