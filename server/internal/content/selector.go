package content

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

var selectorNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
var rewardClaimIDPattern = regexp.MustCompile(`^cl_[A-Za-z0-9._-]{1,128}$`)

var stems = []rune("甲乙丙丁戊己庚辛壬癸")
var branches = []rune("子丑寅卯辰巳午未申酉戌亥")
var stemRoman = []string{"gap", "eul", "byeong", "jeong", "mu", "gi", "gyeong", "sin", "im", "gye"}
var branchRoman = []string{"ja", "chuk", "in", "myo", "jin", "sa", "o", "mi", "shin", "yu", "sul", "hae"}
var sipseongRoman = []string{
	"bigyeon", "geopjae", "siksin", "sanggwan", "pyeonjae",
	"jeongjae", "pyeongwan", "jeonggwan", "pyeonin", "jeongin",
}
var unseongRoman = []string{
	"jangsaeng", "mogyok", "gwandae", "geonrok", "jewang", "soe",
	"byeong", "sa", "myo", "jeol", "tae", "yang",
}
var jeonggi = []rune("癸己甲乙戊丙丁己庚辛戊壬")
var jangsaengBranch = []int{11, 6, 2, 9, 2, 9, 5, 0, 8, 3}
var sangsaeng = []int{1, 2, 3, 4, 0}
var sanggeuk = []int{2, 3, 4, 0, 1}
var topics = []string{"seonghyang", "gwangye", "il", "don", "geongang", "heureum"}

var validSipseong = stringSet(
	"bigyeon", "geopjae", "siksin", "sanggwan", "pyeonjae",
	"jeongjae", "pyeongwan", "jeonggwan", "pyeonin", "jeongin",
)
var validUnseong = stringSet(
	"jangsaeng", "mogyok", "gwandae", "geonrok", "jewang", "soe",
	"byeong", "sa", "myo", "jeol", "tae", "yang",
)
var validState = stringSet("과다", "보통", "부족")
var validSinsalVariant = stringSet("nyeonju", "wolju", "ilju", "siju", "outer")
var validSinsal = stringSet(
	"amrok", "baekho_daesal", "banan", "biin", "cheondeok_gwiin", "cheondeok_hap",
	"cheoneul_gwiin", "cheonsa", "cheonsal", "cheonui_seong", "eumyang_chachak", "gasuk",
	"geonrok", "geopsal", "geumyeo", "geupgak", "goegang", "gongmang", "goran", "gosin",
	"gugin_gwiin", "gwangwi_hakgwan", "gwimun_gwansal", "gyeokgak", "hakdang_gwiin",
	"hongyeom", "hwagae", "hyeonchim", "jaesal", "jangseong", "jisal", "mangsin",
	"muncheong_gwiin", "mungok_gwiin", "nakjeong_gwansal", "nyeonsal_dohwa", "samgi_gwiin",
	"sipak_daepae", "taegeuk_gwiin", "woldeok_gwiin", "woldeok_hap", "wolsal", "wonjin",
	"yangin", "yeokma", "yukhae",
)
var validScope = stringSet("base", "seun", "wolun")

type Selection struct {
	ReadingKey string
	BaseIDs    []string
	// OptionalBaseIDs는 계산상 좌표는 만들 수 있지만 모든 신살에 자리별
	// 변형 문구가 존재하지는 않는 spos 축이다.
	OptionalBaseIDs []string
	DeepIDs         map[string][]string
	Scope           map[string]bool
}

