// Package verify는 구매 검증 유스케이스다.
//
// 인터페이스를 여기에 정의한다. Service가 소비자이기 때문이다.
// providers와 ledger는 이 패키지를 import하지 않고 메서드 시그니처만
// 맞추면 된다. Go의 암묵적 인터페이스 덕분이다.
package verify

import (
	"context"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/binding"
	"github.com/seorilabs/platform/server/internal/iap/catalog"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

// Verifier는 마켓에 구매를 확인한다.
//
// providers/{play,apple,toss}가 구현한다. 이 패키지를 import하지 않는다.
type Verifier interface {
	Platform() domain.Platform
	Verify(ctx context.Context, proof domain.Proof) (domain.VerifiedPurchase, error)
	// CompleteGrant는 마켓에 완료를 알린다.
	// Play는 acknowledge, Apple은 finishTransaction이다.
	CompleteGrant(ctx context.Context, p domain.VerifiedPurchase) error
}

// Ledger는 entitlement 원장이다.
type Ledger interface {
	Grant(ctx context.Context, in ledger.GrantInput) (domain.GrantResult, error)
	RecordPending(ctx context.Context, in ledger.GrantInput) error
	ListActive(ctx context.Context, puid string) ([]string, error)
}

// OutboxWriter는 마켓 완료 재시도 대기열이다.
//
// 완료 호출이 실패해도 지급은 롤백하지 않는다. 불변식 7이다.
type OutboxWriter interface {
	Enqueue(ctx context.Context, orderKey string, p domain.VerifiedPurchase) error
}

// Auditor는 감사 원장에 기록한다.
type Auditor interface {
	Record(ctx context.Context, action, appID, puid, outcome string, detail map[string]any)
}

// Outcome은 검증 결과다.
type Outcome struct {
	Status         string   `json:"status"` // verified | pending | revoked
	EntitlementID  string   `json:"entitlementId"`
	TransactionID  string   `json:"transactionId,omitempty"`
	Granted        *bool    `json:"granted,omitempty"`
	AlreadyGranted *bool    `json:"alreadyGranted,omitempty"`
	Entitlements   []string `json:"entitlements"`
	Completion     *Action  `json:"completion,omitempty"`
}

// Action은 클라이언트가 할 후속 조치다.
type Action struct {
	Action  domain.CompletionAction `json:"action"`
	OrderID string                  `json:"orderId,omitempty"`
}

// Service는 검증 유스케이스다.
type Service struct {
	verifiers    map[domain.Platform]Verifier
	appVerifiers map[string]map[domain.Platform]Verifier
	ledger       Ledger
	appLedgers   map[string]Ledger
	catalog      *catalog.Catalog
	keyring      *binding.Keyring
	outbox       OutboxWriter
	appOutboxes  map[string]OutboxWriter
	auditor      Auditor
	apps         interface {
		GetUsable(context.Context, string) (registry.App, error)
	}
	now func() time.Time
}

// Config는 서비스 조립 설정이다.
type Config struct {
	Verifiers    []Verifier
	AppVerifiers map[string][]Verifier
	Ledger       Ledger
	AppLedgers   map[string]Ledger
	Catalog      *catalog.Catalog
	Keyring      *binding.Keyring
	Outbox       OutboxWriter
	AppOutboxes  map[string]OutboxWriter
	Auditor      Auditor
	Apps         interface {
		GetUsable(context.Context, string) (registry.App, error)
	}
}

// New는 서비스를 만든다.
func New(cfg Config) (*Service, error) {
	if cfg.Ledger == nil || cfg.Catalog == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"결제 설정이 올바르지 않아요")
	}

	m := make(map[domain.Platform]Verifier, len(cfg.Verifiers))
	for _, v := range cfg.Verifiers {
		if v == nil {
			continue
		}
		m[v.Platform()] = v
	}
	appVerifiers := make(map[string]map[domain.Platform]Verifier, len(cfg.AppVerifiers))
	for appID, list := range cfg.AppVerifiers {
		byPlatform := map[domain.Platform]Verifier{}
		for _, v := range list {
			if v != nil {
				byPlatform[v.Platform()] = v
			}
		}
		appVerifiers[appID] = byPlatform
	}

	return &Service{
		verifiers:    m,
		appVerifiers: appVerifiers,
		ledger:       cfg.Ledger,
		appLedgers:   cfg.AppLedgers,
		catalog:      cfg.Catalog,
		keyring:      cfg.Keyring,
		outbox:       cfg.Outbox,
		appOutboxes:  cfg.AppOutboxes,
		auditor:      cfg.Auditor,
		apps:         cfg.Apps,
		now:          time.Now,
	}, nil
}

