package verify

import (
	"context"
	"slices"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// passLedger는 기간 이용권 원장이다. ledger.Ledger가 구현한다. ADR 0036.
type passLedger interface {
	PassSnapshot(ctx context.Context, puid, entitlementID string, s domain.PassSchedule) (ledger.PassStatus, error)
	ClaimPass(ctx context.Context, puid, entitlementID string, s domain.PassSchedule, requestID string) (ledger.PassReceipt, error)
}

// 원장 시그니처가 바뀌면 이용권 API가 조용히 runtime_config_invalid로 닫히지 않게
// 컴파일 시점에 잡는다.
var _ passLedger = (*ledger.Ledger)(nil)

// PassSnapshot은 OpenAPI PassSnapshot이다.
type PassSnapshot struct {
	ServerTime int64               `json:"serverTime"`
	Passes     []ledger.PassStatus `json:"passes"`
}

// passesFor는 앱에서 받을 수 있는 이용권 entitlement와 원장을 돌려준다.
//
// 이용권은 SKU 카탈로그에 일정이 있고 레지스트리 허용 목록에도 있는 상품뿐이다.
// 허용 목록에 없는 상품은 이 앱에서 팔지 않으므로 목록에서 뺀다.
func (s *Service) passesFor(ctx context.Context, appID string) ([]string, passLedger, error) {
	ids := s.catalog.PassEntitlementsForApp(appID)
	if s.apps != nil {
		app, err := s.apps.GetUsable(ctx, appID)
		if err != nil {
			return nil, nil, err
		}
		if !app.FeatureEnabled("iap") {
			return nil, nil, platformerr.New(platformerr.CodeProductNotAllowed, "결제를 지원하지 않는 앱이에요")
		}
		ids = slices.DeleteFunc(ids, func(id string) bool { return !app.EntitlementAllowed(id) })
	}
	if len(ids) == 0 {
		return nil, nil, platformerr.New(platformerr.CodeProductNotAllowed, "이용권을 지원하지 않는 앱이에요")
	}
	l, ok := s.ledgerFor(appID).(passLedger)
	if !ok {
		return nil, nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid, "이용권 원장이 준비되지 않았어요")
	}
	return ids, l, nil
}

// PassSnapshot은 앱의 이용권마다 지금 상태를 돌려준다. 산 적이 없는 이용권도
// 일정과 함께 active=false로 돌려줘 구매 화면이 서버 값을 그릴 수 있게 한다.
func (s *Service) PassSnapshot(ctx context.Context, appID, puid string) (PassSnapshot, error) {
	ids, l, err := s.passesFor(ctx, appID)
	if err != nil {
		return PassSnapshot{}, err
	}
	out := PassSnapshot{ServerTime: s.now().Unix(), Passes: make([]ledger.PassStatus, 0, len(ids))}
	for _, id := range ids {
		schedule, _ := s.catalog.PassForApp(appID, id)
		status, err := l.PassSnapshot(ctx, puid, id, schedule)
		if err != nil {
			return PassSnapshot{}, err
		}
		out.Passes = append(out.Passes, status)
	}
	return out, nil
}

// ClaimPass는 이용권 하루 몫을 받는다. entitlementId는 자기 이용권 중 어느 것을 받을지
// 고르는 값일 뿐이다. 받을 양과 날짜, 소유는 서버 카탈로그와 원장이 정한다(불변식 8).
func (s *Service) ClaimPass(ctx context.Context, appID, puid, entitlementID, requestID string) (ledger.PassReceipt, error) {
	ids, l, err := s.passesFor(ctx, appID)
	if err != nil {
		return ledger.PassReceipt{}, err
	}
	if !slices.Contains(ids, entitlementID) {
		return ledger.PassReceipt{}, platformerr.New(platformerr.CodeProductNotAllowed, "이용권이 아닌 상품이에요")
	}
	schedule, _ := s.catalog.PassForApp(appID, entitlementID)
	r, err := l.ClaimPass(ctx, puid, entitlementID, schedule, requestID)
	if err != nil {
		s.audit(ctx, "iap.pass_claimed", appID, puid, string(platformerr.CodeOf(err)), map[string]any{
			"entitlement_id": entitlementID,
		})
		return ledger.PassReceipt{}, err
	}
	// 지급량을 운영 문의와 실측에 쓰도록 수령마다 감사 원장에 남긴다.
	s.audit(ctx, "iap.pass_claimed", appID, puid, "ok", map[string]any{
		"entitlement_id": entitlementID,
		"request_id":     requestID,
		"days":           r.Days,
		"amount":         r.Amount,
		"applied":        r.Applied,
	})
	return r, nil
}