func Select(req ResolveRequest) (Selection, error) {
	if req.SchemaVersion != SupportedSchemaVersion {
		return Selection{}, platformerr.New(platformerr.CodeContentSchemaMismatch,
			"지원하지 않는 콘텐츠 요청 스키마예요")
	}
	reading, err := normalizeReading(req.Reading)
	if err != nil {
		return Selection{}, err
	}
	scope, err := validateScope(req.Scope, req.Unlock)
	if err != nil {
		return Selection{}, err
	}

	base := newIDSet()
	optionalBase := newIDSet()
	deep := map[string]*idSet{"seun": newIDSet(), "wolun": newIDSet()}
	base.Add("ilju." + reading.Ilju)
	for _, topic := range topics {
		base.Add("topic." + reading.Ilju + "_" + topic)
	}
	for _, fact := range reading.Johap {
		base.Add("johap." + fact.Sipseong + "_" + fact.Unseong)
	}
	for _, fact := range reading.Sinsal {
		base.Add("sinsal." + fact.Name)
		if fact.Variant != "" {
			optionalBase.Add("spos." + fact.Name + "_" + fact.Variant)
		}
	}
	for _, fact := range reading.Relations {
		id, err := relationID(fact)
		if err != nil {
			return Selection{}, err
		}
		if id != "" {
			base.Add(id)
		}
	}
	for _, fact := range reading.Daeun {
		base.Add("daeun." + fact.Sipseong + "_" + fact.State)
	}

	deep["seun"].Add("seun." + reading.Seun.Flow.Sipseong + "_" + reading.Seun.Flow.State)
	for _, daeun := range reading.Seun.DaeunSipseong {
		deep["seun"].Add("dsyun." + daeun + "_" + reading.Seun.Flow.Sipseong)
	}
	if reading.Seun.Samjae != "" {
		deep["seun"].Add("seunsal.samjae_" + reading.Seun.Samjae)
	}
	for _, fact := range reading.Wolun {
		deep["wolun"].Add("wolun." + fact.Sipseong + "_" + fact.State)
	}

	canonical, err := json.Marshal(reading)
	if err != nil {
		return Selection{}, platformerr.Wrap(err, platformerr.CodeInternal,
			"리딩 키를 만들지 못했어요")
	}
	sum := sha256.Sum256(canonical)
	return Selection{
		ReadingKey: "rk_" + hex.EncodeToString(sum[:]),
		BaseIDs:    base.Sorted(), OptionalBaseIDs: optionalBase.Sorted(),
		DeepIDs: map[string][]string{
			"seun":  deep["seun"].Sorted(),
			"wolun": deep["wolun"].Sorted(),
		},
		Scope: scope,
	}, nil
}

