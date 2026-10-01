// Package boxes는 확률 상자의 순수 규칙이다. ADR 0029.
//
// 앱별 상자 카탈로그(JSON)를 읽고 검증하며, 추첨과 장수→레벨 계산을 한다.
// Firestore·HTTP를 모른다. 원장 트랜잭션은 ledger가, 권한 확인은 verify가 한다.
//
// SKU→entitlement 매핑은 기존 IAP 카탈로그(IAP_CATALOG_JSON)가 소유하고,
// 여기서는 entitlement→상자 단위 수와 친구·확률·보장·레벨 규칙만 소유한다.
// 확률은 게임 안 상자 화면과 공개 확률 페이지가 그대로 보여주는 정본이다.
package boxes

import (
	"embed"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

//go:embed *.json
var files embed.FS

// WeightTotal은 희귀도 가중치의 합이다. 10000 = 100.00%.
const WeightTotal = 10000

// LevelTriangular는 Lv n→n+1에 같은 친구 n장이 드는 규칙이다.
const LevelTriangular = "triangular"

const dateLayout = "2006-01-02"

var (
	appIDPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	itemIDPattern      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	entitlementPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// Rarity는 희귀도와 10000 기준 가중치다. 같은 희귀도 안의 친구는 균등이다.
type Rarity struct {
	ID     string `json:"id"`
	Weight int    `json:"weight"`
}

// Friend는 상자에서 나오는 항목이다. 순서는 보장 동률 해소에 쓰인다.
type Friend struct {
	ID     string `json:"id"`
	Rarity string `json:"rarity"`
}

// Version은 한 시점부터 적용되는 확률·보장·친구 목록이다.
//
// 확률을 바꿀 때는 기존 버전을 고치지 않고 새 버전을 추가한다. 개봉 증거에
// 버전을 남기므로 과거 개봉이 어느 확률로 나왔는지 재현할 수 있어야 한다.
type Version struct {
	Version     string         `json:"version"`
	PublishedOn string         `json:"published_on"`
	EffectiveOn string         `json:"effective_on"`
	NoticeDays  int            `json:"notice_days"`
	Pity        int            `json:"pity"`
	PityRarity  string         `json:"pity_rarity"`
	LevelRule   string         `json:"level_rule"`
	Rarities    []Rarity       `json:"rarities"`
	Friends     []Friend       `json:"friends"`
	Products    map[string]int `json:"products"`

	effective time.Time
}

// Catalog는 한 앱의 상자 카탈로그 전체 버전이다.
type Catalog struct {
	AppID    string    `json:"app_id"`
	Versions []Version `json:"versions"`
}

// Load는 앱의 내장 카탈로그를 읽는다. 카탈로그가 없는 앱이면 ok=false다.
func Load(appID string) (*Catalog, bool, error) {
	if !appIDPattern.MatchString(appID) {
		return nil, false, nil
	}
	raw, err := files.ReadFile(appID + ".json")
	if err != nil {
		return nil, false, nil
	}
	c, err := Parse(raw)
	if err != nil {
		return nil, false, err
	}
	if c.AppID != appID {
		return nil, false, invalid("상자 카탈로그의 앱이 파일 이름과 달라요")
	}
	return c, true, nil
}

// Parse는 카탈로그 JSON을 엄격하게 읽고 검증한다. 미지 필드는 거부한다.
func Parse(raw []byte) (*Catalog, error) {
	var c Catalog
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeRuntimeConfigInvalid, "상자 카탈로그를 해석할 수 없어요")
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func invalid(msg string) error {
	return platformerr.New(platformerr.CodeRuntimeConfigInvalid, msg)
}

func (c *Catalog) validate() error {
	if !appIDPattern.MatchString(c.AppID) || len(c.Versions) == 0 {
		return invalid("상자 카탈로그의 앱 또는 버전이 비어 있어요")
	}
	seen := map[string]bool{}
	// 같은 entitlement의 단위 수는 버전이 바뀌어도 같아야 한다. 단위 수가 바뀌면
	// 이미 산 구매의 남은 상자 수가 소급해서 달라진다.
	units := map[string]int{}
	var prev time.Time
	for i := range c.Versions {
		v := &c.Versions[i]
		if v.Version == "" || seen[v.Version] {
			return invalid("상자 카탈로그 버전 이름이 비었거나 중복돼요")
		}
		seen[v.Version] = true
		published, err1 := time.Parse(dateLayout, v.PublishedOn)
		effective, err2 := time.Parse(dateLayout, v.EffectiveOn)
		if err1 != nil || err2 != nil || effective.Before(published) {
			return invalid("상자 카탈로그 날짜가 올바르지 않아요")
		}
		if v.NoticeDays < 1 {
			return invalid("상자 확률 변경 공지 기간이 없어요")
		}
		// 첫 버전은 변경이 아니라 최초 공개라 사전 공지 대상이 아니다.
		// 이후 버전은 공지일부터 적용일까지 notice_days 이상이어야 한다
		// (확률형 아이템 표시 의무 RR-06, 게임산업법 별표 3의2).
		if i > 0 {
			if effective.Sub(published) < time.Duration(v.NoticeDays)*24*time.Hour {
				return invalid("상자 확률 변경은 적용일보다 공지 기간 이상 먼저 공개해야 해요")
			}
			if !effective.After(prev) {
				return invalid("상자 카탈로그 버전은 적용일 순서여야 해요")
			}
		}
		prev = effective
		v.effective = effective
		if err := v.validate(); err != nil {
			return err
		}
		for ent, n := range v.Products {
			if old, ok := units[ent]; ok && old != n {
				return invalid("같은 상자 상품의 단위 수를 바꿀 수 없어요")
			}
			units[ent] = n
		}
	}
	return nil
}

func (v *Version) validate() error {
	if v.Pity < 1 || v.Pity > 1000 || v.LevelRule != LevelTriangular {
		return invalid("상자 보장 또는 레벨 규칙이 올바르지 않아요")
	}
	total := 0
	rarities := map[string]int{}
	for _, r := range v.Rarities {
		if !itemIDPattern.MatchString(r.ID) || r.Weight < 0 {
			return invalid("상자 희귀도가 올바르지 않아요")
		}
		if _, dup := rarities[r.ID]; dup {
			return invalid("상자 희귀도가 중복돼요")
		}
		rarities[r.ID] = 0
		total += r.Weight
	}
	if total != WeightTotal {
		return invalid("상자 희귀도 가중치 합이 10000이 아니에요")
	}
	if _, ok := rarities[v.PityRarity]; !ok {
		return invalid("상자 보장 희귀도가 목록에 없어요")
	}
	ids := map[string]bool{}
	for _, f := range v.Friends {
		if !itemIDPattern.MatchString(f.ID) || ids[f.ID] {
			return invalid("상자 친구 id가 올바르지 않거나 중복돼요")
		}
		ids[f.ID] = true
		if _, ok := rarities[f.Rarity]; !ok {
			return invalid("상자 친구의 희귀도가 목록에 없어요")
		}
		rarities[f.Rarity]++
	}
	for _, r := range v.Rarities {
		// 가중치가 있는데 친구가 없으면 그 희귀도를 뽑았을 때 줄 것이 없다.
		if r.Weight > 0 && rarities[r.ID] == 0 {
			return invalid("가중치가 있는 상자 희귀도에 친구가 없어요")
		}
	}
	if rarities[v.PityRarity] == 0 {
		return invalid("상자 보장 희귀도에 친구가 없어요")
	}
	if len(v.Products) == 0 {
		return invalid("상자 상품이 없어요")
	}
	for ent, n := range v.Products {
		if !entitlementPattern.MatchString(ent) || n < 1 || n > 1000 {
			return invalid("상자 상품 단위 수가 올바르지 않아요")
		}
	}
	return nil
}

// Active는 now(UTC 날짜) 기준으로 적용 중인 가장 최근 버전이다.
func (c *Catalog) Active(now time.Time) (*Version, error) {
	day := now.UTC().Truncate(24 * time.Hour)
	var active *Version
	for i := range c.Versions {
		if !c.Versions[i].effective.After(day) {
			active = &c.Versions[i]
		}
	}
	if active == nil {
		return nil, platformerr.New(platformerr.CodeProductNotAllowed, "상자 판매가 아직 시작되지 않았어요")
	}
	return active, nil
}

// Units는 entitlement 하나가 주는 상자 수다. 전 버전에서 같은 값이다.
func (c *Catalog) Units(entitlementID string) (int, bool) {
	for i := range c.Versions {
		if n, ok := c.Versions[i].Products[entitlementID]; ok {
			return n, true
		}
	}
	return 0, false
}

// EntitlementIDs는 전 버전에 등장한 상자 entitlement 목록이다. 정렬돼 있다.
func (c *Catalog) EntitlementIDs() []string {
	set := map[string]bool{}
	for i := range c.Versions {
		for ent := range c.Versions[i].Products {
			set[ent] = true
		}
	}
	out := make([]string, 0, len(set))
	for ent := range set {
		out = append(out, ent)
	}
	sortStrings(out)
	return out
}

// Friend는 id의 친구를 찾는다.
func (v *Version) Friend(id string) (Friend, bool) {
	for _, f := range v.Friends {
		if f.ID == id {
			return f, true
		}
	}
	return Friend{}, false
}
