package verify

import (
	"context"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/boxes"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// boxLedger는 상자 원장이다. ledger.Ledger가 구현한다. ADR 0029.
type boxLedger interface {
	BoxSnapshot(ctx context.Context, puid string, cat *boxes.Catalog) (ledger.BoxState, error)
	OpenBox(ctx context.Context, puid string, cat *boxes.Catalog, version *boxes.Version, requestID string, rnd boxes.Random) (ledger.BoxReceipt, error)
}

// BoxRarity·BoxFriend·BoxProduct·BoxCatalogView는 OpenAPI BoxCatalogView다.
type BoxRarity struct {
	ID     string `json:"id"`
	Weight int    `json:"weight"`
}
type BoxFriend struct {
	ID     string `json:"id"`
	Rarity string `json:"rarity"`
}
type BoxProduct struct {
	EntitlementID string `json:"entitlementId"`
	Units         int    `json:"units"`
}
type BoxCatalogView struct {
	Version     string       `json:"version"`
	EffectiveOn string       `json:"effectiveOn"`
	NoticeDays  int          `json:"noticeDays"`
	Pity        int          `json:"pity"`
	LevelRule   string       `json:"levelRule"`
	Rarities    []BoxRarity  `json:"rarities"`
	Friends     []BoxFriend  `json:"friends"`
	Products    []BoxProduct `json:"products"`
}

// BoxSnapshot은 OpenAPI BoxSnapshot이다. Linked는 HTTP 경계가 세션에서 채운다.
type BoxSnapshot struct {
	Linked     bool            `json:"linked"`
	ServerTime int64           `json:"serverTime"`
	State      ledger.BoxState `json:"state"`
	Catalog    BoxCatalogView  `json:"catalog"`
}

// boxesFor는 앱이 상자를 쓸 수 있는지 확인하고 카탈로그·적용 버전·원장을 돌려준다.
//
// 상자 entitlement는 레지스트리 허용 목록과 SKU 카탈로그에 모두 있어야 한다.
// 하나라도 빠지면 구매 검증은 되는데 개봉이 안 되거나 그 반대가 되므로
// 설정 불일치로 보고 fail-closed한다.
func (s *Service) boxesFor(ctx context.Context, appID string) (*boxes.Catalog, *boxes.Version, boxLedger, error) {
	cat := s.boxes[appID]
	if cat == nil {
		return nil, nil, nil, platformerr.New(platformerr.CodeProductNotAllowed, "상자를 지원하지 않는 앱이에요")
	}
	if s.apps != nil {
		app, err := s.apps.GetUsable(ctx, appID)
		if err != nil {
			return nil, nil, nil, err
		}
		if !app.FeatureEnabled("iap") {
			return nil, nil, nil, platformerr.New(platformerr.CodeProductNotAllowed, "결제를 지원하지 않는 앱이에요")
		}
		for _, ent := range cat.EntitlementIDs() {
			if !app.EntitlementAllowed(ent) || !s.catalog.HasForApp(appID, ent) {
				return nil, nil, nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid, "상자 상품 설정이 레지스트리·카탈로그와 달라요")
			}
		}
	}
	version, err := cat.Active(s.now())
	if err != nil {
		return nil, nil, nil, err
	}
	l, ok := s.ledgerFor(appID).(boxLedger)
	if !ok {
		return nil, nil, nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid, "상자 원장이 준비되지 않았어요")
	}
	return cat, version, l, nil
}

// BoxSnapshot은 상자 상태와 지금 적용 중인 카탈로그를 돌려준다.
func (s *Service) BoxSnapshot(ctx context.Context, appID, puid string) (BoxSnapshot, error) {
	cat, version, l, err := s.boxesFor(ctx, appID)
	if err != nil {
		return BoxSnapshot{}, err
	}
	state, err := l.BoxSnapshot(ctx, puid, cat)
	if err != nil {
		return BoxSnapshot{}, err
	}
	return BoxSnapshot{ServerTime: s.now().Unix(), State: state, Catalog: catalogView(cat, version)}, nil
}

// OpenBox는 상자 하나를 연다. 추첨은 서버 난수만 쓴다(ADR 0029 4항).
func (s *Service) OpenBox(ctx context.Context, appID, puid, requestID string) (ledger.BoxReceipt, error) {
	cat, version, l, err := s.boxesFor(ctx, appID)
	if err != nil {
		return ledger.BoxReceipt{}, err
	}
	r, err := l.OpenBox(ctx, puid, cat, version, requestID, s.boxRandom)
	if err != nil {
		s.audit(ctx, "iap.box_opened", appID, puid, string(platformerr.CodeOf(err)), nil)
		return ledger.BoxReceipt{}, err
	}
	// 확률 실측과 문의 대응을 위해 개봉마다 감사 원장에 한 줄 남긴다(ADR 0029 8항).
	s.audit(ctx, "iap.box_opened", appID, puid, "ok", map[string]any{
		"catalog_version": r.CatalogVersion,
		"friend_id":       r.FriendID,
		"rarity":          r.Rarity,
		"pity":            r.Pity,
		"applied":         r.Applied,
	})
	return r, nil
}

func catalogView(cat *boxes.Catalog, v *boxes.Version) BoxCatalogView {
	out := BoxCatalogView{
		Version: v.Version, EffectiveOn: v.EffectiveOn, NoticeDays: v.NoticeDays,
		Pity: v.Pity, LevelRule: v.LevelRule,
	}
	for _, r := range v.Rarities {
		out.Rarities = append(out.Rarities, BoxRarity{ID: r.ID, Weight: r.Weight})
	}
	for _, f := range v.Friends {
		out.Friends = append(out.Friends, BoxFriend{ID: f.ID, Rarity: f.Rarity})
	}
	for _, ent := range cat.EntitlementIDs() {
		if n, ok := v.Products[ent]; ok {
			out.Products = append(out.Products, BoxProduct{EntitlementID: ent, Units: n})
		}
	}
	return out
}

// withBoxClock은 테스트에서 시계와 난수를 고정한다.
func (s *Service) withBoxClock(now func() time.Time, r boxes.Random) *Service {
	s.now = now
	s.boxRandom = r
	return s
}