func normalizeReading(in DerivedReadingFacts) (DerivedReadingFacts, error) {
	if in.Kind != "full" && in.Kind != "three_pillar" {
		return in, selectorError("명식 종류가 올바르지 않다")
	}
	chart := []string{in.Chart.Year, in.Chart.Month, in.Chart.Day}
	if in.Kind == "full" {
		if in.Chart.Hour == "" {
			return in, selectorError("네 기둥 명식에 시주가 없다")
		}
		chart = append(chart, in.Chart.Hour)
	} else if in.Chart.Hour != "" {
		return in, selectorError("시각 미상 명식에 시주가 있다")
	}
	for _, pillar := range chart {
		if !validGanji(pillar) {
			return in, selectorError("간지 조합이 올바르지 않다")
		}
	}
	wantIlju, _ := romanGanji(in.Chart.Day)
	if in.Ilju != wantIlju {
		return in, selectorError("일주와 파생 좌표가 일치하지 않는다")
	}
	wantJohap := 4
	if in.Kind == "three_pillar" {
		wantJohap = 3
	}
	if len(in.Johap) != wantJohap {
		return in, selectorError("조합 해설 개수가 명식과 일치하지 않는다")
	}
	for index, fact := range in.Johap {
		if err := validateJohap(fact, chart[index], in.Chart.Day); err != nil {
			return in, err
		}
	}
	if len(in.Daeun) < 1 || len(in.Daeun) > 2 {
		return in, selectorError("대운 해설 갈래 수가 올바르지 않다")
	}
	for _, fact := range in.Daeun {
		if err := validateFlow(fact); err != nil {
			return in, err
		}
	}
	if in.Seun.Year < 1900 || in.Seun.Year > 2200 || len(in.Seun.DaeunSipseong) < 1 ||
		len(in.Seun.DaeunSipseong) > 2 {
		return in, selectorError("세운 파생 사실이 올바르지 않다")
	}
	if err := validateFlow(in.Seun.Flow); err != nil {
		return in, err
	}
	for _, value := range in.Seun.DaeunSipseong {
		if !validSipseong[value] {
			return in, selectorError("세운과 겹치는 대운 십성이 올바르지 않다")
		}
	}
	if in.Seun.Samjae != "" && in.Seun.Samjae != "in" && in.Seun.Samjae != "mid" && in.Seun.Samjae != "out" {
		return in, selectorError("삼재 단계가 올바르지 않다")
	}
	if len(in.Wolun) != 12 {
		return in, selectorError("월운은 절기월 열두 개여야 한다")
	}
	for _, fact := range in.Wolun {
		if err := validateFlow(fact); err != nil {
			return in, err
		}
	}

	if len(in.Sinsal) > 24 || len(in.Relations) > 20 {
		return in, selectorError("파생 항목 수가 상한을 넘는다")
	}
	// 시각 미상 계산은 네 지지가 필요한 신살을 확정하지 않는다. 앱도 빈 배열을 보내며,
	// 여기서 값을 받으면 클라이언트가 만든 신살 좌표를 그대로 신뢰하게 된다.
	if in.Kind == "three_pillar" && len(in.Sinsal) != 0 {
		return in, selectorError("시각 미상 명식에는 신살 파생 사실을 넣을 수 없다")
	}
	seenSinsal := map[string]bool{}
	for _, fact := range in.Sinsal {
		if !selectorNamePattern.MatchString(fact.Name) || !validSinsal[fact.Name] || seenSinsal[fact.Name] ||
			(fact.Variant != "" && !validSinsalVariant[fact.Variant]) {
			return in, selectorError("신살 파생 사실이 올바르지 않다")
		}
		seenSinsal[fact.Name] = true
	}
	chartBranches := []rune{[]rune(in.Chart.Year)[1], []rune(in.Chart.Month)[1], []rune(in.Chart.Day)[1]}
	if in.Chart.Hour != "" {
		chartBranches = append(chartBranches, []rune(in.Chart.Hour)[1])
	}
	seenRelation := map[string]bool{}
	for _, fact := range in.Relations {
		if err := validateRelation(fact, chartBranches); err != nil {
			return in, err
		}
		key := fact.Kind + "\x00" + fact.Pair
		if seenRelation[key] {
			return in, selectorError("관계 파생 사실이 중복됐다")
		}
		seenRelation[key] = true
	}

	out := in
	out.Sinsal = append([]SinsalFact(nil), in.Sinsal...)
	out.Relations = append([]RelationFact(nil), in.Relations...)
	sort.Slice(out.Sinsal, func(i, j int) bool {
		return out.Sinsal[i].Name+"\x00"+out.Sinsal[i].Variant < out.Sinsal[j].Name+"\x00"+out.Sinsal[j].Variant
	})
	sort.Slice(out.Relations, func(i, j int) bool {
		return out.Relations[i].Kind+"\x00"+out.Relations[i].Pair < out.Relations[j].Kind+"\x00"+out.Relations[j].Pair
	})
	return out, nil
}

func validateScope(values []string, unlock *UnlockRequest) (map[string]bool, error) {
	if len(values) == 0 || len(values) > 3 {
		return nil, selectorError("조회 범위가 올바르지 않다")
	}
	out := make(map[string]bool, len(values))
	for _, value := range values {
		if !validScope[value] || out[value] {
			return nil, selectorError("조회 범위가 올바르지 않다")
		}
		out[value] = true
	}
	if unlock == nil {
		return out, nil
	}
	if (unlock.Section != "seun" && unlock.Section != "wolun") || !out[unlock.Section] {
		return nil, selectorError("잠금 해제 대상이 조회 범위에 없다")
	}
	switch unlock.Kind {
	case "reward_claim":
		if !rewardClaimIDPattern.MatchString(unlock.ClaimID) {
			return nil, selectorError("광고 claim ID가 올바르지 않다")
		}
	case "ticket":
		if unlock.ClaimID != "" {
			return nil, selectorError("열람권 요청에 광고 claim을 넣을 수 없다")
		}
	default:
		return nil, selectorError("지원하지 않는 잠금 해제 수단이다")
	}
	return out, nil
}

