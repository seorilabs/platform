package content

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// 궁합은 한 명식 쌍이 한 열람 단위다. 세운·월운의 flow:{year}와 달리 연도가 없으므로
// deepKey와 section이 같은 고정 문자열이다.
const (
	pairingDeepKey = "gunghap"
	pairingSection = "gunghap"
)

// 지지 오행 — 子丑寅卯辰巳午未申酉戌亥 → 목0 화1 토2 금3 수4. 천간은 인덱스/2가 오행이다.
var branchOhaeng = []int{4, 2, 0, 0, 2, 1, 1, 2, 3, 3, 2, 4}
var ohaengRoman = []string{"mok", "hwa", "to", "geum", "su"}

// 앱 계산 코어(app/src/calc/pairing.ts)와 같은 고정 순서다. 태그 배열을 순서까지 대조하므로
// 한쪽만 바꾸면 모든 궁합 요청이 selector_invalid가 된다.
var pairRelationOrder = []string{"yukhap", "samhap", "banghap", "chung", "hyeong", "pa", "hae", "wonjin", "gwimun"}

// 대표 관계 우선순위 — 부딪힘을 합보다 먼저 둔다. 巳申(육합·형·파)처럼 겹칠 때 읽는 이가
// 먼저 알아야 할 쪽이 그것이기 때문이다.
var pairPrimaryOrder = []string{"chung", "wonjin", "gwimun", "hyeong", "yukhap", "samhap", "banghap", "pa", "hae"}

var hapPrimaries = stringSet("yukhap", "samhap", "banghap", "same")
var chungPrimaries = stringSet("chung", "hyeong", "pa", "hae", "wonjin", "gwimun")

type PairingSelection struct {
	// PairKey는 두 명식 해시를 정렬해 다시 해시한 값이라 A×B와 B×A가 같다.
	PairKey string
	// DeepIDs는 궁합 좌표 전부다. 궁합에는 무료 본문이 없다.
	DeepIDs []string
}

// pairingSide는 요청 한쪽을 유도에 필요한 인덱스로 정규화한 것이다.
type pairingSide struct {
	dayStem   int
	dayBranch int
	ohaeng    [5]int
}

// SelectPairing은 두 명식에서 짝 사실을 전부 다시 유도해 요청의 pair와 대조한다.
//
// readings:resolve의 Select와 같은 원칙이다 — 앱이 보낸 값을 좌표로 믿지 않는다. 앱은
// 계산 결과를 그대로 보내지만 서버는 그것을 "검산 대상"으로만 쓴다. 하나라도 다르면
// 좌표를 만들지 않고 selector_invalid로 거절한다.
func SelectPairing(req ResolvePairingRequest) (PairingSelection, error) {
	if req.SchemaVersion != SupportedSchemaVersion {
		return PairingSelection{}, platformerr.New(platformerr.CodeContentSchemaMismatch,
			"지원하지 않는 콘텐츠 요청 스키마예요")
	}
	sideA, err := normalizePairingSide(req.A)
	if err != nil {
		return PairingSelection{}, err
	}
	sideB, err := normalizePairingSide(req.B)
	if err != nil {
		return PairingSelection{}, err
	}
	derived := derivePairFacts(sideA, sideB)
	if !samePairFacts(derived, req.Pair) {
		return PairingSelection{}, selectorError("짝 사실이 두 명식에서 다시 유도한 값과 다르다")
	}
	if req.Unlock != nil {
		if req.Unlock.Section != pairingSection {
			return PairingSelection{}, selectorError("잠금 해제 대상이 궁합이 아니다")
		}
		if err := validateUnlockMeans(*req.Unlock); err != nil {
			return PairingSelection{}, err
		}
	}
	pairKey, err := pairingKey(req.A, req.B)
	if err != nil {
		return PairingSelection{}, err
	}
	return PairingSelection{PairKey: pairKey, DeepIDs: pairingDeepIDs(derived)}, nil
}

func normalizePairingSide(in PairingSideFacts) (pairingSide, error) {
	if in.Kind != "full" && in.Kind != "three_pillar" {
		return pairingSide{}, selectorError("명식 종류가 올바르지 않다")
	}
	chart := []string{in.Chart.Year, in.Chart.Month, in.Chart.Day}
	if in.Kind == "full" {
		if in.Chart.Hour == "" {
			return pairingSide{}, selectorError("네 기둥 명식에 시주가 없다")
		}
		chart = append(chart, in.Chart.Hour)
	} else if in.Chart.Hour != "" {
		return pairingSide{}, selectorError("시각 미상 명식에 시주가 있다")
	}
	var side pairingSide
	for _, pillar := range chart {
		if !validGanji(pillar) {
			return pairingSide{}, selectorError("간지 조합이 올바르지 않다")
		}
		runes := []rune(pillar)
		stem, branch := runeIndex(stems, runes[0]), runeIndex(branches, runes[1])
		// 오행 분포는 지지 자체의 오행으로 센다(정기가 아니라) — 앱 pillars.ohaengCounts와 같다.
		side.ohaeng[stem/2]++
		side.ohaeng[branchOhaeng[branch]]++
	}
	dayRunes := []rune(in.Chart.Day)
	side.dayStem = runeIndex(stems, dayRunes[0])
	side.dayBranch = runeIndex(branches, dayRunes[1])
	return side, nil
}

