package ledger

import (
	_ "embed"
	"encoding/json"
)

//go:embed salt_compass_catalog.json
var saltCatalogJSON []byte

type saltCatalog struct {
	Version    string           `json:"version"`
	Credits    map[string]int64 `json:"credits"`
	BuildCosts map[string]int64 `json:"buildCosts"`
}

// 원장 변경 없이 배포 중 catalog를 바꾸지 않는다. 과거 환불은 order의 지급량을 쓴다.
var saltRules = func() saltCatalog {
	var c saltCatalog
	if err := json.Unmarshal(saltCatalogJSON, &c); err != nil {
		panic(err)
	}
	if c.Version == "" || c.Credits[saltStarterID] != 40 || len(c.Credits) != 4 || len(c.BuildCosts) == 0 {
		panic("invalid Salt & Compass financial catalog")
	}
	return c
}()