// Supports는 이 마켓의 검증기가 있는지 본다.
//
// 자격증명이 없어 조립하지 못한 마켓이 있을 수 있다.
// AIT mTLS 인증서가 미확보라 AIT만 빠지는 상황이 실제로 가능하다.
func (s *Service) Supports(p domain.Platform) bool {
	_, ok := s.verifiers[p]
	return ok
}

func (s *Service) verifierFor(appID string, p domain.Platform) (Verifier, bool) {
	if byPlatform := s.appVerifiers[appID]; byPlatform != nil {
		if v, ok := byPlatform[p]; ok {
			return v, true
		}
	}
	v, ok := s.verifiers[p]
	return v, ok
}
func (s *Service) ledgerFor(appID string) Ledger {
	if l := s.appLedgers[appID]; l != nil {
		return l
	}
	return s.ledger
}
func (s *Service) outboxFor(appID string) OutboxWriter {
	if o := s.appOutboxes[appID]; o != nil {
		return o
	}
	return s.outbox
}

// VerifyPurchase는 구매를 검증하고 지급한다.
//
// 흐름
//  1. 마켓 검증기 확인
//  2. SKU를 entitlement로 변환
//  3. 마켓에 구매 확인
//  4. 계정 바인딩 검증 — 다른 사용자의 구매 가로채기 차단
//  5. 상태에 따라 지급/보류/회수
//  6. 마켓 완료 처리. 실패해도 지급은 유지
func (s *Service) VerifyPurchase(
	ctx context.Context,
	appID, puid string,
	proof domain.Proof,
) (Outcome, error) {
	var app registry.App
	if s.apps != nil {
		var err error
		app, err = s.apps.GetUsable(ctx, appID)
		if err != nil {
			return Outcome{}, err
		}
		if !app.FeatureEnabled("iap") || !app.MarketEnabled(string(proof.Platform)) {
			return Outcome{}, platformerr.New(platformerr.CodeProductNotAllowed, "이 앱에서 허용하지 않는 결제 마켓이에요")
		}
	}
	v, ok := s.verifierFor(appID, proof.Platform)
	if !ok {
		return Outcome{}, platformerr.Newf(platformerr.CodePlatformUnavailable,
			"%s 결제는 아직 준비 중이에요", proof.Platform)
	}

	product, err := s.catalog.ProductForApp(appID, proof.Platform, proof.ProductID)
	if err != nil {
		return Outcome{}, err
	}
	entID := product.EntitlementID
	if s.apps != nil && (!app.EntitlementAllowed(entID) || !s.catalog.HasForApp(appID, entID)) {
		return Outcome{}, platformerr.New(platformerr.CodeProductNotAllowed, "이 앱에 허용되지 않은 상품이에요")
	}
	led := s.ledgerFor(appID)
	proof.ProductType = product.Type

	purchase, err := v.Verify(ctx, proof)
	if err != nil {
		s.audit(ctx, "iap.verified", appID, puid, string(platformerr.CodeOf(err)), map[string]any{
			"platform":   string(proof.Platform),
			"product_id": proof.ProductID,
		})
		return Outcome{}, err
	}

	// 계정 바인딩은 부정 신호로 남기되 지급을 막지는 않는다. ADR 0010
	//
	// 바인딩 참조는 마켓 계정 식별자가 아니라 우리가 platform_user_id로
	// 만든 HMAC이다. 앱을 지우면 익명 uid가 새로 생기므로 이 값이 반드시
	// 달라진다. 거부하면 재설치한 유저는 예외 없이 산 상품을 잃는다.
	// Apple 심사 지침 3.1.1은 비소비성 상품에 복원 수단을 요구하므로
	// 그 상태로는 리젝 사유이기도 하다.
	//
	// 소유의 근거는 마켓이 이 계정에 발급한 토큰 자체다. Play는
	// queryPurchases로 현재 로그인 계정이 가진 구매만 돌려주고, Apple은
	// Apple ID에 묶인 거래만 준다. 우리는 그 토큰을 마켓 API로 다시
	// 검증한 뒤에야 여기 도달한다.
	//
	// Google도 이 필드를 "부정 탐지와 귀속"으로 설명하고 선택 사항으로
	// 둔다. 불일치를 거부 사유로 쓰라고 하지 않는다.
	if s.keyring != nil && binding.RequiresBinding(proof.Platform) {
		if err := s.checkBinding(puid, purchase); err != nil {
			s.audit(ctx, "iap.binding_mismatch", appID, puid,
				string(platformerr.CodeOf(err)), map[string]any{
					"platform":   string(proof.Platform),
					"product_id": proof.ProductID,
				})
		}
	}

	in := ledger.GrantInput{
		PlatformUserID: puid,
		EntitlementID:  entID,
		Purchase:       purchase,
	}

	switch purchase.State {
	case domain.StatePending:
		if err := led.RecordPending(ctx, in); err != nil {
			return Outcome{}, err
		}
		list, _ := led.ListActive(ctx, puid)
		return Outcome{
			Status:        "pending",
			EntitlementID: entID,
			TransactionID: purchase.ProviderOrderID,
			Entitlements:  orEmpty(list),
		}, nil

	case domain.StateRevoked:
		// 알림이 아니라 클라이언트 검증에서 revoked가 온 경우다.
		// 이미 환불된 구매를 다시 제시한 것이므로 원장에 반영만 한다.
		res, err := led.Grant(ctx, in)
		if err != nil {
			return Outcome{}, err
		}
		s.auditTransfer(ctx, appID, puid, in, res)
		list, _ := led.ListActive(ctx, puid)
		return Outcome{
			Status:        "revoked",
			EntitlementID: entID,
			TransactionID: purchase.ProviderOrderID,
			Entitlements:  orEmpty(list),
		}, nil

	case domain.StateActive:
		return s.grantAndComplete(ctx, appID, puid, v, led, in)

	default:
		return Outcome{}, platformerr.New(platformerr.CodePurchaseInvalid,
			"구매 정보를 확인할 수 없어요")
	}
}

