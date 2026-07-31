package admin

import (
	"context"
	"net/http"
	"strconv"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
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
	CountDeadLetters(ctx context.Context) (int, error)
	// Environment는 원장 환경이다. 운영자가 지금 어느 쪽을 보는지
	// 화면에 띄워야 sandbox를 production으로 착각하지 않는다.
	Environment() domain.Environment
}

// Auditor는 감사 원장에 기록한다.
type Auditor interface {
	Record(ctx context.Context, action, appID, puid, outcome string, detail map[string]any)
}

// Handler는 백오피스 전용 API다.
type Handler struct {
	ledger  Ledger
	config  Config
	auditor Auditor
	auth    *Authenticator
}

func NewHandler(l Ledger, cfg Config, auth *Authenticator, auditor Auditor) (*Handler, error) {
	if l == nil || auth == nil {
		return nil, platformerr.New(platformerr.CodeRuntimeConfigInvalid,
			"Admin API 설정이 올바르지 않아요")
	}
	return &Handler{ledger: l, config: cfg, auth: auth, auditor: auditor}, nil
}

// Register는 라우트를 등록한다.
//
// 모든 라우트가 OIDC 인증을 거친다. 인증 없는 Admin 경로를 하나라도
// 열면 원장 전체가 노출된다.
func (h *Handler) Register(mux *http.ServeMux) {
	routes := map[string]httpx.Handler{
		// 조회 — 등급 A
		"GET /v1/admin/orders/recent":             h.recentOrders,
		"GET /v1/admin/users/{puid}/entitlements": h.userEntitlements,
		"GET /v1/admin/operator-grants":           h.operatorGrants,
		"GET /v1/admin/health":                    h.health,

		// 조작 — 등급 C. reason과 requestId가 필수다
		"POST /v1/admin/entitlements/grant":  h.grantEntitlement,
		"POST /v1/admin/entitlements/revoke": h.revokeEntitlement,

		// break-glass. 백오피스가 죽어도 점검 모드는 켤 수 있어야 한다
		"POST /v1/admin/config/maintenance": h.setMaintenance,
	}

	for pattern, handler := range routes {
		mux.Handle(pattern, h.auth.Middleware(http.HandlerFunc(httpx.Wrap(handler))))
	}
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

// recentOrders는 최근 주문 목록이다. 기존 recent-purchases에 대응한다.
func (h *Handler) recentOrders(w http.ResponseWriter, r *http.Request) error {
	orders, err := h.ledger.ListRecentOrders(r.Context(), parseLimit(r))
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{"orders": orders})
	return nil
}

// userEntitlements는 사용자별 entitlement다. 기존 account-entitlements에 대응한다.
//
// 활성 여부와 무관하게 전부 준다. 왜 없는지를 봐야 CS가 가능하다.
func (h *Handler) userEntitlements(w http.ResponseWriter, r *http.Request) error {
	puid := r.PathValue("puid")
	if puid == "" {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"사용자 식별자가 필요해요")
	}

	list, err := h.ledger.ListUserEntitlements(r.Context(), puid)
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"platformUserId": puid,
		"entitlements":   list,
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
		"grants":      grants,
		"revocations": revocations,
	})
	return nil
}

// operatorRequest는 지급·회수 요청이다.
//
// requestId와 reason이 없으면 받지 않는다. 전자는 멱등을 위해,
// 후자는 나중에 왜 그랬는지 설명하기 위해 필요하다.
type operatorRequest struct {
	RequestID      string `json:"requestId"`
	PlatformUserID string `json:"platformUserId"`
	EntitlementID  string `json:"entitlementId"`
	Reason         string `json:"reason"`
	AppID          string `json:"appId"`
}

func (h *Handler) grantEntitlement(w http.ResponseWriter, r *http.Request) error {
	return h.applyOperator(w, r, "iap.operator_grant", h.ledger.OperatorGrant)
}

func (h *Handler) revokeEntitlement(w http.ResponseWriter, r *http.Request) error {
	return h.applyOperator(w, r, "iap.operator_revoke", h.ledger.OperatorRevoke)
}

func (h *Handler) applyOperator(
	w http.ResponseWriter,
	r *http.Request,
	action string,
	apply func(context.Context, ledger.OperatorInput) (ledger.OperatorResult, error),
) error {
	var req operatorRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}

	actor := ActorFrom(r.Context())

	// 누가 눌렀는지를 백오피스가 헤더로 넘긴다.
	// 없으면 서비스 계정으로 대체한다 — 최소한 어느 시스템인지는 남는다.
	login := actor.Login
	if login == "" {
		login = actor.Email
	}

	in := ledger.OperatorInput{
		RequestID:      req.RequestID,
		PlatformUserID: req.PlatformUserID,
		EntitlementID:  req.EntitlementID,
		ActorLogin:     login,
		Reason:         req.Reason,
		AppID:          req.AppID,
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

func (h *Handler) audit(ctx context.Context, action string, req operatorRequest, login, outcome string) {
	if h.auditor == nil {
		return
	}
	h.auditor.Record(ctx, action, req.AppID, req.PlatformUserID, outcome, map[string]any{
		"entitlement_id": req.EntitlementID,
		"request_id":     req.RequestID,
		"actor":          login,
		// reason은 사람이 쓴 자유 문장이다. 감사 원장에 남긴다.
		"reason": req.Reason,
	})
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
	if req.AppID == "" {
		return platformerr.New(platformerr.CodeRequestInvalid, "앱 식별자가 필요해요")
	}

	actor := ActorFrom(r.Context())
	login := actor.Login
	if login == "" {
		login = actor.Email
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
