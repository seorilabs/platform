package content

import (
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

func TestReadingLimitExemptsSameReadingKey(t *testing.T) {
	doc := &usageDoc{ReadingKeys: map[string]bool{}}
	if err := allowReadingKey(doc, 1, "rk_same"); err != nil {
		t.Fatal(err)
	}
	if err := allowReadingKey(doc, 1, "rk_same"); err != nil {
		t.Fatalf("같은 readingKey 재조회가 한도에 걸렸다: %v", err)
	}
	if err := allowReadingKey(doc, 1, "rk_other"); platformerr.CodeOf(err) != platformerr.CodeRateLimited {
		t.Fatalf("새 readingKey code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

func TestTermLimitCountsEveryLookup(t *testing.T) {
	doc := &usageDoc{}
	if err := allowTermLookup(doc, 1); err != nil {
		t.Fatal(err)
	}
	if err := allowTermLookup(doc, 1); platformerr.CodeOf(err) != platformerr.CodeRateLimited {
		t.Fatalf("code=%q err=%v", platformerr.CodeOf(err), err)
	}
}

// 목록은 표시용이라 깨진 문서를 건너뛴다. 다만 버리는 기준이 응답 계약과
// 같아야 한다 — source enum과 시각이 그대로 새 나가면 스펙을 어긴다.
func TestListableUnlockDropsDocsThatBreakTheResponseContract(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	ok := unlockDoc{
		AppID: "ungeul", PlatformUserID: "puid",
		ReadingKey: "rk_a", DeepKey: "flow:2026", Source: "ticket", CreatedAt: now,
	}
	if !listableUnlock(ok, "ungeul", "puid") {
		t.Fatal("정상 문서가 걸러졌다")
	}

	for name, mutate := range map[string]func(unlockDoc) unlockDoc{
		"다른 앱":          func(d unlockDoc) unlockDoc { d.AppID = "other"; return d },
		"다른 사용자":        func(d unlockDoc) unlockDoc { d.PlatformUserID = "other"; return d },
		"readingKey 없음": func(d unlockDoc) unlockDoc { d.ReadingKey = ""; return d },
		"deepKey 없음":    func(d unlockDoc) unlockDoc { d.DeepKey = ""; return d },
		"알 수 없는 source": func(d unlockDoc) unlockDoc { d.Source = "여기없는수단"; return d },
		"빈 source":      func(d unlockDoc) unlockDoc { d.Source = ""; return d },
		"시각 없음":         func(d unlockDoc) unlockDoc { d.CreatedAt = time.Time{}; return d },
	} {
		t.Run(name, func(t *testing.T) {
			if listableUnlock(mutate(ok), "ungeul", "puid") {
				t.Fatal("걸러졌어야 한다")
			}
		})
	}

	if !listableUnlock(func() unlockDoc { d := ok; d.Source = "reward_claim"; return d }(), "ungeul", "puid") {
		t.Fatal("광고 보상 해제가 걸러졌다")
	}
}