func validateJohap(f JohapFact, pillar, dayPillar string) error {
	if !validSipseong[f.Sipseong] || !validUnseong[f.Unseong] {
		return selectorError("십성-12운성 조합이 올바르지 않다")
	}
	pillarRunes := []rune(pillar)
	dayRunes := []rune(dayPillar)
	if len(pillarRunes) != 2 || len(dayRunes) != 2 {
		return selectorError("십성-12운성 조합의 명식이 올바르지 않다")
	}
	branchIndex := runeIndex(branches, pillarRunes[1])
	dayStemIndex := runeIndex(stems, dayRunes[0])
	if branchIndex < 0 || dayStemIndex < 0 {
		return selectorError("십성-12운성 조합의 명식이 올바르지 않다")
	}
	expectedSipseong := sipseongForStems(dayStemIndex, runeIndex(stems, jeonggi[branchIndex]))
	step := 1
	if dayStemIndex%2 != 0 {
		step = -1
	}
	unseongIndex := positiveMod((branchIndex-jangsaengBranch[dayStemIndex])*step, len(branches))
	if f.Sipseong != sipseongRoman[expectedSipseong] || f.Unseong != unseongRoman[unseongIndex] {
		return selectorError("십성-12운성 조합이 명식과 일치하지 않는다")
	}
	return nil
}

func sipseongForStems(dayStemIndex, otherStemIndex int) int {
	mine, theirs := dayStemIndex/2, otherStemIndex/2
	group := 4 // 인성 — 나를 생한다.
	switch {
	case theirs == mine:
		group = 0
	case theirs == sangsaeng[mine]:
		group = 1
	case theirs == sanggeuk[mine]:
		group = 2
	case sanggeuk[theirs] == mine:
		group = 3
	}
	polarity := 0
	if dayStemIndex%2 != otherStemIndex%2 {
		polarity = 1
	}
	return group*2 + polarity
}

func positiveMod(value, modulus int) int {
	return ((value % modulus) + modulus) % modulus
}

func validateFlow(f FlowFact) error {
	if !validSipseong[f.Sipseong] || !validState[f.State] {
		return selectorError("흐름 파생 사실이 올바르지 않다")
	}
	return nil
}

func validateRelation(f RelationFact, chartBranches []rune) error {
	allowed := stringSet("yukhap", "samhap", "banghap", "chung", "hyeong", "jahyeong", "pa", "hae", "wonjin", "gwimun")
	if !allowed[f.Kind] {
		return selectorError("관계 종류가 올바르지 않다")
	}
	if f.Pair == "" && (f.Kind == "samhap" || f.Kind == "banghap") {
		if validRelationGroup(f.Kind, chartBranches) {
			return nil
		}
		return selectorError("명식에 성립하지 않는 지지 국이다")
	}
	if utf8.RuneCountInString(f.Pair) != 2 {
		return selectorError("관계 쌍이 필요하다")
	}
	pair := []rune(f.Pair)
	if len(pair) != 2 || runeIndex(branches, pair[0]) < 0 || runeIndex(branches, pair[1]) < 0 {
		return selectorError("명식에 없는 지지 관계다")
	}
	counts := map[rune]int{}
	for _, branch := range chartBranches {
		counts[branch]++
	}
	if counts[pair[0]] == 0 || counts[pair[1]] == 0 {
		return selectorError("명식에 없는 지지 관계다")
	}
	if pair[0] == pair[1] && counts[pair[0]] < 2 {
		return selectorError("명식에 한 번뿐인 지지를 중복 관계로 보냈다")
	}
	if !validRelationPair(f.Kind, pair[0], pair[1]) {
		return selectorError("지지 쌍과 관계 종류가 일치하지 않는다")
	}
	return nil
}

