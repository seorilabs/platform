// Package catalog는 마켓 SKU와 entitlement의 대응을 관리한다.
//
// 한 entitlement가 마켓마다 다른 SKU를 갖는다. 클라이언트는 마켓 SKU를
// 보내고 서버는 entitlement로 바꿔 원장에 기록한다.
package catalog

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// MaxEntitlements는 한 앱의 entitlement 상한이다.
// 이보다 많으면 설정 실수일 가능성이 높다.
const MaxEntitlements = 100

var entitlementIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// placeholders는 설정을 덜 채운 흔적이다.
//
// 이런 값이 남은 채 배포되면 런타임에 이상하게 동작한다.
// 부팅 시점에 잡는 편이 낫다.
var placeholders = map[string]bool{
	"확정 필요": true,
	"TBD":   true,
	"TODO":  true,
	"FIXME": true,
	"XXX":   true,
	"":      true,
}

// Entry는 entitlement 하나의 마켓별 SKU다.
type Entry struct {
	Type       domain.ProductType `json:"type,omitempty"`
	GooglePlay string             `json:"google_play,omitempty"`
	AppStore   string             `json:"app_store,omitempty"`
	AppsInToss string             `json:"apps_in_toss,omitempty"`
}

// Product는 서버가 허용한 SKU의 지급 대상과 상품 유형이다.
type Product struct {
	EntitlementID string
	Type          domain.ProductType
}

// SKU는 마켓에 해당하는 SKU를 돌려준다.
func (e Entry) SKU(p domain.Platform) string {
	switch p {
	case domain.PlatformGooglePlay:
		return e.GooglePlay
	case domain.PlatformAppStore:
		return e.AppStore
	case domain.PlatformAppsInToss:
		return e.AppsInToss
	default:
		return ""
	}
}

// file은 카탈로그 JSON의 구조다.
type file struct {
	Version      int              `json:"version"`
	Entitlements map[string]Entry `json:"entitlements"`
	Apps         map[string]struct {
		Entitlements map[string]Entry `json:"entitlements"`
	} `json:"apps"`
}

// Catalog는 검증을 통과한 카탈로그다.
type Catalog struct {
	entitlements map[string]Entry
	// entriesByApp는 같은 entitlement ID를 여러 앱이 서로 다른 SKU로
	// 사용할 수 있게 앱 경계를 보존한다.
	entriesByApp map[string]map[string]Entry
	// bySKU는 (platform, sku) → entitlementID 역인덱스다.
	// 검증 요청이 마켓 SKU로 오므로 이 방향 조회가 주경로다.
	bySKU           map[string]string
	appEntitlements map[string]map[string]bool
}

// Parse는 카탈로그 JSON을 검증해 만든다.
//
// requiredPlatforms에 있는 마켓은 모든 entitlement가 SKU를 가져야 한다.
// 마켓별 단계적 출시를 지원하려고 필수 마켓을 인자로 받는다.
// 아직 AIT에 안 올린 상품이 있어도 Play와 App Store는 동작해야 한다.
func Parse(raw []byte, requiredPlatforms []domain.Platform) (*Catalog, error) {
	var f file
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, platformerr.Wrapf(err, platformerr.CodeCatalogInvalid,
			"카탈로그를 해석할 수 없어요")
	}

	if len(f.Entitlements) == 0 && len(f.Apps) == 0 {
		return nil, platformerr.New(platformerr.CodeCatalogIncomplete, "카탈로그가 비어 있어요")
	}
	if len(f.Entitlements) > MaxEntitlements {
		return nil, platformerr.Newf(platformerr.CodeCatalogInvalid,
			"entitlement가 %d개를 넘어요", MaxEntitlements)
	}

	c := &Catalog{
		entitlements:    make(map[string]Entry, len(f.Entitlements)),
		entriesByApp:    make(map[string]map[string]Entry),
		bySKU:           make(map[string]string, len(f.Entitlements)*3),
		appEntitlements: make(map[string]map[string]bool),
	}
	if len(f.Entitlements) > 0 && len(f.Apps) > 0 {
		return nil, platformerr.New(platformerr.CodeCatalogInvalid, "전역 카탈로그와 앱별 카탈로그를 함께 둘 수 없어요")
	}
	if len(f.Apps) > 0 {
		appIDs := make([]string, 0, len(f.Apps))
		for appID := range f.Apps {
			appIDs = append(appIDs, appID)
		}
		sort.Strings(appIDs)
		for _, appID := range appIDs {
			entries := f.Apps[appID].Entitlements
			if strings.TrimSpace(appID) == "" || len(entries) == 0 || len(entries) > MaxEntitlements {
				return nil, platformerr.Newf(platformerr.CodeCatalogInvalid, "%s 앱 카탈로그가 올바르지 않아요", appID)
			}
			if err := c.addEntries(appID, entries, requiredPlatforms); err != nil {
				return nil, err
			}
		}
		return c, nil
	}
	if err := c.addEntries("", f.Entitlements, requiredPlatforms); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Catalog) addEntries(appID string, entries map[string]Entry, requiredPlatforms []domain.Platform) error {
	if c.appEntitlements[appID] == nil {
		c.appEntitlements[appID] = map[string]bool{}
	}
	if c.entriesByApp[appID] == nil {
		c.entriesByApp[appID] = map[string]Entry{}
	}

	// 순서를 고정해 에러 메시지가 실행마다 바뀌지 않게 한다.
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if !entitlementIDPattern.MatchString(id) {
			return platformerr.Newf(platformerr.CodeCatalogInvalid,
				"entitlement 이름이 올바르지 않아요: %s", id)
		}
		entry := entries[id]
		if !entry.Type.Valid() {
			return platformerr.Newf(platformerr.CodeCatalogInvalid,
				"%s의 상품 유형이 올바르지 않아요: %s", id, entry.Type)
		}

		for _, p := range requiredPlatforms {
			sku := entry.SKU(p)
			if placeholders[strings.TrimSpace(sku)] {
				return platformerr.Newf(platformerr.CodeCatalogIncomplete,
					"%s의 %s SKU가 아직 정해지지 않았어요", id, p)
			}
		}

		for _, p := range domain.AllPlatforms() {
			sku := strings.TrimSpace(entry.SKU(p))
			if sku == "" || placeholders[sku] {
				continue
			}
			key := appSKUKey(appID, p, sku)
			if prev, dup := c.bySKU[key]; dup {
				// 같은 SKU가 두 entitlement에 붙으면 어느 쪽을 줄지 알 수 없다.
				return platformerr.Newf(platformerr.CodeCatalogDuplicate,
					"%s의 SKU %s가 %s와 %s에 중복돼요", p, sku, prev, id)
			}
			c.bySKU[key] = id
		}

		c.entitlements[id] = entry
		c.entriesByApp[appID][id] = entry
		c.appEntitlements[appID][id] = true
	}
	return nil
}