func derivePairFacts(a, b pairingSide) PairFacts {
	ilji := derivePairBranch(a.dayBranch, b.dayBranch)
	facts := PairFacts{
		Ilgan: PairIlganFacts{
			// aToB는 A가 B에게 무엇인가 — B의 일간에서 본 A의 일간 십성이다.
			AToB: sipseongRoman[sipseongForStems(b.dayStem, a.dayStem)],
			BToA: sipseongRoman[sipseongForStems(a.dayStem, b.dayStem)],
			// 천간합 甲己·乙庚·丙辛·丁壬·戊癸는 인덱스 차가 5인 쌍이다.
			Hap: (a.dayStem+5)%10 == b.dayStem,
		},
		Ilji:   ilji,
		Ohaeng: make([]PairOhaengFact, 0, 5),
		Close: PairCloseFacts{
			Stem:   ilganGroup(a.dayStem/2, b.dayStem/2),
			Branch: closeBranch(ilji.Primary),
		},
	}
	for index, name := range ohaengRoman {
		stateA, stateB := ohaengStateOf(a.ohaeng[index]), ohaengStateOf(b.ohaeng[index])
		facts.Ohaeng = append(facts.Ohaeng, PairOhaengFact{
			Name: name, A: stateA, B: stateB, Kind: fillKind(stateA, stateB),
		})
	}
	return facts
}

// derivePairBranch는 두 일지의 관계 태그와 대표 관계다. 삼합·방합은 두 글자 반합으로
// 성립하고, 자형은 한 명식 안의 관계라 두 사람 사이에서는 내지 않는다.
func derivePairBranch(a, b int) PairIljiFacts {
	tags := make([]string, 0, 3)
	for _, kind := range pairRelationOrder {
		if validRelationPair(kind, branches[a], branches[b]) {
			tags = append(tags, kind)
		}
	}
	primary := ""
	for _, kind := range pairPrimaryOrder {
		if slices.Contains(tags, kind) {
			primary = kind
			break
		}
	}
	if primary == "" {
		primary = "none"
		if a == b {
			primary = "same"
		}
	}
	return PairIljiFacts{Tags: tags, Primary: primary}
}

func ilganGroup(ohaengA, ohaengB int) string {
	switch {
	case ohaengA == ohaengB:
		return "bihwa"
	case sangsaeng[ohaengA] == ohaengB || sangsaeng[ohaengB] == ohaengA:
		return "sangsaeng"
	default:
		return "sanggeuk"
	}
}

func closeBranch(primary string) string {
	switch {
	case hapPrimaries[primary]:
		return "hap"
	case chungPrimaries[primary]:
		return "chung"
	default:
		return "none"
	}
}

// ohaengStateOf는 리딩(대운·세운·월운)과 같은 임계값이다 — 개수 4 이상 과다, 0 부족.
func ohaengStateOf(count int) string {
	switch {
	case count >= 4:
		return "과다"
	case count == 0:
		return "부족"
	default:
		return "보통"
	}
}

// fillKind에서 fill_b는 B가 A의 빈 자리를 채운다는 뜻이다.
func fillKind(stateA, stateB string) string {
	switch {
	case stateA == "부족" && stateB == "과다":
		return "fill_b"
	case stateA == "과다" && stateB == "부족":
		return "fill_a"
	case stateA == "과다" && stateB == "과다":
		return "overlap"
	case stateA == "부족" && stateB == "부족":
		return "gap"
	default:
		return "plain"
	}
}

func samePairFacts(derived, given PairFacts) bool {
	if derived.Ilgan != given.Ilgan || derived.Close != given.Close {
		return false
	}
	if derived.Ilji.Primary != given.Ilji.Primary || !slices.Equal(derived.Ilji.Tags, given.Ilji.Tags) {
		return false
	}
	return slices.Equal(derived.Ohaeng, given.Ohaeng)
}

// pairingDeepIDs는 궁합 축 46좌표 중 이 쌍에 성립하는 것이다. 양방향 십성은 같은 풀
// (`gung-ilgan.{십성}`)을 두 번 쓰고, 채움은 방향과 무관하게 한 좌표다 — 방향은 앱이
// pair.ohaeng의 kind로 문장 앞에 붙인다.
func pairingDeepIDs(facts PairFacts) []string {
	ids := newIDSet()
	ids.Add("gung-ilgan." + facts.Ilgan.AToB)
	ids.Add("gung-ilgan." + facts.Ilgan.BToA)
	if facts.Ilgan.Hap {
		ids.Add("gung-ilgan.hap")
	}
	ids.Add("gung-ilji." + facts.Ilji.Primary)
	for _, row := range facts.Ohaeng {
		switch row.Kind {
		case "fill_a", "fill_b":
			ids.Add("gung-ohaeng.fill_" + row.Name)
		case "overlap", "gap":
			ids.Add("gung-ohaeng." + row.Kind + "_" + row.Name)
		}
	}
	ids.Add("gung-close." + facts.Close.Stem + "_" + facts.Close.Branch)
	return ids.Sorted()
}

// pairingKey는 두 명식 해시를 정렬해 다시 해시한다. 정렬 덕에 어느 쪽을 A로 두든 같은
// 키가 나와 일일 한도·해제 원장이 한 쌍을 한 번만 센다.
func pairingKey(a, b PairingSideFacts) (string, error) {
	first, err := pairingSideDigest(a)
	if err != nil {
		return "", err
	}
	second, err := pairingSideDigest(b)
	if err != nil {
		return "", err
	}
	if second < first {
		first, second = second, first
	}
	sum := sha256.Sum256([]byte(first + second))
	return "pk_" + hex.EncodeToString(sum[:]), nil
}

func pairingSideDigest(side PairingSideFacts) (string, error) {
	canonical, err := json.Marshal(side)
	if err != nil {
		return "", platformerr.Wrap(err, platformerr.CodeInternal, "궁합 키를 만들지 못했어요")
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