func (s *Service) grantAndComplete(
	ctx context.Context,
	appID, puid string,
	v Verifier,
	led Ledger,
	in ledger.GrantInput,
) (Outcome, error) {
	res, err := led.Grant(ctx, in)
	if err != nil {
		return Outcome{}, err
	}

	if res.BlockedBySandboxReset {
		return s.completeSandboxReset(ctx, appID, puid, v, in, res)
	}

	s.audit(ctx, "iap.granted", appID, puid, "ok", map[string]any{
		"platform":        string(in.Purchase.Platform),
		"entitlement_id":  in.EntitlementID,
		"granted":         res.Granted,
		"already_granted": res.AlreadyGranted,
	})

	// 같은 마켓 계정의 다른 platform_user에게서 옮겨왔다.
	// 되돌릴 수 없는 조작이라 별도 action으로 남긴다. iap.granted에
	// 섞으면 나중에 "이 유저가 언제 무엇을 잃었나"를 찾을 수 없다.
	s.auditTransfer(ctx, appID, puid, in, res)

	out := Outcome{
		Status:        "verified",
		EntitlementID: in.EntitlementID,
		TransactionID: in.Purchase.ProviderOrderID,
		Entitlements:  orEmpty(res.Entitlements),
	}
	granted, already := res.Granted, res.AlreadyGranted
	out.Granted = &granted
	out.AlreadyGranted = &already

	action := s.completeGrant(ctx, appID, puid, v, in.Purchase)
	out.Completion = &action

	return out, nil
}