// EntitlementFor는 마켓 SKU에 해당하는 entitlement를 찾는다.
func (c *Catalog) EntitlementFor(p domain.Platform, sku string) (string, error) {
	return c.EntitlementForApp("", p, sku)
}

// EntitlementForApp은 (appId, market, productId) 고정 키로 상품을 찾는다.
// 앱별 항목이 없을 때만 기존 단일 앱 카탈로그를 읽어 lizard SDK와 배포
// 환경변수의 마이그레이션을 끊지 않는다.
func (c *Catalog) EntitlementForApp(appID string, p domain.Platform, sku string) (string, error) {
	product, err := c.ProductForApp(appID, p, sku)
	if err != nil {
		return "", err
	}
	return product.EntitlementID, nil
}

// ProductForApp은 허용된 SKU의 entitlement와 서버 신뢰 상품 유형을 찾는다.
func (c *Catalog) ProductForApp(appID string, p domain.Platform, sku string) (Product, error) {
	id, ok := c.bySKU[appSKUKey(appID, p, strings.TrimSpace(sku))]
	resolvedAppID := appID
	if !ok {
		id, ok = c.bySKU[appSKUKey("", p, strings.TrimSpace(sku))]
		resolvedAppID = ""
	}
	if !ok {
		// 어떤 SKU가 존재하는지 알려주지 않는다.
		return Product{}, platformerr.New(platformerr.CodeProductNotAllowed,
			"판매하지 않는 상품이에요")
	}
	entry, ok := c.entriesByApp[resolvedAppID][id]
	if !ok {
		return Product{}, platformerr.New(platformerr.CodeCatalogInvalid,
			"상품 카탈로그 역인덱스가 올바르지 않아요")
	}
	return Product{EntitlementID: id, Type: entry.Type.Normalize()}, nil
}

func (c *Catalog) HasForApp(appID, entitlementID string) bool {
	if c.appEntitlements[appID][entitlementID] {
		return true
	}
	return c.appEntitlements[""][entitlementID]
}

// SKUFor는 entitlement의 마켓별 SKU를 돌려준다.
func (c *Catalog) SKUFor(entitlementID string, p domain.Platform) (string, bool) {
	e, ok := c.entitlements[entitlementID]
	if !ok {
		return "", false
	}
	sku := strings.TrimSpace(e.SKU(p))
	if sku == "" || placeholders[sku] {
		return "", false
	}
	return sku, true
}

// SKUForApp은 앱별 entitlement의 마켓 SKU를 돌려준다. 앱별 항목이
// 없을 때만 기존 단일 앱 카탈로그로 폴백해 lizard 계약을 유지한다.
func (c *Catalog) SKUForApp(appID, entitlementID string, p domain.Platform) (string, bool) {
	entries := c.entriesByApp[appID]
	e, ok := entries[entitlementID]
	if !ok {
		e, ok = c.entriesByApp[""][entitlementID]
	}
	if !ok {
		return "", false
	}
	sku := strings.TrimSpace(e.SKU(p))
	if sku == "" || placeholders[sku] {
		return "", false
	}
	return sku, true
}

// Has는 entitlement가 카탈로그에 있는지 본다.
func (c *Catalog) Has(entitlementID string) bool {
	_, ok := c.entitlements[entitlementID]
	return ok
}

// IDs는 모든 entitlement 이름을 정렬해 돌려준다.
func (c *Catalog) IDs() []string {
	out := make([]string, 0, len(c.entitlements))
	for id := range c.entitlements {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func appSKUKey(appID string, p domain.Platform, sku string) string {
	return appID + "\x00" + string(p) + "\x00" + sku
}
