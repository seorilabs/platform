package boxes

import (
	"fmt"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// seq는 정해진 수를 차례로 돌려주는 테스트 난수다.
type seq []int

func (s *seq) Intn(n int) (int, error) {
	v := (*s)[0]
	*s = (*s)[1:]
	return v % n, nil
}

func bloomhand(t *testing.T) *Version {
	t.Helper()
	c, ok, err := Load("bloomhand")
	if err != nil || !ok {
		t.Fatalf("load ok=%v err=%v", ok, err)
	}
	v, err := c.Active(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBloomhandCatalogMatchesPublishedOdds(t *testing.T) {
	v := bloomhand(t)
	want := map[string]int{"common": 6000, "uncommon": 3200, "rare": 800}
	count := map[string]int{}
	for _, f := range v.Friends {
		count[f.Rarity]++
	}
	for _, r := range v.Rarities {
		if want[r.ID] != r.Weight {
			t.Errorf("%s weight %d", r.ID, r.Weight)
		}
	}
	if count["common"] != 10 || count["uncommon"] != 7 || count["rare"] != 3 || v.Pity != 10 {
		t.Fatalf("count=%v pity=%d", count, v.Pity)
	}
	c, _, _ := Load("bloomhand")
	if n, _ := c.Units("friend_box_5"); n != 5 {
		t.Fatalf("units %d", n)
	}
	if got := c.EntitlementIDs(); len(got) != 2 || got[0] != "friend_box_1" {
		t.Fatalf("ids %v", got)
	}
}

func TestDrawWeightedThenUniform(t *testing.T) {
	v := bloomhand(t)
	cases := []struct {
		roll, pick int
		want       string
	}{
		{0, 0, "B01"},    // 흔함 첫 칸
		{5999, 9, "B10"}, // 흔함 끝
		{6000, 0, "B11"}, // 드묾 시작
		{9199, 6, "B17"}, // 드묾 끝
		{9200, 2, "B20"}, // 희귀
	}
	for _, c := range cases {
		r := seq{c.roll, c.pick}
		d, err := v.Draw(&r, nil, 0)
		if err != nil || d.FriendID != c.want || d.Pity {
			t.Errorf("roll %d pick %d: %+v %v", c.roll, c.pick, d, err)
		}
	}
}

func TestDrawPityGivesFewestOwnedRare(t *testing.T) {
	v := bloomhand(t)
	r := seq{0, 0}
	d, err := v.Draw(&r, map[string]int{"B18": 2, "B19": 1, "B20": 1}, 9)
	if err != nil || d.FriendID != "B19" || !d.Pity || d.Rarity != "rare" {
		t.Fatalf("%+v %v", d, err)
	}
	if v.NextSinceRare(9, d) != 0 {
		t.Fatal("rare resets the counter")
	}
	r = seq{0, 0}
	d, _ = v.Draw(&r, nil, 8)
	if d.Pity || v.NextSinceRare(8, d) != 9 {
		t.Fatalf("9th box is not guaranteed: %+v", d)
	}
}

func TestLevelsAreTriangular(t *testing.T) {
	for _, c := range []struct{ copies, level int }{{0, 0}, {1, 1}, {2, 2}, {3, 2}, {4, 3}, {7, 4}, {46, 10}} {
		if got := LevelForCopies(c.copies); got != c.level {
			t.Errorf("copies %d level %d want %d", c.copies, got, c.level)
		}
	}
}

func TestParseRejectsUnsafeCatalogs(t *testing.T) {
	base := `{"app_id":"x","versions":[%s]}`
	good := `{"version":"v1","published_on":"2026-10-01","effective_on":"2026-10-01","notice_days":7,"pity":10,"pity_rarity":"rare","level_rule":"triangular","rarities":[{"id":"common","weight":9000},{"id":"rare","weight":1000}],"friends":[{"id":"a","rarity":"common"},{"id":"b","rarity":"rare"}],"products":{"box_1":1}}`
	cases := map[string]string{
		"weights":       `{"version":"v1","published_on":"2026-10-01","effective_on":"2026-10-01","notice_days":7,"pity":10,"pity_rarity":"rare","level_rule":"triangular","rarities":[{"id":"common","weight":9000},{"id":"rare","weight":900}],"friends":[{"id":"a","rarity":"common"},{"id":"b","rarity":"rare"}],"products":{"box_1":1}}`,
		"empty rarity":  `{"version":"v1","published_on":"2026-10-01","effective_on":"2026-10-01","notice_days":7,"pity":10,"pity_rarity":"rare","level_rule":"triangular","rarities":[{"id":"common","weight":9000},{"id":"rare","weight":1000}],"friends":[{"id":"a","rarity":"common"}],"products":{"box_1":1}}`,
		"short notice":  good + `,{"version":"v2","published_on":"2026-10-05","effective_on":"2026-10-08","notice_days":7,"pity":10,"pity_rarity":"rare","level_rule":"triangular","rarities":[{"id":"common","weight":9000},{"id":"rare","weight":1000}],"friends":[{"id":"a","rarity":"common"},{"id":"b","rarity":"rare"}],"products":{"box_1":1}}`,
		"units changed": good + `,{"version":"v2","published_on":"2026-10-01","effective_on":"2026-10-08","notice_days":7,"pity":10,"pity_rarity":"rare","level_rule":"triangular","rarities":[{"id":"common","weight":9000},{"id":"rare","weight":1000}],"friends":[{"id":"a","rarity":"common"},{"id":"b","rarity":"rare"}],"products":{"box_1":2}}`,
		"unknown field": `{"version":"v1","extra":1}`,
	}
	for name, body := range cases {
		if _, err := Parse([]byte(fmt.Sprintf(base, body))); platformerr.CodeOf(err) != platformerr.CodeRuntimeConfigInvalid {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	ok := good + `,{"version":"v2","published_on":"2026-10-01","effective_on":"2026-10-08","notice_days":7,"pity":10,"pity_rarity":"rare","level_rule":"triangular","rarities":[{"id":"common","weight":8000},{"id":"rare","weight":2000}],"friends":[{"id":"a","rarity":"common"},{"id":"b","rarity":"rare"}],"products":{"box_1":1}}`
	c, err := Parse([]byte(fmt.Sprintf(base, ok)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		day  string
		want string
	}{{"2026-10-07", "v1"}, {"2026-10-08", "v2"}} {
		now, _ := time.Parse(dateLayout, tc.day)
		v, err := c.Active(now.Add(23 * time.Hour))
		if err != nil || v.Version != tc.want {
			t.Errorf("%s: %v %v", tc.day, v, err)
		}
	}
	if _, err := c.Active(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)); platformerr.CodeOf(err) != platformerr.CodeProductNotAllowed {
		t.Fatalf("before first version: %v", err)
	}
}
