package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/registry"
)

type readingKeyFixture struct {
	Name        string         `json:"name"`
	Affected    bool           `json:"affected"`
	ExistingKey string         `json:"existingKey"`
	Request     ResolveRequest `json:"request"`
}

func readingKeyFixtures(t *testing.T) []readingKeyFixture {
	t.Helper()
	var fixtures []readingKeyFixture
	if err := json.Unmarshal([]byte(readingKeyAppRequests), &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func cloneReadingKeyRequest(t *testing.T, req ResolveRequest) ResolveRequest {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var cloned ResolveRequest
	if err := json.Unmarshal(data, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func withWonjinVariant(t *testing.T, req ResolveRequest, variant string) ResolveRequest {
	t.Helper()
	out := cloneReadingKeyRequest(t, req)
	for i := range out.Reading.Sinsal {
		if out.Reading.Sinsal[i].Name == "wonjin" {
			out.Reading.Sinsal[i].Variant = variant
			return out
		}
	}
	out.Reading.Sinsal = append(out.Reading.Sinsal, SinsalFact{Name: "wonjin", Variant: variant})
	return out
}

// 수정 전 공개 규칙은 정렬한 요청 전체를 그대로 해시했다. 별도 경로에서 재현하여
// 현재의 Select끼리 같은 잘못된 키를 내더라도 회귀 검사가 통과하지 않게 한다.
func publishedReadingKey(t *testing.T, reading DerivedReadingFacts) string {
	t.Helper()
	normalized, err := normalizeReading(reading)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return "rk_" + hex.EncodeToString(sum[:])
}

func TestReadingKeyPreservesExistingWonjinUnlockWhileSelectingCorrectPosition(t *testing.T) {
	for _, fixture := range readingKeyFixtures(t) {
		if !fixture.Affected {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			current := fixture.Request
			original := cloneReadingKeyRequest(t, current)
			old := withWonjinVariant(t, current, "outer")
			if got := publishedReadingKey(t, old.Reading); got != fixture.ExistingKey {
				t.Fatalf("published key = %s, want %s", got, fixture.ExistingKey)
			}
			if publishedReadingKey(t, current.Reading) == fixture.ExistingKey {
				t.Fatal("위치 수정으로 키가 바뀌는 실제 요청이어야 한다")
			}
			oldSelection, err := Select(old)
			if err != nil {
				t.Fatal(err)
			}
			selection, err := Select(current)
			if err != nil {
				t.Fatal(err)
			}
			if oldSelection.ReadingKey != fixture.ExistingKey || selection.ReadingKey != fixture.ExistingKey {
				t.Fatalf("old/new key = %s / %s, want %s", oldSelection.ReadingKey, selection.ReadingKey, fixture.ExistingKey)
			}
			if !reflect.DeepEqual(current, original) {
				t.Fatal("키 보존이 원본 요청을 변경했다")
			}
			if !slices.Contains(oldSelection.OptionalBaseIDs, "spos.wonjin_outer") || slices.Contains(oldSelection.OptionalBaseIDs, "spos.wonjin_ilju") {
				t.Fatalf("old optional = %v", oldSelection.OptionalBaseIDs)
			}
			if !slices.Contains(selection.OptionalBaseIDs, "spos.wonjin_ilju") || slices.Contains(selection.OptionalBaseIDs, "spos.wonjin_outer") {
				t.Fatalf("new optional = %v", selection.OptionalBaseIDs)
			}
			if !reflect.DeepEqual(oldSelection.BaseIDs, selection.BaseIDs) || !reflect.DeepEqual(oldSelection.DeepIDs, selection.DeepIDs) {
				t.Fatal("원진 위치 외의 본문 선택이 바뀌었다")
			}
			otherOptional := func(ids []string, omitted string) []string {
				out := make([]string, 0, len(ids))
				for _, id := range ids {
					if id != omitted {
						out = append(out, id)
					}
				}
				return out
			}
			if !slices.Equal(otherOptional(oldSelection.OptionalBaseIDs, "spos.wonjin_outer"),
				otherOptional(selection.OptionalBaseIDs, "spos.wonjin_ilju")) {
				t.Fatal("원진 위치 외의 선택 원고가 바뀌었다")
			}
			changed := cloneReadingKeyRequest(t, current)
			changed.Reading.Seun.Year++
			changedSelection, err := Select(changed)
			if err != nil {
				t.Fatal(err)
			}
			if changedSelection.ReadingKey == selection.ReadingKey {
				t.Fatal("다른 연도 사실까지 같은 키로 합쳤다")
			}
		})
	}
}

func TestReadingKeyKeepsUnaffectedAppRequestsAndDoesNotAliasForgedDayPosition(t *testing.T) {
	for _, fixture := range readingKeyFixtures(t) {
		if fixture.Affected {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			selection, err := Select(fixture.Request)
			if err != nil {
				t.Fatal(err)
			}
			if selection.ReadingKey != fixture.ExistingKey || selection.ReadingKey != publishedReadingKey(t, fixture.Request.Reading) {
				t.Fatalf("unaffected key = %s, want %s", selection.ReadingKey, fixture.ExistingKey)
			}
			if fixture.Name != "outer-only" && fixture.Name != "no-wonjin" {
				return
			}
			forged := withWonjinVariant(t, fixture.Request, "ilju")
			forgedSelection, err := Select(forged)
			if err != nil {
				t.Fatal(err)
			}
			if forgedSelection.ReadingKey != publishedReadingKey(t, forged.Reading) {
				t.Fatal("일지가 참여하는 뒤의 성립 쌍이 없는데 ilju를 outer로 정규화했다")
			}
			outer := withWonjinVariant(t, fixture.Request, "outer")
			if forgedSelection.ReadingKey == publishedReadingKey(t, outer.Reading) {
				t.Fatal("위조한 일주 위치가 기존 일주 밖 열람 키를 재사용했다")
			}
		})
	}
}

type readingKeyUsage struct {
	doc     usageDoc
	checked []string
}

func (u *readingKeyUsage) AllowReading(_ context.Context, app registry.App, _ string, key string) error {
	u.checked = append(u.checked, key)
	return allowReadingKey(&u.doc, app.Content.ReadingDailyLimit, key)
}
func (*readingKeyUsage) AllowTerm(context.Context, registry.App, string) error { return nil }

type readingKeyUnlocks struct {
	fakeUnlocks
	key     string
	checked []string
}

func (u *readingKeyUnlocks) GetUnlock(_ context.Context, _, _, key, deepKey string) (UnlockGrant, error) {
	u.checked = append(u.checked, key+"/"+deepKey)
	if key == u.key && deepKey == "flow:2026" {
		return UnlockGrant{Exists: true, Source: "ticket", Reference: strings.Repeat("b", 64)}, nil
	}
	return UnlockGrant{}, nil
}

func TestResolveRetainsWonjinTicketGrantAndDailyReadingSlot(t *testing.T) {
	for _, fixture := range readingKeyFixtures(t) {
		if !fixture.Affected {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			current := fixture.Request
			current.Unlock = &UnlockRequest{Section: "seun", Kind: "ticket"}
			old := withWonjinVariant(t, current, "outer")
			app := testContentApp()
			app.Content.ReadingDailyLimit = 1
			app.Content.TicketEntitlementID = "deep-ticket"
			app.Content.TicketUnitsPerPurchase = 3
			release := serviceRelease(t, current)
			release.Items["spos.wonjin_outer"] = Item{ID: "spos.wonjin_outer", Text: "일주 밖 해설", Access: AccessFree, Contexts: []Context{ContextReading}}
			usage := &readingKeyUsage{doc: usageDoc{ReadingKeys: map[string]bool{fixture.ExistingKey: true}}}
			unlocks := &readingKeyUnlocks{key: fixture.ExistingKey}
			entitlements := &fakeEntitlements{sourceActive: true}
			access := NewAccessService(unlocks, nil, entitlements)
			service, err := NewService(fakeApps{app}, fakeReleases{release}, usage, access)
			if err != nil {
				t.Fatal(err)
			}
			for _, req := range []ResolveRequest{old, current} {
				result, err := service.Resolve(t.Context(), "ungeul", "puid", req)
				if err != nil {
					t.Fatal(err)
				}
				if result.ReadingKey != fixture.ExistingKey || len(result.Locked) != 0 {
					t.Fatalf("기존 열람권 재조회 = key %s locked %v", result.ReadingKey, result.Locked)
				}
				want, absent := "spos.wonjin_ilju", "spos.wonjin_outer"
				for _, fact := range req.Reading.Sinsal {
					if fact.Name == "wonjin" && fact.Variant == "outer" {
						want, absent = absent, want
					}
				}
				articleIDs := make([]string, 0, len(result.Articles))
				deep := 0
				for _, article := range result.Articles {
					articleIDs = append(articleIDs, article.ID)
					if article.Access == AccessDeep {
						deep++
					}
				}
				if !slices.Contains(articleIDs, want) || slices.Contains(articleIDs, absent) || deep == 0 {
					t.Fatalf("반환 본문 = %v, want %s and deep", articleIDs, want)
				}
			}
			if entitlements.consumed || unlocks.ticketRecorded {
				t.Fatal("위치 설명 수정 뒤 기존 열람권을 다시 소비했다")
			}
			if len(usage.doc.ReadingKeys) != 1 {
				t.Fatalf("새 일일 명식 수를 소비했다: %v", usage.doc.ReadingKeys)
			}
			for _, key := range usage.checked {
				if key != fixture.ExistingKey {
					t.Fatalf("usage key = %s", key)
				}
			}
			for _, key := range unlocks.checked {
				if key != fixture.ExistingKey+"/flow:2026" {
					t.Fatalf("grant key = %s", key)
				}
			}
		})
	}
}

// 1988년 가상 출생 입력을 실제 앱 computeChart와 buildDerivedReadingFacts로 산출했다.
// 기준일 2026-09-09. affected 세 건은 11-15 11:15, 11-27 11:15, 12-10 15:15이다.
// 기존 키는 원진 outer를 보내던 공개 직렬화 규칙으로 고정했다.
const readingKeyAppRequests = `[
{"name":"affected-11-15","affected":true,"existingKey":"rk_6eca4a61e7866ca705fc7f0281767a3b524767c294ec617aced2a092c1f2eb3e","request":{
"schemaVersion":1,"reading":{
"kind":"full",
"chart":{"year":"戊辰","month":"癸亥","day":"甲戌","hour":"己巳"},
"ilju":"gapsul",
"johap":[{"sipseong":"pyeonjae","unseong":"soe"},{"sipseong":"pyeonin","unseong":"jangsaeng"},{"sipseong":"pyeonjae","unseong":"yang"},{"sipseong":"siksin","unseong":"byeong"}],
"sinsal":[{"name":"amrok","variant":"wolju"},{"name":"baekho_daesal","variant":"nyeonju"},{"name":"cheonui_seong"},{"name":"geopsal","variant":"siju"},{"name":"geumyeo","variant":"nyeonju"},{"name":"geupgak"},{"name":"gosin","variant":"siju"},{"name":"gugin_gwiin","variant":"ilju"},{"name":"gwangwi_hakgwan","variant":"siju"},{"name":"gwimun_gwansal","variant":"ilju"},{"name":"hakdang_gwiin","variant":"wolju"},{"name":"hwagae","variant":"nyeonju"},{"name":"mangsin","variant":"wolju"},{"name":"muncheong_gwiin","variant":"siju"},{"name":"mungok_gwiin","variant":"wolju"},{"name":"nakjeong_gwansal","variant":"siju"},{"name":"woldeok_gwiin"},{"name":"woldeok_hap"},{"name":"wolsal","variant":"ilju"},{"name":"wonjin","variant":"ilju"}],
"relations":[{"kind":"wonjin","pair":"辰亥"},{"kind":"gwimun","pair":"辰亥"},{"kind":"chung","pair":"辰戌"},{"kind":"chung","pair":"亥巳"},{"kind":"wonjin","pair":"戌巳"},{"kind":"gwimun","pair":"戌巳"}],
"daeun":[{"sipseong":"sanggwan","state":"보통"}],
"seun":{"year":2026,"flow":{"sipseong":"siksin","state":"보통"},"daeunSipseong":["sanggwan"]},
"wolun":[{"sipseong":"pyeongwan","state":"부족"},{"sipseong":"jeonggwan","state":"부족"},{"sipseong":"pyeonin","state":"보통"},{"sipseong":"jeongin","state":"보통"},{"sipseong":"bigyeon","state":"보통"},{"sipseong":"geopjae","state":"보통"},{"sipseong":"siksin","state":"보통"},{"sipseong":"sanggwan","state":"보통"},{"sipseong":"pyeonjae","state":"과다"},{"sipseong":"jeongjae","state":"과다"},{"sipseong":"pyeongwan","state":"부족"},{"sipseong":"jeonggwan","state":"부족"}]
},"scope":["base","seun","wolun"]}},
{"name":"affected-11-27","affected":true,"existingKey":"rk_a166ce0007e3efb9b8566beb8c4b7fee83b9c1b4ed3cc26c940dab2bea2f89d6","request":{
"schemaVersion":1,"reading":{
"kind":"full",
"chart":{"year":"戊辰","month":"癸亥","day":"丙戌","hour":"癸巳"},
"ilju":"byeongsul",
"johap":[{"sipseong":"siksin","unseong":"gwandae"},{"sipseong":"pyeongwan","unseong":"jeol"},{"sipseong":"siksin","unseong":"myo"},{"sipseong":"bigyeon","unseong":"geonrok"}],
"sinsal":[{"name":"baekho_daesal","variant":"nyeonju"},{"name":"cheoneul_gwiin","variant":"wolju"},{"name":"cheonui_seong"},{"name":"eumyang_chachak","variant":"siju"},{"name":"geonrok","variant":"siju"},{"name":"geopsal","variant":"siju"},{"name":"geupgak"},{"name":"gosin","variant":"siju"},{"name":"gwimun_gwansal","variant":"ilju"},{"name":"hwagae","variant":"nyeonju"},{"name":"mangsin","variant":"wolju"},{"name":"wolsal","variant":"ilju"},{"name":"wonjin","variant":"ilju"}],
"relations":[{"kind":"wonjin","pair":"辰亥"},{"kind":"gwimun","pair":"辰亥"},{"kind":"chung","pair":"辰戌"},{"kind":"chung","pair":"亥巳"},{"kind":"wonjin","pair":"戌巳"},{"kind":"gwimun","pair":"戌巳"}],
"daeun":[{"sipseong":"geopjae","state":"보통"}],
"seun":{"year":2026,"flow":{"sipseong":"bigyeon","state":"보통"},"daeunSipseong":["geopjae"]},
"wolun":[{"sipseong":"pyeonjae","state":"부족"},{"sipseong":"jeongjae","state":"부족"},{"sipseong":"pyeongwan","state":"보통"},{"sipseong":"jeonggwan","state":"보통"},{"sipseong":"pyeonin","state":"부족"},{"sipseong":"jeongin","state":"부족"},{"sipseong":"bigyeon","state":"보통"},{"sipseong":"geopjae","state":"보통"},{"sipseong":"siksin","state":"보통"},{"sipseong":"sanggwan","state":"보통"},{"sipseong":"pyeonjae","state":"부족"},{"sipseong":"jeongjae","state":"부족"}]
},"scope":["base","seun","wolun"]}},
{"name":"affected-12-10","affected":true,"existingKey":"rk_9076b2bd78d734b902c45605d64574d3110e92b8425af0858bee99a06e27b84e","request":{
"schemaVersion":1,"reading":{
"kind":"full",
"chart":{"year":"戊辰","month":"甲子","day":"己亥","hour":"辛未"},
"ilju":"gihae",
"johap":[{"sipseong":"geopjae","unseong":"soe"},{"sipseong":"pyeonjae","unseong":"jeol"},{"sipseong":"jeongjae","unseong":"tae"},{"sipseong":"bigyeon","unseong":"gwandae"}],
"sinsal":[{"name":"amrok","variant":"siju"},{"name":"baekho_daesal","variant":"nyeonju"},{"name":"banan","variant":"nyeonju"},{"name":"cheoneul_gwiin","variant":"wolju"},{"name":"cheonsal","variant":"siju"},{"name":"cheonui_seong"},{"name":"geupgak"},{"name":"gongmang","variant":"nyeonju"},{"name":"gwangwi_hakgwan","variant":"ilju"},{"name":"gwimun_gwansal","variant":"ilju"},{"name":"hongyeom","variant":"nyeonju"},{"name":"hwagae","variant":"nyeonju"},{"name":"jangseong","variant":"wolju"},{"name":"jisal","variant":"ilju"},{"name":"mangsin","variant":"ilju"},{"name":"nyeonsal_dohwa","variant":"wolju"},{"name":"taegeuk_gwiin","variant":"nyeonju"},{"name":"wonjin","variant":"ilju"},{"name":"yangin","variant":"siju"}],
"relations":[{"kind":"wonjin","pair":"辰亥"},{"kind":"gwimun","pair":"辰亥"},{"kind":"hae","pair":"子未"},{"kind":"wonjin","pair":"子未"}],
"daeun":[{"sipseong":"pyeonin","state":"부족"}],
"seun":{"year":2026,"flow":{"sipseong":"jeongin","state":"부족"},"daeunSipseong":["pyeonin"]},
"wolun":[{"sipseong":"sanggwan","state":"보통"},{"sipseong":"siksin","state":"보통"},{"sipseong":"jeongjae","state":"보통"},{"sipseong":"pyeonjae","state":"보통"},{"sipseong":"jeonggwan","state":"보통"},{"sipseong":"pyeongwan","state":"보통"},{"sipseong":"jeongin","state":"부족"},{"sipseong":"pyeonin","state":"부족"},{"sipseong":"geopjae","state":"과다"},{"sipseong":"bigyeon","state":"과다"},{"sipseong":"sanggwan","state":"보통"},{"sipseong":"siksin","state":"보통"}]
},"scope":["base","seun","wolun"]}},
{"name":"outer-only","affected":false,"existingKey":"rk_d64b5d88b79a65e5309b79b83efa053f86dd7a2db0617006f1e876e8622fb4b0","request":{
"schemaVersion":1,"reading":{
"kind":"full",
"chart":{"year":"丁卯","month":"壬子","day":"乙卯","hour":"癸未"},
"ilju":"eulmyo",
"johap":[{"sipseong":"bigyeon","unseong":"geonrok"},{"sipseong":"pyeonin","unseong":"byeong"},{"sipseong":"bigyeon","unseong":"geonrok"},{"sipseong":"pyeonjae","unseong":"yang"}],
"sinsal":[{"name":"cheoneul_gwiin","variant":"wolju"},{"name":"geonrok","variant":"nyeonju"},{"name":"gongmang","variant":"wolju"},{"name":"hwagae","variant":"siju"},{"name":"jangseong","variant":"nyeonju"},{"name":"mungok_gwiin","variant":"wolju"},{"name":"nakjeong_gwansal","variant":"wolju"},{"name":"nyeonsal_dohwa","variant":"wolju"},{"name":"taegeuk_gwiin","variant":"wolju"},{"name":"woldeok_gwiin"},{"name":"woldeok_hap"},{"name":"wonjin","variant":"outer"}],
"relations":[{"kind":"hyeong","pair":"卯子"},{"kind":"hyeong","pair":"子卯"},{"kind":"hae","pair":"子未"},{"kind":"wonjin","pair":"子未"}],
"daeun":[{"sipseong":"jeongjae","state":"보통"}],
"seun":{"year":2026,"flow":{"sipseong":"sanggwan","state":"보통"},"daeunSipseong":["jeongjae"],"samjae":"mid"},
"wolun":[{"sipseong":"jeonggwan","state":"부족"},{"sipseong":"pyeongwan","state":"부족"},{"sipseong":"jeongin","state":"보통"},{"sipseong":"pyeonin","state":"보통"},{"sipseong":"geopjae","state":"보통"},{"sipseong":"bigyeon","state":"보통"},{"sipseong":"sanggwan","state":"보통"},{"sipseong":"siksin","state":"보통"},{"sipseong":"jeongjae","state":"보통"},{"sipseong":"pyeonjae","state":"보통"},{"sipseong":"jeonggwan","state":"부족"},{"sipseong":"pyeongwan","state":"부족"}]
},"scope":["base","seun","wolun"]}},
{"name":"first-pair-involves-day","affected":false,"existingKey":"rk_c57d048fcf45c78722059181fe2a80f6fb2f44fa67085359cabb5a9eb2b0f43e","request":{
"schemaVersion":1,"reading":{
"kind":"full",
"chart":{"year":"丁卯","month":"壬子","day":"己未","hour":"辛未"},
"ilju":"gimi",
"johap":[{"sipseong":"pyeongwan","unseong":"byeong"},{"sipseong":"pyeonjae","unseong":"jeol"},{"sipseong":"bigyeon","unseong":"gwandae"},{"sipseong":"bigyeon","unseong":"gwandae"}],
"sinsal":[{"name":"amrok","variant":"ilju"},{"name":"cheoneul_gwiin","variant":"wolju"},{"name":"gongmang","variant":"wolju"},{"name":"hwagae","variant":"ilju"},{"name":"jangseong","variant":"nyeonju"},{"name":"mungok_gwiin","variant":"nyeonju"},{"name":"nyeonsal_dohwa","variant":"wolju"},{"name":"taegeuk_gwiin","variant":"ilju"},{"name":"woldeok_gwiin"},{"name":"woldeok_hap"},{"name":"wonjin","variant":"ilju"},{"name":"yangin","variant":"ilju"}],
"relations":[{"kind":"hyeong","pair":"卯子"},{"kind":"hae","pair":"子未"},{"kind":"wonjin","pair":"子未"}],
"daeun":[{"sipseong":"bigyeon","state":"보통"}],
"seun":{"year":2026,"flow":{"sipseong":"jeongin","state":"보통"},"daeunSipseong":["bigyeon"],"samjae":"mid"},
"wolun":[{"sipseong":"sanggwan","state":"보통"},{"sipseong":"siksin","state":"보통"},{"sipseong":"jeongjae","state":"보통"},{"sipseong":"pyeonjae","state":"보통"},{"sipseong":"jeonggwan","state":"보통"},{"sipseong":"pyeongwan","state":"보통"},{"sipseong":"jeongin","state":"보통"},{"sipseong":"pyeonin","state":"보통"},{"sipseong":"geopjae","state":"보통"},{"sipseong":"bigyeon","state":"보통"},{"sipseong":"sanggwan","state":"보통"},{"sipseong":"siksin","state":"보통"}]
},"scope":["base","seun","wolun"]}},
{"name":"no-wonjin","affected":false,"existingKey":"rk_9f13a138440780fab86b7956b6798e663e0ce908461fabc4a3895d69dc437b8e","request":{
"schemaVersion":1,"reading":{
"kind":"full",
"chart":{"year":"丁卯","month":"癸丑","day":"辛酉","hour":"乙未"},
"ilju":"sinyu",
"johap":[{"sipseong":"pyeonjae","unseong":"jeol"},{"sipseong":"pyeonin","unseong":"yang"},{"sipseong":"bigyeon","unseong":"geonrok"},{"sipseong":"pyeonin","unseong":"soe"}],
"sinsal":[{"name":"baekho_daesal","variant":"wolju"},{"name":"cheondeok_hap"},{"name":"eumyang_chachak","variant":"ilju"},{"name":"gasuk","variant":"wolju"},{"name":"geonrok","variant":"ilju"},{"name":"geupgak"},{"name":"gongmang","variant":"wolju"},{"name":"hongyeom","variant":"ilju"},{"name":"hwagae","variant":"siju"},{"name":"jaesal","variant":"ilju"},{"name":"jangseong","variant":"nyeonju"},{"name":"woldeok_hap"},{"name":"wolsal","variant":"wolju"}],
"relations":[{"kind":"chung","pair":"卯酉"},{"kind":"chung","pair":"丑未"},{"kind":"hyeong","pair":"丑未"}],
"daeun":[{"sipseong":"pyeonin","state":"보통"}],
"seun":{"year":2026,"flow":{"sipseong":"jeonggwan","state":"보통"},"daeunSipseong":["pyeonin"],"samjae":"mid"},
"wolun":[{"sipseong":"geopjae","state":"보통"},{"sipseong":"bigyeon","state":"보통"},{"sipseong":"sanggwan","state":"보통"},{"sipseong":"siksin","state":"보통"},{"sipseong":"jeongjae","state":"보통"},{"sipseong":"pyeonjae","state":"보통"},{"sipseong":"jeonggwan","state":"보통"},{"sipseong":"pyeongwan","state":"보통"},{"sipseong":"jeongin","state":"보통"},{"sipseong":"pyeonin","state":"보통"},{"sipseong":"geopjae","state":"보통"},{"sipseong":"bigyeon","state":"보통"}]
},"scope":["base","seun","wolun"]}},
{"name":"partial-no-sinsal","affected":false,"existingKey":"rk_8d1ab56227b7cd5e0d19bea8d348a6bdb95c7e7aa8909694e352c76fa7c5ca28","request":{
"schemaVersion":1,"reading":{
"kind":"three_pillar",
"chart":{"year":"戊辰","month":"癸亥","day":"甲戌"},
"ilju":"gapsul",
"johap":[{"sipseong":"pyeonjae","unseong":"soe"},{"sipseong":"pyeonin","unseong":"jangsaeng"},{"sipseong":"pyeonjae","unseong":"yang"}],
"sinsal":[],
"relations":[{"kind":"wonjin","pair":"辰亥"},{"kind":"gwimun","pair":"辰亥"},{"kind":"chung","pair":"辰戌"}],
"daeun":[{"sipseong":"sanggwan","state":"부족"}],
"seun":{"year":2026,"flow":{"sipseong":"siksin","state":"부족"},"daeunSipseong":["sanggwan"]},
"wolun":[{"sipseong":"pyeongwan","state":"부족"},{"sipseong":"jeonggwan","state":"부족"},{"sipseong":"pyeonin","state":"보통"},{"sipseong":"jeongin","state":"보통"},{"sipseong":"bigyeon","state":"보통"},{"sipseong":"geopjae","state":"보통"},{"sipseong":"siksin","state":"부족"},{"sipseong":"sanggwan","state":"부족"},{"sipseong":"pyeonjae","state":"보통"},{"sipseong":"jeongjae","state":"보통"},{"sipseong":"pyeongwan","state":"부족"},{"sipseong":"jeonggwan","state":"부족"}]
},"scope":["base","seun","wolun"]}}
]`
