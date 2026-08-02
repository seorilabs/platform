package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

var (
	adminRequestIDPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	adminAppIDPattern        = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	adminPlatformUserPattern = regexp.MustCompile(`^pu_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	adminEntitlementPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// Ledger는 운영 조회와 조작이다.
//
// 소비자인 이 패키지가 인터페이스를 정의한다. ledger.Ledger가 구현한다.
type Ledger interface {
	ListRecentOrders(ctx context.Context, limit int) ([]ledger.OrderSummary, error)
	ListUserEntitlements(ctx context.Context, puid string) ([]ledger.UserEntitlement, error)
	ListOperatorGrants(ctx context.Context, limit int) ([]ledger.OperatorRecord, error)
	ListOperatorRevocations(ctx context.Context, limit int) ([]ledger.OperatorRecord, error)
	OperatorGrant(ctx context.Context, in ledger.OperatorInput) (ledger.OperatorResult, error)
	OperatorRevoke(ctx context.Context, in ledger.OperatorInput) (ledger.OperatorResult, error)
	MarkSandboxReset(ctx context.Context, in ledger.SandboxResetInput) ([]string, error)
	CountDeadLetters(ctx context.Context) (int, error)
	CheckAdminMutationRate(ctx context.Context, principal string) error
	// Environment는 원장 환경이다. 운영자가 지금 어느 쪽을 보는지
	// 화면에 띄워야 sandbox를 production으로 착각하지 않는다.
	Environment() domain.Environment
}

// Users는 PII 없는 고객지원 사용자 조회 포트다.
type Users interface {
	LookupSupportUser(ctx context.Context, reference string) (identity.SupportUser, error)
}

// Apps는 GitHub registry/apps/*.json에서 동기화된 앱 설정 조회 포트다.
type Apps interface {
	Get(ctx context.Context, appID string) (registry.App, error)
}

// Catalog는 Admin role이 비밀 없이 읽는 entitlement allowlist다.
type Catalog interface {
	Has(entitlementID string) bool
	IDs() []string
}

// Auditor는 감사 원장에 기록한다.
type Auditor interface {
	Record(ctx context.Context, action, appID, puid, outcome string, detail map[string]any)
}

// Handler는 백오피스 전용 API다.
type Handler struct {
	ledger  Ledger
	config  Config
	users   Users
	apps    Apps
	catalog Catalog
	auditor Auditor
	auth    *Authenticator
}

func NewHandler(
	l Ledger,
	cfg Config,
	users Users,
	apps Apps,
	catalog Catalog,
	auth *Authenticator,
	auditor Auditor,
) (*Handler, error) {
	if l == nil || cfg == nil || users == nil || apps == nil || catalog == nil || auth == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"Admin API 설정이 올바르지 않아요")
	}
	return &Handler{
		ledger: l, config: cfg, users: users, apps: apps, catalog: catalog,
		auth: auth, auditor: auditor,
	}, nil
}

// Register는 라우트를 등록한다.
//
// 모든 라우트가 OIDC 인증을 거친다. 인증 없는 Admin 경로를 하나라도
// 열면 원장 전체가 노출된다.
func (h *Handler) Register(mux *http.ServeMux) {
	readRoutes := map[string]httpx.Handler{
		// 조회 — 등급 A
		"GET /v1/admin/orders/recent":             h.recentOrders,
		"GET /v1/admin/users/{reference}":         h.user,
		"GET /v1/admin/users/{puid}/entitlements": h.userEntitlements,
		"GET /v1/admin/operator-grants":           h.operatorGrants,
		"GET /v1/admin/iap/catalog":               h.iapCatalog,
		"GET /v1/admin/health":                    h.health,
	}
	writeRoutes := map[string]httpx.Handler{
		// 조작 — 등급 C. reason과 requestId가 필수다
		"POST /v1/admin/entitlements/grant":  h.grantEntitlement,
		"POST /v1/admin/entitlements/revoke": h.revokeEntitlement,

		// sandbox 원장에서만 동작한다. production에서는 거부한다
		"POST /v1/admin/iap/sandbox-reset": h.resetAppStoreSandbox,

		// break-glass. 백오피스가 죽어도 점검 모드는 켤 수 있어야 한다
		"POST /v1/admin/config/maintenance": h.setMaintenance,
	}

	for pattern, handler := range readRoutes {
		mux.Handle(pattern, h.auth.Middleware(AccessRead, http.HandlerFunc(httpx.Wrap(handler))))
	}
	for pattern, handler := range writeRoutes {
		mux.Handle(pattern, h.auth.Middleware(AccessWrite, http.HandlerFunc(httpx.Wrap(handler))))
	}
}

// iapCatalog는 조작 폼이 선택할 entitlement ID만 준다. SKU와 마켓
// 자격증명은 Admin API가 알거나 노출할 이유가 없다.
func (h *Handler) iapCatalog(w http.ResponseWriter, _ *http.Request) error {
	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"entitlements": h.catalog.IDs(),
	})
	return nil
}

// health는 운영 상태 요약이다.
//
// dead-letter가 0이 아니면 마켓에 완료를 알리지 못한 주문이 있다는 뜻이라
// 사람이 봐야 한다.
func (h *Handler) health(w http.ResponseWriter, r *http.Request) error {
	deadLetters, err := h.ledger.CountDeadLetters(r.Context())
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"environment":     string(h.ledger.Environment()),
		"deadLetterCount": deadLetters,
	})
	return nil
}

// adminOrder는 Admin API에 노출할 수 있는 주문 필드의 명시적 allowlist다.
// 내부 ledger struct를 그대로 직렬화하면 providerOrderId 같은 마켓 식별자가
// 의도치 않게 브라우저까지 전달될 수 있어 응답 경계를 별도로 둔다.
type adminOrder struct {
	OrderKey       string    `json:"orderKey"`
	AppID          string    `json:"appId"`
	PlatformUserID string    `json:"platformUserId"`
	EntitlementID  string    `json:"entitlementId"`
	Platform       string    `json:"platform"`
	ProductID      string    `json:"productId"`
	State          string    `json:"state"`
	PurchasedAt    time.Time `json:"purchasedAt"`
	ObservedAt     time.Time `json:"observedAt"`
	Tombstone      bool      `json:"tombstone"`
}

type adminUser struct {
	PlatformUserID string    `json:"platformUserId"`
	AppID          string    `json:"appId"`
	SupportCode    string    `json:"supportCode"`
	IsAnonymous    bool      `json:"isAnonymous"`
	CreatedAt      time.Time `json:"createdAt"`
	LastSeenAt     time.Time `json:"lastSeenAt"`
}

type adminEntitlementSource struct {
	Platform  string    `json:"platform"`
	ProductID string    `json:"productId"`
	State     string    `json:"state"`
	OrderKey  string    `json:"orderKey"`
	Observed  time.Time `json:"observedAt"`
}

type adminEntitlement struct {
	EntitlementID string                   `json:"entitlementId"`
	Active        bool                     `json:"active"`
	UpdatedAt     time.Time                `json:"updatedAt"`
	Sources       []adminEntitlementSource `json:"sources"`
}

type adminOperatorRecord struct {
	RequestID      string    `json:"requestId"`
	GrantRequestID string    `json:"grantRequestId,omitempty"`
	PlatformUserID string    `json:"platformUserId"`
	EntitlementID  string    `json:"entitlementId"`
	ActorLogin     string    `json:"actorLogin"`
	Reason         string    `json:"reason"`
	AppID          string    `json:"appId"`
	CreatedAt      time.Time `json:"createdAt"`
	Kind           string    `json:"kind"`
}

// recentOrders는 최근 주문 목록이다. 기존 recent-purchases에 대응한다.
func (h *Handler) recentOrders(w http.ResponseWriter, r *http.Request) error {
	orders, err := h.ledger.ListRecentOrders(r.Context(), parseLimit(r))
	if err != nil {
		return err
	}
	// processed_orders에는 appId가 없다. platformUserId의 PII 없는 identity
	// 문서에서만 확정한다. 삭제 사용자와 owner 없는 tombstone은 추측하지
	// 않고 빈 appId로 둔다.
	appByUser := make(map[string]string, len(orders))
	result := make([]adminOrder, 0, len(orders))
	for i := range orders {
		puid := orders[i].PlatformUserID
		appID := ""
		if puid != "" {
			var ok bool
			appID, ok = appByUser[puid]
			if !ok {
				user, lookupErr := h.users.LookupSupportUser(r.Context(), puid)
				if lookupErr != nil {
					if platformerr.CodeOf(lookupErr) != platformerr.CodeUserNotFound {
						return lookupErr
					}
				} else {
					if user.PlatformUserID != puid {
						return platformerr.New(platformerr.CodeLedgerStateInvalid,
							"주문의 사용자 binding이 올바르지 않아요")
					}
					appID = user.AppID
				}
				appByUser[puid] = appID
			}
		}

		result = append(result, adminOrder{
			OrderKey:       orders[i].OrderKey,
			AppID:          appID,
			PlatformUserID: orders[i].PlatformUserID,
			EntitlementID:  orders[i].EntitlementID,
			Platform:       orders[i].Platform,
			ProductID:      orders[i].ProductID,
			State:          orders[i].State,
			PurchasedAt:    orders[i].PurchasedAt,
			ObservedAt:     orders[i].ObservedAt,
			Tombstone:      orders[i].Tombstone,
		})
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{"orders": result})
	return nil
}

// user는 platformUserId 또는 정확한 supportCode로 PII 없는 요약을 찾는다.
func (h *Handler) user(w http.ResponseWriter, r *http.Request) error {
	user, err := h.users.LookupSupportUser(r.Context(), r.PathValue("reference"))
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, map[string]any{"user": adminUser{
		PlatformUserID: user.PlatformUserID,
		AppID:          user.AppID,
		SupportCode:    user.SupportCode,
		IsAnonymous:    user.IsAnonymous,
		CreatedAt:      user.CreatedAt,
		LastSeenAt:     user.LastSeenAt,
	}})
	return nil
}

// userEntitlements는 사용자별 entitlement다. 기존 account-entitlements에 대응한다.
//
// 활성 여부와 무관하게 전부 준다. 왜 없는지를 봐야 CS가 가능하다.
func (h *Handler) userEntitlements(w http.ResponseWriter, r *http.Request) error {
	puid := r.PathValue("puid")
	if !adminPlatformUserPattern.MatchString(puid) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"사용자 식별자가 올바르지 않아요")
	}

	list, err := h.ledger.ListUserEntitlements(r.Context(), puid)
	if err != nil {
		return err
	}

	result := make([]adminEntitlement, 0, len(list))
	for _, ent := range list {
		sources := make([]adminEntitlementSource, 0, len(ent.Sources))
		for _, src := range ent.Sources {
			sources = append(sources, adminEntitlementSource{
				Platform: src.Platform, ProductID: src.ProductID, State: src.State,
				OrderKey: src.OrderKey, Observed: src.Observed,
			})
		}
		result = append(result, adminEntitlement{
			EntitlementID: ent.EntitlementID,
			Active:        ent.Active,
			UpdatedAt:     ent.UpdatedAt,
			Sources:       sources,
		})
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"platformUserId": puid,
		"entitlements":   result,
	})
	return nil
}

// operatorGrants는 운영자 지급·회수 이력이다. 기존 production-grants에 대응한다.
func (h *Handler) operatorGrants(w http.ResponseWriter, r *http.Request) error {
	limit := parseLimit(r)

	grants, err := h.ledger.ListOperatorGrants(r.Context(), limit)
	if err != nil {
		return err
	}
	revocations, err := h.ledger.ListOperatorRevocations(r.Context(), limit)
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"grants":      adminOperatorRecords(grants),
		"revocations": adminOperatorRecords(revocations),
	})
	return nil
}

func adminOperatorRecords(records []ledger.OperatorRecord) []adminOperatorRecord {
	out := make([]adminOperatorRecord, 0, len(records))
	for _, rec := range records {
		out = append(out, adminOperatorRecord{
			RequestID:      rec.RequestID,
			GrantRequestID: rec.GrantRequestID,
			PlatformUserID: rec.PlatformUserID,
			EntitlementID:  rec.EntitlementID,
			ActorLogin:     rec.ActorLogin,
			Reason:         rec.Reason,
			AppID:          rec.AppID,
			CreatedAt:      rec.CreatedAt,
			Kind:           rec.Kind,
		})
	}
	return out
}

// operatorRequest는 지급·회수 요청이다.
//
// requestId와 고정 reason 코드가 없으면 받지 않는다. 전자는 멱등을 위해,
// 후자는 PII 없이 왜 조작했는지 분류하기 위해 필요하다.
type operatorMutationRequest struct {
	RequestID           string `json:"requestId"`
	PlatformUserID      string `json:"platformUserId"`
	EntitlementID       string `json:"entitlementId"`
	Reason              string `json:"reason"`
	AppID               string `json:"appId"`
	ExpectedEnvironment string `json:"expectedEnvironment"`
	Confirmation        string `json:"confirmation"`
}

type grantRequest struct {
	operatorMutationRequest
}

type revokeRequest struct {
	operatorMutationRequest
	GrantRequestID string `json:"grantRequestId"`
}

// operatorRequest는 wire 형식을 통합한 내부 값이다. grant와 revoke의 JSON
// struct는 분리해 grant 요청에 grantRequestId가 섞이면 빈 값이어도 거부한다.
type operatorRequest struct {
	operatorMutationRequest
	GrantRequestID string
}

func (h *Handler) grantEntitlement(w http.ResponseWriter, r *http.Request) error {
	var req grantRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	return h.applyOperator(w, r, operatorRequest{operatorMutationRequest: req.operatorMutationRequest},
		"iap.operator_grant", false, h.ledger.OperatorGrant)
}

func (h *Handler) revokeEntitlement(w http.ResponseWriter, r *http.Request) error {
	var req revokeRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	return h.applyOperator(w, r, operatorRequest{
		operatorMutationRequest: req.operatorMutationRequest,
		GrantRequestID:          req.GrantRequestID,
	}, "iap.operator_revoke", true, h.ledger.OperatorRevoke)
}

func (h *Handler) applyOperator(
	w http.ResponseWriter,
	r *http.Request,
	req operatorRequest,
	action string,
	revoke bool,
	apply func(context.Context, ledger.OperatorInput) (ledger.OperatorResult, error),
) error {
	actor := ActorFrom(r.Context())
	if err := h.validateOperatorRequest(r.Context(), req, revoke); err != nil {
		h.audit(r.Context(), action, req, actorLogin(actor), string(platformerr.CodeOf(err)))
		return err
	}
	if err := h.ledger.CheckAdminMutationRate(r.Context(), actor.Email); err != nil {
		h.audit(r.Context(), action, req, actorLogin(actor), string(platformerr.CodeOf(err)))
		return err
	}

	// 누가 눌렀는지를 백오피스가 헤더로 넘긴다.
	// 없으면 서비스 계정으로 대체한다 — 최소한 어느 시스템인지는 남는다.
	login := actorLogin(actor)

	in := ledger.OperatorInput{
		RequestID:      req.RequestID,
		PlatformUserID: req.PlatformUserID,
		EntitlementID:  req.EntitlementID,
		ActorLogin:     login,
		Reason:         req.Reason,
		AppID:          req.AppID,
		GrantRequestID: req.GrantRequestID,
	}

	res, err := apply(r.Context(), in)
	if err != nil {
		h.audit(r.Context(), action, req, login, string(platformerr.CodeOf(err)))
		return err
	}

	outcome := "ok"
	if !res.Applied {
		// 이미 처리된 요청이다. 실패가 아니다.
		outcome = "already_applied"
	}
	h.audit(r.Context(), action, req, login, outcome)

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"applied":      res.Applied,
		"entitlements": res.Entitlements,
	})
	return nil
}

func actorLogin(actor Actor) string {
	if actor.Login != "" {
		return actor.Login
	}
	if actor.Email == "" {
		return "oidc-principal"
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(actor.Email))))
	return "oidc_sha256:" + hex.EncodeToString(sum[:])
}

func (h *Handler) validateOperatorRequest(
	ctx context.Context,
	req operatorRequest,
	revoke bool,
) error {
	if !adminRequestIDPattern.MatchString(req.RequestID) ||
		!adminPlatformUserPattern.MatchString(req.PlatformUserID) ||
		!adminEntitlementPattern.MatchString(req.EntitlementID) ||
		!ledger.ValidAdminMutationReason(req.Reason) || !adminAppIDPattern.MatchString(req.AppID) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"requestId, appId, platformUserId, entitlementId와 허용된 reason이 필요해요")
	}
	if revoke && !adminRequestIDPattern.MatchString(req.GrantRequestID) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"회수할 grantRequestId가 필요해요")
	}
	if !revoke && req.GrantRequestID != "" {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"지급 요청에는 grantRequestId를 넣을 수 없어요")
	}
	if err := h.validateIAPContext(ctx, req.AppID, req.PlatformUserID, req.ExpectedEnvironment); err != nil {
		return err
	}
	if !h.catalog.Has(req.EntitlementID) {
		return platformerr.New(platformerr.CodeProductNotAllowed,
			"카탈로그에 없는 entitlement예요")
	}

	want := fmt.Sprintf("GRANT %s %s %s", req.AppID, req.PlatformUserID, req.EntitlementID)
	if revoke {
		want = fmt.Sprintf("REVOKE %s %s %s %s",
			req.AppID, req.PlatformUserID, req.EntitlementID, req.GrantRequestID)
	}
	if req.Confirmation != want {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"typed confirmation이 요청 내용과 맞지 않아요")
	}
	return nil
}

func (h *Handler) validateIAPContext(
	ctx context.Context,
	appID, puid, expectedEnvironment string,
) error {
	env := domain.Environment(expectedEnvironment)
	if env != domain.EnvSandbox && env != domain.EnvProduction {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"expectedEnvironment가 올바르지 않아요")
	}
	if h.ledger.Environment() != env {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"요청한 환경과 Admin 원장 환경이 달라요")
	}

	app, err := h.apps.Get(ctx, appID)
	if err != nil {
		return err
	}
	if err := app.EnsureUsable(); err != nil {
		return err
	}
	if !app.FeatureEnabled("iap") {
		return platformerr.New(platformerr.CodeAuthForbidden,
			"이 앱은 IAP 관리가 활성화되지 않았어요")
	}
	if string(app.IAP.LedgerEnvironment) != expectedEnvironment {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"앱 레지스트리와 요청한 원장 환경이 달라요")
	}

	user, err := h.users.LookupSupportUser(ctx, puid)
	if err != nil {
		return err
	}
	if user.PlatformUserID != puid || user.AppID != appID {
		return platformerr.New(platformerr.CodeAuthForbidden,
			"사용자가 이 앱에 속하지 않아요")
	}
	return nil
}

// sandboxResetRequest는 App Store sandbox 초기화 요청이다.
//
// AppleClearedConfirmed는 App Store Connect에서 구매내역을 실제로
// 지웠다는 운영자 확인이다. 플랫폼은 App Store Connect API 자격증명을
// 갖고 있지 않아 이걸 스스로 확인할 수 없다.
//
// 확인 없이 원장만 지우면 더 나쁜 상태가 된다. Apple에는 거래가
// 남았는데 원장에는 없으니, 다음 검증이 그 거래를 새 구매로 보고
// 다시 지급한다. 초기화한 줄 알았던 테스터가 상품을 그대로 갖는다.
type sandboxResetRequest struct {
	RequestID             string `json:"requestId"`
	PlatformUserID        string `json:"platformUserId"`
	Reason                string `json:"reason"`
	AppID                 string `json:"appId"`
	ExpectedEnvironment   string `json:"expectedEnvironment"`
	Confirmation          string `json:"confirmation"`
	AppleClearedConfirmed bool   `json:"appleClearedConfirmed"`
}

// resetAppStoreSandbox는 sandbox 구매내역 초기화를 원장에 반영한다.
//
// 기기에 남은 미완료 거래를 떼어내는 출발점이다. 여기서 표식을 남겨야
// 다음 검증이 그 거래를 재지급 대상이 아니라 정리 대상으로 본다.
func (h *Handler) resetAppStoreSandbox(w http.ResponseWriter, r *http.Request) error {
	var req sandboxResetRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}

	if !adminRequestIDPattern.MatchString(req.RequestID) ||
		!adminPlatformUserPattern.MatchString(req.PlatformUserID) ||
		!ledger.ValidAdminMutationReason(req.Reason) || !adminAppIDPattern.MatchString(req.AppID) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"초기화 요청에 requestId, appId, platformUserId와 허용된 reason이 필요해요")
	}
	if !req.AppleClearedConfirmed {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"App Store Connect에서 구매내역을 먼저 지워야 해요")
	}
	if req.ExpectedEnvironment != string(domain.EnvSandbox) {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"sandbox 초기화는 expectedEnvironment가 sandbox여야 해요")
	}
	if err := h.validateIAPContext(r.Context(), req.AppID, req.PlatformUserID, req.ExpectedEnvironment); err != nil {
		return err
	}
	wantConfirmation := fmt.Sprintf("RESET %s %s", req.AppID, req.PlatformUserID)
	if req.Confirmation != wantConfirmation {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"typed confirmation이 요청 내용과 맞지 않아요")
	}

	actor := ActorFrom(r.Context())
	login := actorLogin(actor)
	if err := h.ledger.CheckAdminMutationRate(r.Context(), actor.Email); err != nil {
		h.auditSandboxReset(r.Context(), req, login, string(platformerr.CodeOf(err)), 0)
		return err
	}

	orderKeys, err := h.ledger.MarkSandboxReset(r.Context(), ledger.SandboxResetInput{
		RequestID:      req.RequestID,
		PlatformUserID: req.PlatformUserID,
		AppID:          req.AppID,
		ActorLogin:     login,
		Reason:         req.Reason,
	})
	if err != nil {
		h.auditSandboxReset(r.Context(), req, login, string(platformerr.CodeOf(err)), 0)
		return err
	}
	h.auditSandboxReset(r.Context(), req, login, "ok", len(orderKeys))

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"platformUserId": req.PlatformUserID,
		"resetOrderKeys": orderKeys,
	})
	return nil
}

func (h *Handler) auditSandboxReset(
	ctx context.Context,
	req sandboxResetRequest,
	login, outcome string,
	count int,
) {
	if h.auditor == nil {
		return
	}
	h.auditor.Record(ctx, "iap.sandbox_reset", req.AppID, req.PlatformUserID, outcome,
		map[string]any{
			"request_id":  req.RequestID,
			"environment": req.ExpectedEnvironment,
			"actor":       login,
			"reason":      req.Reason,
			"order_count": count,
		})
}

func (h *Handler) audit(ctx context.Context, action string, req operatorRequest, login, outcome string) {
	if h.auditor == nil {
		return
	}
	appID, puid := "", ""
	if adminAppIDPattern.MatchString(req.AppID) {
		appID = req.AppID
	}
	if adminPlatformUserPattern.MatchString(req.PlatformUserID) {
		puid = req.PlatformUserID
	}
	detail := map[string]any{"actor": login}
	if adminRequestIDPattern.MatchString(req.RequestID) {
		detail["request_id"] = req.RequestID
	}
	if adminRequestIDPattern.MatchString(req.GrantRequestID) {
		detail["grant_request_id"] = req.GrantRequestID
	}
	if adminEntitlementPattern.MatchString(req.EntitlementID) {
		detail["entitlement_id"] = req.EntitlementID
	}
	if req.ExpectedEnvironment == string(domain.EnvSandbox) ||
		req.ExpectedEnvironment == string(domain.EnvProduction) {
		detail["environment"] = req.ExpectedEnvironment
	}
	if ledger.ValidAdminMutationReason(req.Reason) {
		detail["reason"] = req.Reason
	}
	h.auditor.Record(ctx, action, appID, puid, outcome, detail)
}

// parseLimit은 조회 상한을 읽는다.
//
// 잘못된 값은 무시하고 기본값을 쓴다. 백오피스가 실수로 전체를
// 긁어가지 못하게 원장 쪽에서도 상한을 다시 건다.
func parseLimit(r *http.Request) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// Config는 RemoteConfig 조작이다.
//
// break-glass 절차가 부르는 경로다. 백오피스가 죽어도 점검 모드는
// 켤 수 있어야 한다 — 그게 R1의 실질이다.
type Config interface {
	SetMaintenance(ctx context.Context, appID string, minutes int, actor string) error
}

// maintenanceRequest는 점검 모드 요청이다.
//
// 본문 텍스트를 받지 않는다. 시간과 앱만 받고 문구는 서버가 갖고 있다.
// 장애 중에 자유 텍스트 입력이나 외부 LLM 호출에 의존하면 안 된다.
type maintenanceRequest struct {
	AppID string `json:"appId"`
	// Minutes가 0 이하면 점검 모드를 끈다.
	Minutes int `json:"minutes"`
}

func (h *Handler) setMaintenance(w http.ResponseWriter, r *http.Request) error {
	if h.config == nil {
		return platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"설정 서비스가 준비되지 않았어요")
	}

	var req maintenanceRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if !adminAppIDPattern.MatchString(req.AppID) || req.Minutes < 0 || req.Minutes > 1440 {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"앱 식별자와 0~1440분의 점검 시간이 필요해요")
	}
	app, err := h.apps.Get(r.Context(), req.AppID)
	if err != nil {
		return err
	}
	if err := app.EnsureUsable(); err != nil {
		return err
	}

	actor := ActorFrom(r.Context())
	login := actorLogin(actor)
	if err := h.ledger.CheckAdminMutationRate(r.Context(), actor.Email); err != nil {
		if h.auditor != nil {
			h.auditor.Record(r.Context(), "config.maintenance", req.AppID, "",
				string(platformerr.CodeOf(err)), map[string]any{"minutes": req.Minutes, "actor": login})
		}
		return err
	}

	if err := h.config.SetMaintenance(r.Context(), req.AppID, req.Minutes, login); err != nil {
		return err
	}

	outcome := "off"
	if req.Minutes > 0 {
		outcome = "on"
	}
	if h.auditor != nil {
		h.auditor.Record(r.Context(), "config.maintenance", req.AppID, "", outcome,
			map[string]any{"minutes": req.Minutes, "actor": login})
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"appId":   req.AppID,
		"active":  req.Minutes > 0,
		"minutes": req.Minutes,
	})
	return nil
}