func validRelationGroup(kind string, chart []rune) bool {
	groups := []string{}
	switch kind {
	case "samhap":
		groups = []string{"申子辰", "亥卯未", "寅午戌", "巳酉丑"}
	case "banghap":
		groups = []string{"寅卯辰", "巳午未", "申酉戌", "亥子丑"}
	}
	present := map[rune]bool{}
	for _, branch := range chart {
		present[branch] = true
	}
	for _, group := range groups {
		matched := true
		for _, branch := range group {
			if !present[branch] {
				matched = false
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func validRelationPair(kind string, a, b rune) bool {
	pair := string([]rune{a, b})
	reverse := string([]rune{b, a})
	containsPair := func(values string) bool {
		return strings.Contains(values, pair) || strings.Contains(values, reverse)
	}
	switch kind {
	case "yukhap":
		return containsPair("子丑 寅亥 卯戌 辰酉 巳申 午未")
	case "chung":
		return containsPair("子午 丑未 寅申 卯酉 辰戌 巳亥")
	case "pa":
		return containsPair("子酉 丑辰 寅亥 卯午 巳申 未戌")
	case "hae":
		return containsPair("子未 丑午 寅巳 卯辰 申亥 酉戌")
	case "wonjin":
		return containsPair("子未 丑午 寅酉 卯申 辰亥 巳戌")
	case "gwimun":
		return containsPair("子酉 丑午 寅未 卯申 辰亥 巳戌")
	case "hyeong":
		return a != b && (sameRelationGroup(a, b, "寅巳申") ||
			sameRelationGroup(a, b, "丑戌未") || sameRelationGroup(a, b, "子卯"))
	case "samhap":
		return a != b && (sameRelationGroup(a, b, "申子辰") ||
			sameRelationGroup(a, b, "亥卯未") || sameRelationGroup(a, b, "寅午戌") ||
			sameRelationGroup(a, b, "巳酉丑"))
	case "banghap":
		return a != b && (sameRelationGroup(a, b, "寅卯辰") ||
			sameRelationGroup(a, b, "巳午未") || sameRelationGroup(a, b, "申酉戌") ||
			sameRelationGroup(a, b, "亥子丑"))
	case "jahyeong":
		return a == b && strings.ContainsRune("辰午酉亥", a)
	default:
		return false
	}
}

func sameRelationGroup(a, b rune, group string) bool {
	return strings.ContainsRune(group, a) && strings.ContainsRune(group, b)
}

func relationID(f RelationFact) (string, error) {
	switch f.Kind {
	case "samhap", "banghap", "jahyeong":
		return "hapchung." + f.Kind, nil
	case "wonjin":
		return "sinsal.wonjin", nil
	case "gwimun":
		return "sinsal.gwimun_gwansal", nil
	}
	pair := []rune(f.Pair)
	if len(pair) != 2 {
		return "", selectorError("관계 쌍이 올바르지 않다")
	}
	if f.Kind == "hyeong" {
		for _, group := range []struct{ members, suffix string }{
			{"寅巳申", "in_sa_shin"}, {"丑戌未", "chuk_sul_mi"}, {"子卯", "ja_myo"},
		} {
			if strings.ContainsRune(group.members, pair[0]) && strings.ContainsRune(group.members, pair[1]) {
				return "hapchung.hyeong_" + group.suffix, nil
			}
		}
		return "", selectorError("형 관계 묶음이 올바르지 않다")
	}
	ai, bi := runeIndex(branches, pair[0]), runeIndex(branches, pair[1])
	if ai < 0 || bi < 0 {
		return "", selectorError("관계 지지가 올바르지 않다")
	}
	if ai > bi {
		ai, bi = bi, ai
	}
	prefix := map[string]string{"yukhap": "yukhap", "chung": "chung", "pa": "pa", "hae": "hae"}[f.Kind]
	if prefix == "" {
		return "", nil
	}
	return fmt.Sprintf("hapchung.%s_%s_%s", prefix, branchRoman[ai], branchRoman[bi]), nil
}

func validGanji(value string) bool {
	runes := []rune(value)
	if len(runes) != 2 {
		return false
	}
	si, bi := runeIndex(stems, runes[0]), runeIndex(branches, runes[1])
	return si >= 0 && bi >= 0 && si%2 == bi%2
}

func romanGanji(value string) (string, bool) {
	runes := []rune(value)
	if len(runes) != 2 {
		return "", false
	}
	si, bi := runeIndex(stems, runes[0]), runeIndex(branches, runes[1])
	if si < 0 || bi < 0 {
		return "", false
	}
	return stemRoman[si] + branchRoman[bi], true
}

func runeIndex(values []rune, want rune) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}

func stringSet(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

type idSet map[string]struct{}

func newIDSet() *idSet {
	value := idSet{}
	return &value
}

func (s *idSet) Add(id string) { (*s)[id] = struct{}{} }

func (s *idSet) Sorted() []string {
	out := make([]string, 0, len(*s))
	for id := range *s {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func selectorError(message string) error {
	return platformerr.New(platformerr.CodeContentSelectorInvalid, message)
}