// auditTransfer는 active/revoked 경로의 검색용 감사 projection을 통일한다.
// 복구 가능한 최소 증거는 ledger transaction에 먼저 영구 저장되므로,
// 이 비동기 감사 기록이 실패해도 이전 소유권을 잃지 않는다. 불변식 4, 5.
func (s *Service) auditTransfer(
	ctx context.Context,
	appID, puid string,
	in ledger.GrantInput,
	res domain.GrantResult,
) {
	if res.TransferredFrom == "" {
		return
	}
	s.audit(ctx, "iap.transferred", appID, puid, "ok", map[string]any{
		"platform":         string(in.Purchase.Platform),
		"entitlement_id":   in.EntitlementID,
		"transferred_from": res.TransferredFrom,
	})
}

// completeSandboxReset은 초기화 이전 거래를 기기에서 떼어낸다.
//
// sandbox 구매내역을 지워도 기기에 남은 미완료 비소모품 거래는 사라지지
// 않는다. Product.purchase()가 구매 시트 없이 그 거래를 그대로 돌려주므로
// 몇 번을 눌러도 같은 자리를 맴돈다.
//
// 원장은 이미 revoked로 확정됐다. 여기서 Apple 거래를 finish해 기기 쪽
// 잔재를 정리하고, 클라이언트에게 sync 뒤 다시 사라고 알린다.
// finish가 실패해도 회수를 되돌리지 않는다. 다음 검증에서 다시 시도한다.
func (s *Service) completeSandboxReset(
	ctx context.Context,
	appID, puid string,
	v Verifier,
	in ledger.GrantInput,
	res domain.GrantResult,
) (Outcome, error) {
	// 이 경로는 finish 가능한 App Store 거래에만 성립한다.
	// 다른 마켓이 여기 오면 원장이나 provider가 어긋난 것이다.
	if in.Purchase.Platform != domain.PlatformAppStore ||
		in.Purchase.Completion != domain.CompletionAppleFinish {
		return Outcome{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"초기화 차단을 적용할 수 없는 구매예요")
	}

	outcome := "ok"
	if err := v.CompleteGrant(ctx, in.Purchase); err != nil {
		outcome = string(platformerr.CodeOf(err))
	}
	s.audit(ctx, "iap.sandbox_reset_blocked", appID, puid, outcome, map[string]any{
		"platform":       string(in.Purchase.Platform),
		"entitlement_id": in.EntitlementID,
	})

	return Outcome{
		Status:        "revoked",
		EntitlementID: in.EntitlementID,
		TransactionID: in.Purchase.ProviderOrderID,
		Entitlements:  orEmpty(res.Entitlements),
		Completion:    &Action{Action: domain.ActionAppStoreSyncAfterSandboxReset},
	}, nil
}

// completeGrant는 마켓에 완료를 알린다.
//
// 불변식 7. 실패해도 지급을 롤백하지 않는다.
// 반대로 하면 "돈은 나갔는데 물건이 없다"가 된다.
// 대신 outbox에 넣어 워커가 재시도한다.
func (s *Service) completeGrant(
	ctx context.Context,
	appID, puid string,
	v Verifier,
	p domain.VerifiedPurchase,
) Action {
	switch p.Completion {
	case domain.CompletionNone:
		return Action{Action: domain.ActionNone}

	case domain.CompletionAppsInTossClient:
		// AIT는 서버가 아니라 클라이언트가 완료 처리를 한다.
		return Action{
			Action:  domain.ActionAppsInTossCompleteGrant,
			OrderID: p.CanonicalID,
		}
	}

	if err := v.CompleteGrant(ctx, p); err != nil {
		// 지급은 이미 커밋됐다. 롤백하지 않는다.
		s.audit(ctx, "iap.completed", appID, puid, string(platformerr.CodeOf(err)), map[string]any{
			"platform": string(p.Platform),
		})
		if outbox := s.outboxFor(appID); outbox != nil {
			// outbox 적재도 실패하면 워커가 못 집는다.
			// 그래도 지급은 유지한다. 운영자가 원장에서 찾아 처리한다.
			_ = outbox.Enqueue(ctx, p.Key(), p)
		}
		return Action{Action: domain.ActionRetryServerCompletion}
	}

	s.audit(ctx, "iap.completed", appID, puid, "ok", map[string]any{
		"platform": string(p.Platform),
	})
	return Action{Action: domain.ActionNone}
}

func (s *Service) checkBinding(puid string, p domain.VerifiedPurchase) error {
	switch p.Platform {
	case domain.PlatformGooglePlay:
		return s.keyring.VerifyGoogle(puid, p.PlatformAccountID)
	case domain.PlatformAppStore:
		return s.keyring.VerifyApple(puid, p.PlatformAccountID)
	default:
		return nil
	}
}

// AccountReferences는 신규 구매 전에 클라이언트가 받아갈 계정 참조다.
func (s *Service) AccountReferences(puid string) (google, apple string, err error) {
	if s.keyring == nil {
		return "", "", platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"결제 설정이 준비되지 않았어요")
	}
	return s.keyring.GoogleAccountID(puid), s.keyring.AppleAccountToken(puid), nil
}

// ListEntitlements는 활성 entitlement를 돌려준다.
//
// 마켓 SDK 없이도 환불 반영을 확인할 수 있는 경로다.
func (s *Service) ListEntitlements(ctx context.Context, puid string) ([]string, error) {
	list, err := s.ledger.ListActive(ctx, puid)
	if err != nil {
		return nil, err
	}
	return orEmpty(list), nil
}

func (s *Service) ListEntitlementsForApp(ctx context.Context, appID, puid string) ([]string, error) {
	if s.apps == nil {
		return s.ListEntitlements(ctx, puid)
	}
	app, err := s.apps.GetUsable(ctx, appID)
	if err != nil {
		return nil, err
	}
	if !app.FeatureEnabled("iap") {
		return nil, platformerr.New(platformerr.CodeProductNotAllowed, "이 앱은 IAP를 사용하지 않아요")
	}
	list, err := s.ledgerFor(appID).ListActive(ctx, puid)
	if err != nil {
		return nil, err
	}
	return orEmpty(list), nil
}

func (s *Service) AccountReferencesForApp(ctx context.Context, appID, puid string) (string, string, error) {
	if s.apps == nil {
		return s.AccountReferences(puid)
	}
	app, err := s.apps.GetUsable(ctx, appID)
	if err != nil {
		return "", "", err
	}
	if !app.FeatureEnabled("iap") {
		return "", "", platformerr.New(platformerr.CodeProductNotAllowed, "이 앱은 IAP를 사용하지 않아요")
	}
	return s.AccountReferences(puid)
}

func (s *Service) audit(ctx context.Context, action, appID, puid, outcome string, detail map[string]any) {
	if s.auditor == nil {
		return
	}
	s.auditor.Record(ctx, action, appID, puid, outcome, detail)
}

// orEmpty는 nil 슬라이스를 빈 슬라이스로 만든다.
// JSON에서 null이 아니라 []로 나가야 클라이언트가 length를 바로 쓸 수 있다.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
