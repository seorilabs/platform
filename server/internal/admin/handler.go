package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
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
	adminAppIDPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	adminPlatformUserPattern = regexp.MustCompile(`^pu_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	adminEntitlementPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	adminSupportCodePattern  = regexp.MustCompile(`^[A-Z]{1,3}-[0-9A-HJKMNP-TV-Z]{8}$`)
	adminOrderKeyPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	adminProductIDPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,256}$`)
)

// Ledger는 운영 조회와 조작이다.
//
// 소비자인 이 패키지가 인터페이스를 정의한다. ledger.Ledger가 구현한다.
type Ledger interface {
	ListRecentOrders(ctx context.Context, limit int) ([]ledger.OrderSummary, error)
	ListUserEntitlements(ctx context.Context, puid string) ([]ledger.UserEntitlement, error)
	ListOperatorGrants(ctx context.Context, limit int) ([]ledger.OperatorRecord, error)
	ListOperatorRevocations(ctx context.Context, limit int) ([]ledger.OperatorRecord, error)
	FindOperatorReplay(ctx context.Context, in ledger.OperatorInput, revoke bool) (ledger.OperatorResult, bool, error)
	FindSandboxResetReplay(ctx context.Context, in ledger.SandboxResetInput) ([]string, bool, error)
	GetSandboxResetStatus(ctx context.Context, requestID string) (ledger.SandboxResetStatus, error)
	ResumeSandboxReset(ctx context.Context, requestID string) ([]string, error)
	CloseSandboxResetNotStarted(ctx context.Context, in ledger.SandboxResetClosureInput) (bool, error)
	OperatorGrant(ctx context.Context, in ledger.OperatorInput) (ledger.OperatorResult, error)
	OperatorRevoke(ctx context.Context, in ledger.OperatorInput) (ledger.OperatorResult, error)
	MarkSandboxReset(ctx context.Context, in ledger.SandboxResetInput) ([]string, error)
	CountDeadLetters(ctx context.Context) (int, error)
	ListRefundReviews(ctx context.Context, appID, state string, limit int) ([]ledger.RefundReviewSummary, error)
	FindRefundReviewDecisionReplay(ctx context.Context, in ledger.RefundReviewDecisionInput) (ledger.RefundReviewDecisionResult, bool, error)
	DecideRefundReview(ctx context.Context, in ledger.RefundReviewDecisionInput) (ledger.RefundReviewDecisionResult, error)
	RefundReviewHealth(ctx context.Context) (ledger.RefundReviewHealth, error)
	CheckAdminMutationRate(ctx context.Context, principal string) error
	// Environment는 원장 환경이다. 운영자가 지금 어느 쪽을 보는지
	// 화면에 띄워야 sandbox를 production으로 착각하지 않는다.
	Environment() domain.Environment
}

// Users는 PII 없는 고객지원 사용자 조회 포트다.
type Users interface {
	LookupSupportUser(ctx context.Context, reference string) (identity.SupportUser, error)
	// CountUsers는 개별 사용자를 특정하지 않는 집계다. 운영자가 지금
	// 보고 있는 플랫폼의 규모를 알아야 한 건짜리 조회 결과를 해석할 수
	// 있다.
	CountUsers(ctx context.Context, now time.Time) (identity.UserCounts, error)
}

// Apps는 GitHub registry/apps/*.json에서 동기화된 앱 설정 조회 포트다.
type Apps interface {
	Get(ctx context.Context, appID string) (registry.App, error)
	// List는 환경 대조용이다. 레지스트리와 원장 환경이 어긋나면 이 서비스의
	// 모든 조작이 422가 되는데, 결제는 계속 되어 아무도 모른다.
	List(ctx context.Context) ([]registry.App, error)
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
	now     func() time.Time
}

// WithClock은 시계를 주입한다. 테스트용이다.
func (h *Handler) WithClock(now func() time.Time) *Handler {
	h.now = now
	return h
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
		auth: auth, auditor: auditor, now: time.Now,
	}, nil
}

// Register는 라우트를 등록한다.
//
// 모든 라우트가 OIDC 인증을 거친다. 인증 없는 Admin 경로를 하나라도
// 열면 원장 전체가 노출된다.
func (h *Handler) Register(mux *http.ServeMux) {
	readRoutes := map[string]httpx.Handler{
		// 조회 — 등급 A
		"GET /v1/admin/orders/recent":                   h.recentOrders,
		"GET /v1/admin/users/{reference}":               h.user,
		"GET /v1/admin/users/{puid}/entitlements":       h.userEntitlements,
		"GET /v1/admin/operator-grants":                 h.operatorGrants,
		"GET /v1/admin/apps/{appId}/iap/catalog":        h.iapCatalog,
		"GET /v1/admin/apps/{appId}/iap/refund-reviews": h.refundReviews,
		"GET /v1/admin/iap/sandbox-resets/{requestId}":  h.sandboxResetStatus,
		"GET /v1/admin/health":                          h.health,
		"GET /v1/admin/metrics":                         h.metrics,
	}
	writeRoutes := map[string]httpx.Handler{
		// 조작 — 등급 C. reason과 requestId가 필수다
		"POST /v1/admin/entitlements/grant":  h.grantEntitlement,
		"POST /v1/admin/entitlements/revoke": h.revokeEntitlement,

		// sandbox 원장에서만 동작한다. production에서는 거부한다
		"POST /v1/admin/iap/sandbox-reset":                                   h.resetAppStoreSandbox,
		"POST /v1/admin/iap/sandbox-resets/{requestId}/resume":               h.resumeAppStoreSandboxReset,
		"POST /v1/admin/iap/sandbox-resets/{requestId}/close-not-started":    h.closeAppStoreSandboxResetNotStarted,
		"POST /v1/admin/apps/{appId}/iap/refund-reviews/{reviewId}/decision": h.decideRefundReview,

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

// iapCatalog는 선택한 앱에 허용된 entitlement ID만 준다. 전역 SKU
// 카탈로그와 앱 레지스트리 allowlist의 교집합이라 다른 앱 지급에 쓸 수 없다.
func (h *Handler) iapCatalog(w http.ResponseWriter, r *http.Request) error {
	appID := r.PathValue("appId")
	entitlements, err := h.appEntitlements(r.Context(), appID)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"appId":        appID,
		"entitlements": entitlements,
	})
	return nil
}

func (h *Handler) appEntitlements(ctx context.Context, appID string) ([]string, error) {
	if !adminAppIDPattern.MatchString(appID) {
		return nil, platformerr.New(platformerr.CodeRequestInvalid,
			"appId 형식이 올바르지 않아요")
	}
	app, err := h.apps.Get(ctx, appID)
	if err != nil {
		return nil, err
	}
	if err := app.EnsureUsable(); err != nil {
		return nil, err
	}
	if !app.FeatureEnabled("iap") || len(app.IAP.EntitlementIDs) == 0 {
		return nil, platformerr.New(platformerr.CodeAuthForbidden,
			"이 앱은 IAP 관리가 활성화되지 않았어요")
	}
	if domain.Environment(app.IAP.LedgerEnvironment) != h.ledger.Environment() {
		return nil, platformerr.New(platformerr.CodeEnvironmentMismatch,
			"앱 레지스트리와 Admin 원장 환경이 달라요")
	}

	result := append([]string(nil), app.IAP.EntitlementIDs...)
	for _, entitlementID := range result {
		// registry 검증을 우회한 잘못된 Firestore 문서도 여기서 fail-close한다.
		if !adminEntitlementPattern.MatchString(entitlementID) || !h.catalog.Has(entitlementID) {
			return nil, platformerr.New(platformerr.CodeCatalogIncomplete,
				"앱 entitlement allowlist와 SKU 카탈로그가 일치하지 않아요")
		}
	}
	sort.Strings(result)
	return result, nil
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
	refundHealth, err := h.ledger.RefundReviewHealth(r.Context())
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"environment":              string(h.ledger.Environment()),
		"deadLetterCount":          deadLetters,
		"environmentMismatches":    h.environmentMismatches(r.Context()),
		"pendingRefundReviewCount": refundHealth.Pending,
		"dueSoonRefundReviewCount": refundHealth.DueSoon,
		"failedRefundReviewCount":  refundHealth.Failed,
	})
	return nil
}

// metrics는 플랫폼 전체 사용자 규모다.
//
// health와 나눠 둔다. health는 운영자가 조작 전에 반드시 보는 값이라
// 싸고 빨라야 하는데, 지표는 컬렉션 집계라 성격이 다르다. 한 응답에
// 묶으면 집계가 느려지거나 실패할 때 조작 판단에 필요한 환경 표시까지
// 함께 사라진다. 백오피스 개요 화면이 실제로 그렇게 무너진 적이 있다.
func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) error {
	now := h.now()
	counts, err := h.users.CountUsers(r.Context(), now)
	if err != nil {
		return err
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"totalUsers":        counts.Total,
		"dailyActiveUsers":  counts.ActiveDay,
		"weeklyActiveUsers": counts.ActiveWeek,
		// 활성 판정 근거를 값으로 박는다. 나중에 이벤트 기반 집계를
		// 붙이면 같은 필드에 다른 정의가 들어올 수 있는데, 숫자만
		// 오면 화면이 조용히 뜻이 바뀐 값을 그대로 그린다.
		"activitySource": "session_last_seen",
		"measuredAt":     now.UTC().Format(time.RFC3339),
	})
	return nil
}

type adminRefundReview struct {
	ReviewID              string     `json:"reviewId"`
	AppID                 string     `json:"appId"`
	ExpectedEnvironment   string     `json:"expectedEnvironment"`
	State                 string     `json:"state"`
	RefundReason          int64      `json:"refundReason"`
	ReceivedAt            time.Time  `json:"receivedAt"`
	DueAt                 time.Time  `json:"dueAt"`
	RequestID             string     `json:"requestId,omitempty"`
	RefundPreference      string     `json:"refundPreference,omitempty"`
	SampleContentProvided *bool      `json:"sampleContentProvided,omitempty"`
	DecisionReason        string     `json:"decisionReason,omitempty"`
	DecidedAt             *time.Time `json:"decidedAt,omitempty"`
	RespondedAt           *time.Time `json:"respondedAt,omitempty"`
	FailedAt              *time.Time `json:"failedAt,omitempty"`
	ExpiredAt             *time.Time `json:"expiredAt,omitempty"`
	LastErrorCode         string     `json:"lastErrorCode,omitempty"`
}

// refundReviews는 token·orderId·ciphertext가 없는 명시 DTO만 반환한다.
func (h *Handler) refundReviews(w http.ResponseWriter, r *http.Request) error {
	appID := r.PathValue("appId")
	if err := h.validateRefundReviewAppContext(r.Context(), appID, string(h.ledger.Environment())); err != nil {
		return err
	}
	state := r.URL.Query().Get("state")
	items, err := h.ledger.ListRefundReviews(r.Context(), appID, state, parseLimit(r))
	if err != nil {
		return err
	}
	result := make([]adminRefundReview, 0, len(items))
	for _, item := range items {
		if item.AppID != appID || !adminOrderKeyPattern.MatchString(item.ReviewID) ||
			item.Environment != string(h.ledger.Environment()) || item.ReceivedAt.IsZero() || item.DueAt.IsZero() {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"환불 검토 원장에 브라우저로 노출할 수 없는 값이 있어요")
		}
		result = append(result, adminRefundReview{
			ReviewID: item.ReviewID, AppID: item.AppID,
			ExpectedEnvironment: item.Environment, State: item.State,
			RefundReason: item.RefundReason, ReceivedAt: item.ReceivedAt, DueAt: item.DueAt,
			RequestID: item.RequestID, RefundPreference: item.RefundPreference,
			SampleContentProvided: item.SampleContentProvided,
			DecisionReason:        item.DecisionReason, DecidedAt: timePointer(item.DecidedAt),
			RespondedAt: timePointer(item.RespondedAt), FailedAt: timePointer(item.FailedAt),
			ExpiredAt: timePointer(item.ExpiredAt), LastErrorCode: item.LastErrorCode,
		})
	}
	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"appId": appID, "environment": string(h.ledger.Environment()), "refundReviews": result,
	})
	return nil
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

type refundReviewDecisionRequest struct {
	RequestID             string `json:"requestId"`
	ExpectedEnvironment   string `json:"expectedEnvironment"`
	RefundPreference      string `json:"refundPreference"`
	SampleContentProvided *bool  `json:"sampleContentProvided"`
	Reason                string `json:"reason"`
	Confirmation          string `json:"confirmation"`
}

func (h *Handler) decideRefundReview(w http.ResponseWriter, r *http.Request) error {
	appID, reviewID := r.PathValue("appId"), r.PathValue("reviewId")
	var req refundReviewDecisionRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if !adminAppIDPattern.MatchString(appID) || !adminOrderKeyPattern.MatchString(reviewID) ||
		!ledger.ValidRefundReviewRequestID(req.RequestID) || req.SampleContentProvided == nil ||
		!ledger.ValidRefundReviewPreference(req.RefundPreference) ||
		!ledger.ValidRefundReviewDecisionReason(req.Reason) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"환불 검토 결정에 올바른 대상과 필수 값이 필요해요")
	}
	if req.ExpectedEnvironment != string(domain.EnvSandbox) &&
		req.ExpectedEnvironment != string(domain.EnvProduction) {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"expectedEnvironment가 올바르지 않아요")
	}
	wantConfirmation := fmt.Sprintf("RESPOND REFUND %s %s %s", appID, reviewID, req.RefundPreference)
	if req.Confirmation != wantConfirmation {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"typed confirmation이 환불 검토 결정과 맞지 않아요")
	}
	if req.ExpectedEnvironment != string(h.ledger.Environment()) {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"요청한 환경과 Admin 원장 환경이 달라요")
	}

	actor := ActorFrom(r.Context())
	input := ledger.RefundReviewDecisionInput{
		RequestID: req.RequestID, ReviewID: reviewID, AppID: appID,
		ExpectedEnvironment:   req.ExpectedEnvironment,
		RefundPreference:      req.RefundPreference,
		SampleContentProvided: *req.SampleContentProvided,
		Reason:                req.Reason, ActorLogin: actorLogin(actor),
	}
	if replay, found, err := h.ledger.FindRefundReviewDecisionReplay(r.Context(), input); err != nil {
		h.auditRefundReviewDecision(r.Context(), input, string(platformerr.CodeOf(err)))
		return err
	} else if found {
		if err := validateRefundReviewDecisionResult(replay, input); err != nil {
			h.auditRefundReviewDecision(r.Context(), input, string(platformerr.CodeOf(err)))
			return err
		}
		h.auditRefundReviewDecision(r.Context(), input, "already_applied")
		writeRefundReviewDecision(w, replay)
		return nil
	}
	if err := h.validateRefundReviewAppContext(r.Context(), appID, req.ExpectedEnvironment); err != nil {
		h.auditRefundReviewDecision(r.Context(), input, string(platformerr.CodeOf(err)))
		return err
	}
	if err := h.ledger.CheckAdminMutationRate(r.Context(), actor.Email); err != nil {
		h.auditRefundReviewDecision(r.Context(), input, string(platformerr.CodeOf(err)))
		return err
	}
	result, err := h.ledger.DecideRefundReview(r.Context(), input)
	if err != nil {
		h.auditRefundReviewDecision(r.Context(), input, string(platformerr.CodeOf(err)))
		return err
	}
	if err := validateRefundReviewDecisionResult(result, input); err != nil {
		h.auditRefundReviewDecision(r.Context(), input, string(platformerr.CodeOf(err)))
		return err
	}
	outcome := "ok"
	if !result.Applied {
		outcome = "already_applied"
	}
	h.auditRefundReviewDecision(r.Context(), input, outcome)
	writeRefundReviewDecision(w, result)
	return nil
}

func validateRefundReviewDecisionResult(
	result ledger.RefundReviewDecisionResult,
	input ledger.RefundReviewDecisionInput,
) error {
	if result.RequestID != input.RequestID || result.ReviewID != input.ReviewID ||
		result.AppID != input.AppID || result.ExpectedEnvironment != input.ExpectedEnvironment ||
		result.RefundPreference != input.RefundPreference ||
		result.SampleContentProvided != input.SampleContentProvided ||
		!validRefundReviewDecisionResultState(result.State) {
		return platformerr.New(platformerr.CodeLedgerStateInvalid,
			"환불 검토 결정 응답의 target binding이 올바르지 않아요")
	}
	return nil
}

// 결정 문서가 있으면 전달 worker의 현재 terminal 상태도 exact replay의
// 안정된 결과다. 실패·만료를 5xx로 바꾸면 이미 영구 확정된 command를
// Backoffice가 결과 불명으로 오인해 같은 request ID 복구를 막게 된다.
func validRefundReviewDecisionResultState(state string) bool {
	switch state {
	case ledger.RefundReviewDecided, ledger.RefundReviewResponded,
		ledger.RefundReviewExpired, ledger.RefundReviewFailed:
		return true
	default:
		return false
	}
}

func writeRefundReviewDecision(w http.ResponseWriter, result ledger.RefundReviewDecisionResult) {
	httpx.WriteOK(w, http.StatusAccepted, map[string]any{
		"applied": result.Applied, "requestId": result.RequestID,
		"reviewId": result.ReviewID, "appId": result.AppID,
		"expectedEnvironment": result.ExpectedEnvironment, "state": result.State,
		"refundPreference":      result.RefundPreference,
		"sampleContentProvided": result.SampleContentProvided,
		"operation":             "refund_review_decision",
	})
}

func (h *Handler) validateRefundReviewAppContext(
	ctx context.Context,
	appID, expectedEnvironment string,
) error {
	if !adminAppIDPattern.MatchString(appID) {
		return platformerr.New(platformerr.CodeRequestInvalid, "appId 형식이 올바르지 않아요")
	}
	if expectedEnvironment != string(h.ledger.Environment()) {
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
	if !app.FeatureEnabled("iap") || !app.MarketEnabled("google_play") ||
		app.IAP.GooglePlayPackageName == "" {
		return platformerr.New(platformerr.CodeAuthForbidden,
			"이 앱은 Google Play 환불 검토가 활성화되지 않았어요")
	}
	if string(app.IAP.LedgerEnvironment) != expectedEnvironment {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"앱 레지스트리와 요청한 원장 환경이 달라요")
	}
	return nil
}

func (h *Handler) auditRefundReviewDecision(
	ctx context.Context,
	in ledger.RefundReviewDecisionInput,
	outcome string,
) {
	if h.auditor == nil {
		return
	}
	h.auditor.Record(ctx, "iap.refund_review_decision", in.AppID, "", outcome,
		map[string]any{
			"request_id": in.RequestID, "review_id": in.ReviewID,
			"environment": in.ExpectedEnvironment, "actor": in.ActorLogin,
			"refund_preference":       in.RefundPreference,
			"sample_content_provided": in.SampleContentProvided,
			"reason":                  in.Reason,
		})
}

// environmentMismatch는 레지스트리와 이 서비스의 원장 환경이 어긋난 앱이다.
type environmentMismatch struct {
	AppID string `json:"appId"`
	// Registry는 레지스트리가 선언한 환경이다. 비어 있을 수 있다.
	Registry string `json:"registry"`
	// Ledger는 이 서비스가 실제로 읽고 쓰는 환경이다.
	Ledger string `json:"ledger"`
}

// environmentMismatches는 조작이 막힌 앱을 찾는다.
//
// 이걸 만든 이유는 증상이 한쪽에서만 나기 때문이다. LedgerEnvironment 검사는
// 이 패키지에만 있고 verify 경로에는 없다. 그래서 어긋나도 유저 결제는 계속
// 되고 운영자만 아무것도 못 하는 상태가 된다. 5xx도 안 나고 트래픽도 정상이라
// 대시보드로는 잡히지 않는다.
//
// 2026-08-03에 실제로 겪었다. 서비스를 production으로 전환했는데 레지스트리가
// sandbox로 남아 admin이 전부 422였고, 선물 한 건을 넣어보고 나서야 알았다.
//
// 조회 실패는 에러로 올리지 않는다. health는 진단 창구라 여기서 죽으면 진짜
// 문제를 볼 창구까지 같이 닫힌다. 대신 조회 실패 자체를 로그로 남긴다.
func (h *Handler) environmentMismatches(ctx context.Context) []environmentMismatch {
	apps, err := h.apps.List(ctx)
	if err != nil {
		slog.WarnContext(ctx, "환경 대조용 레지스트리 조회 실패", "err", err)
		return []environmentMismatch{}
	}

	want := string(h.ledger.Environment())
	// nil이 아니라 빈 슬라이스를 돌려준다. JSON에서 null과 []는 다르고,
	// 소비자가 length로 판단하는데 null이면 그 자리에서 터진다.
	out := []environmentMismatch{}
	for _, app := range apps {
		// IAP를 쓰지 않는 앱은 원장 환경이 의미가 없다. babycare처럼
		// 인증 브리지만 쓰는 앱을 경고에 섞으면 신호가 묻힌다.
		if !app.FeatureEnabled("iap") {
			continue
		}
		got := string(app.IAP.LedgerEnvironment)
		if got == want {
			continue
		}
		out = append(out, environmentMismatch{AppID: app.AppID, Registry: got, Ledger: want})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })

	if len(out) > 0 {
		ids := make([]string, 0, len(out))
		for _, m := range out {
			ids = append(ids, m.AppID)
		}
		// 이 한 줄이 알림의 근거가 된다. 로그 기반 지표로 걸 수 있게
		// 앱 목록과 기대 환경을 같이 남긴다.
		slog.WarnContext(ctx, "레지스트리와 원장 환경이 어긋나 조작이 막혔다",
			"ledger_environment", want,
			"apps", strings.Join(ids, ","),
			"count", len(out))
	}
	return out
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
		if !validAdminOrderSummary(orders[i]) {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"주문 원장에 브라우저로 노출할 수 없는 값이 있어요")
		}
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
					if user.PlatformUserID != puid || !adminAppIDPattern.MatchString(user.AppID) {
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

func validAdminOrderSummary(order ledger.OrderSummary) bool {
	if !adminOrderKeyPattern.MatchString(order.OrderKey) ||
		!domain.Platform(order.Platform).Valid() {
		return false
	}
	switch domain.State(order.State) {
	case domain.StateActive, domain.StatePending, domain.StateRevoked, domain.StateInvalid:
	default:
		return false
	}
	if order.Tombstone {
		return order.PlatformUserID == "" && order.EntitlementID == "" && order.ProductID == ""
	}
	return adminPlatformUserPattern.MatchString(order.PlatformUserID) &&
		adminEntitlementPattern.MatchString(order.EntitlementID) &&
		adminProductIDPattern.MatchString(order.ProductID)
}

// user는 platformUserId 또는 정확한 supportCode로 PII 없는 요약을 찾는다.
func (h *Handler) user(w http.ResponseWriter, r *http.Request) error {
	reference := r.PathValue("reference")
	if !adminPlatformUserPattern.MatchString(reference) &&
		!adminSupportCodePattern.MatchString(reference) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"정확한 platformUserId 또는 supportCode가 필요해요")
	}
	user, err := h.users.LookupSupportUser(r.Context(), reference)
	if err != nil {
		return err
	}
	if !adminPlatformUserPattern.MatchString(user.PlatformUserID) ||
		!adminAppIDPattern.MatchString(user.AppID) ||
		!adminSupportCodePattern.MatchString(user.SupportCode) {
		return platformerr.New(platformerr.CodeLedgerStateInvalid,
			"사용자 지원 문서가 올바르지 않아요")
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
		if !adminEntitlementPattern.MatchString(ent.EntitlementID) || ent.UpdatedAt.IsZero() {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"entitlement 원장에 브라우저로 노출할 수 없는 값이 있어요")
		}
		sources := make([]adminEntitlementSource, 0, len(ent.Sources))
		for _, src := range ent.Sources {
			if !validAdminEntitlementSource(src) {
				return platformerr.New(platformerr.CodeLedgerStateInvalid,
					"entitlement source에 브라우저로 노출할 수 없는 값이 있어요")
			}
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

func validAdminEntitlementSource(src ledger.EntitlementSource) bool {
	if !domain.Platform(src.Platform).Valid() ||
		!adminProductIDPattern.MatchString(src.ProductID) ||
		!adminOrderKeyPattern.MatchString(src.OrderKey) || src.Observed.IsZero() {
		return false
	}
	switch domain.State(src.State) {
	case domain.StateActive, domain.StatePending, domain.StateRevoked, domain.StateInvalid:
		return true
	default:
		return false
	}
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
	return h.applyOperator(w, r, operatorRequest(req),
		"iap.operator_revoke", true, h.ledger.OperatorRevoke)
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
	login := actorLogin(actor)
	if err := validateOperatorRequestShape(req, revoke); err != nil {
		h.audit(r.Context(), action, req, login, string(platformerr.CodeOf(err)))
		return err
	}
	if h.ledger.Environment() != domain.Environment(req.ExpectedEnvironment) {
		err := platformerr.New(platformerr.CodeEnvironmentMismatch,
			"요청한 환경과 Admin 원장 환경이 달라요")
		h.audit(r.Context(), action, req, login, string(platformerr.CodeOf(err)))
		return err
	}

	in := ledger.OperatorInput{
		RequestID:      req.RequestID,
		PlatformUserID: req.PlatformUserID,
		EntitlementID:  req.EntitlementID,
		ActorLogin:     login,
		Reason:         req.Reason,
		AppID:          req.AppID,
		GrantRequestID: req.GrantRequestID,
	}
	// commit 뒤 응답만 유실된 요청은 mutable app/user/catalog 상태나 rate
	// gate보다 먼저 원장을 읽는다. 같은 requestId의 결과 조회가 앱 pause나
	// 즉시 재시도 429 때문에 영구히 막히면 멱등 복구가 성립하지 않는다.
	if replay, found, err := h.ledger.FindOperatorReplay(r.Context(), in, revoke); err != nil {
		h.audit(r.Context(), action, req, login, string(platformerr.CodeOf(err)))
		return err
	} else if found {
		h.audit(r.Context(), action, req, login, "already_applied")
		writeOperatorResult(w, replay, req, revoke)
		return nil
	}

	if err := h.validateOperatorContext(r.Context(), req); err != nil {
		h.audit(r.Context(), action, req, login, string(platformerr.CodeOf(err)))
		return err
	}
	if err := h.ledger.CheckAdminMutationRate(r.Context(), actor.Email); err != nil {
		h.audit(r.Context(), action, req, login, string(platformerr.CodeOf(err)))
		return err
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
	writeOperatorResult(w, res, req, revoke)
	return nil
}

func writeOperatorResult(
	w http.ResponseWriter,
	res ledger.OperatorResult,
	req operatorRequest,
	revoke bool,
) {
	operation := "grant"
	if revoke {
		operation = "revoke"
	}
	result := map[string]any{
		"applied":             res.Applied,
		"entitlements":        res.Entitlements,
		"requestId":           req.RequestID,
		"appId":               req.AppID,
		"platformUserId":      req.PlatformUserID,
		"entitlementId":       req.EntitlementID,
		"expectedEnvironment": req.ExpectedEnvironment,
		"operation":           operation,
	}
	if revoke {
		result["grantRequestId"] = req.GrantRequestID
	}
	httpx.WriteOK(w, http.StatusOK, result)
}

func actorLogin(actor Actor) string {
	if actor.Login != "" {
		return actor.Login
	}
	if actor.Email == "" {
		// 인증된 principal이 없는데 감사 주체를 만들어 내지 않는다.
		// 정상 HTTP 경로에서는 allowlist 검증 전에 빈 이메일이 거부된다.
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(actor.Email))))
	return "oidc_sha256:" + hex.EncodeToString(sum[:])
}

func validateOperatorRequestShape(req operatorRequest, revoke bool) error {
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
	if req.ExpectedEnvironment != string(domain.EnvSandbox) &&
		req.ExpectedEnvironment != string(domain.EnvProduction) {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"expectedEnvironment가 올바르지 않아요")
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

func (h *Handler) validateOperatorContext(ctx context.Context, req operatorRequest) error {
	app, err := h.validateIAPContext(ctx, req.AppID, req.PlatformUserID, req.ExpectedEnvironment)
	if err != nil {
		return err
	}
	if !app.EntitlementAllowed(req.EntitlementID) {
		return platformerr.New(platformerr.CodeProductNotAllowed,
			"이 앱에 허용되지 않은 entitlement예요")
	}
	if !h.catalog.Has(req.EntitlementID) {
		return platformerr.New(platformerr.CodeCatalogIncomplete,
			"앱 entitlement allowlist와 SKU 카탈로그가 일치하지 않아요")
	}
	return nil
}

func (h *Handler) validateIAPContext(
	ctx context.Context,
	appID, puid, expectedEnvironment string,
) (registry.App, error) {
	env := domain.Environment(expectedEnvironment)
	if env != domain.EnvSandbox && env != domain.EnvProduction {
		return registry.App{}, platformerr.New(platformerr.CodeEnvironmentMismatch,
			"expectedEnvironment가 올바르지 않아요")
	}
	if h.ledger.Environment() != env {
		return registry.App{}, platformerr.New(platformerr.CodeEnvironmentMismatch,
			"요청한 환경과 Admin 원장 환경이 달라요")
	}

	app, err := h.apps.Get(ctx, appID)
	if err != nil {
		return registry.App{}, err
	}
	if err := app.EnsureUsable(); err != nil {
		return registry.App{}, err
	}
	if !app.FeatureEnabled("iap") || len(app.IAP.EntitlementIDs) == 0 {
		return registry.App{}, platformerr.New(platformerr.CodeAuthForbidden,
			"이 앱은 IAP 관리가 활성화되지 않았어요")
	}
	if string(app.IAP.LedgerEnvironment) != expectedEnvironment {
		return registry.App{}, platformerr.New(platformerr.CodeEnvironmentMismatch,
			"앱 레지스트리와 요청한 원장 환경이 달라요")
	}

	user, err := h.users.LookupSupportUser(ctx, puid)
	if err != nil {
		return registry.App{}, err
	}
	if user.PlatformUserID != puid || user.AppID != appID {
		return registry.App{}, platformerr.New(platformerr.CodeAuthForbidden,
			"사용자가 이 앱에 속하지 않아요")
	}
	return app, nil
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

// sandboxResetResumeRequest는 이미 영구 기록된 reset intent를 재개한다.
//
// platformUserId와 reason은 클라이언트가 다시 보내지 않는다. 재개 대상은
// immutable intent가 결정해야 응답 유실 뒤 사람이 payload를 잘못 재구성해도
// 다른 사용자로 바뀌지 않는다.
type sandboxResetResumeRequest struct {
	AppID        string `json:"appId"`
	Confirmation string `json:"confirmation"`
}

// sandboxResetCloseNotStartedRequest는 reset intent가 없던 unknown 실행을
// 영구 종결한다. PUID나 자유 서술을 받지 않아 closure는 PII-free다.
type sandboxResetCloseNotStartedRequest struct {
	AppID        string `json:"appId"`
	Confirmation string `json:"confirmation"`
}

type sandboxResetStatusResult struct {
	RequestID           string `json:"requestId"`
	AppID               string `json:"appId"`
	ExpectedEnvironment string `json:"expectedEnvironment"`
	Operation           string `json:"operation"`
	State               string `json:"state"`
}

// sandboxResetStatus는 durable reset intent가 준비 단계인지 완료 단계인지
// 보여준다. 백오피스는 이 상태를 확인하지 않고 unknown 실행을
// not_applied로 닫으면 안 된다.
func (h *Handler) sandboxResetStatus(w http.ResponseWriter, r *http.Request) error {
	requestID := r.PathValue("requestId")
	if !adminRequestIDPattern.MatchString(requestID) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"초기화 요청 식별자가 올바르지 않아요")
	}
	if h.ledger.Environment() != domain.EnvSandbox {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"sandbox 초기화 상태는 sandbox 원장에서만 조회할 수 있어요")
	}

	status, err := h.ledger.GetSandboxResetStatus(r.Context(), requestID)
	if err != nil {
		return err
	}
	result, err := makeSandboxResetStatusResult(requestID, status)
	if err != nil {
		return err
	}
	httpx.WriteOK(w, http.StatusOK, result)
	return nil
}

// resumeAppStoreSandboxReset은 phase 1 intent가 남은 reset의 phase 2를
// 재개한다. mutable 앱·사용자 상태와 rate gate는 다시 검사하지 않는다.
// intent가 이미 요청 시작을 확정했기 때문에 여기서 새 precondition을
// 적용하면 reset과 Grant의 선후관계가 깨질 수 있다.
func (h *Handler) resumeAppStoreSandboxReset(w http.ResponseWriter, r *http.Request) error {
	requestID := r.PathValue("requestId")
	var req sandboxResetResumeRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if !adminRequestIDPattern.MatchString(requestID) || !adminAppIDPattern.MatchString(req.AppID) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"재개 요청에 올바른 requestId와 appId가 필요해요")
	}
	wantConfirmation := fmt.Sprintf("RESUME RESET %s %s", req.AppID, requestID)
	if req.Confirmation != wantConfirmation {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"typed confirmation이 재개 요청과 맞지 않아요")
	}
	if h.ledger.Environment() != domain.EnvSandbox {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"sandbox 초기화는 sandbox 원장에서만 재개할 수 있어요")
	}

	// status의 appId와 immutable target을 먼저 묶는다. 경로의 requestId만
	// 알고 다른 앱 confirmation으로 재개하는 것을 허용하지 않는다.
	status, err := h.ledger.GetSandboxResetStatus(r.Context(), requestID)
	if err != nil {
		return err
	}
	if _, err := makeSandboxResetStatusResult(requestID, status); err != nil {
		return err
	}
	if status.AppID != req.AppID {
		return platformerr.New(platformerr.CodeOperatorReplayMismatch,
			"재개 요청의 appId가 최초 초기화 요청과 달라요")
	}
	if status.State == ledger.SandboxResetClosedNotStarted {
		return platformerr.New(platformerr.CodeSandboxResetClosed,
			"미시작으로 종결한 sandbox 초기화는 재개할 수 없어요")
	}

	login := actorLogin(ActorFrom(r.Context()))
	orderKeys, err := h.ledger.ResumeSandboxReset(r.Context(), requestID)
	if err != nil {
		h.auditSandboxResetResume(r.Context(), status, login,
			string(platformerr.CodeOf(err)), 0)
		return err
	}
	if err := validateSandboxResetOrderKeys(orderKeys); err != nil {
		h.auditSandboxResetResume(r.Context(), status, login,
			string(platformerr.CodeOf(err)), 0)
		return err
	}
	h.auditSandboxResetResume(r.Context(), status, login, "ok", len(orderKeys))
	writeSandboxResetResultValues(w, status.RequestID, status.AppID,
		status.PlatformUserID, orderKeys)
	return nil
}

// closeAppStoreSandboxResetNotStarted는 상태 조회와 로컬 unlock 사이 늦게
// 도착한 reset이 다시 시작되지 않도록 requestId를 원장에 영구 fence한다.
// mutable 앱·사용자 상태를 재검사하지 않는 이유는 복구 종결 가능성이 앱 pause나
// 사용자 삭제에 따라 사라지면 unknown 실행을 안전하게 닫을 수 없기 때문이다.
func (h *Handler) closeAppStoreSandboxResetNotStarted(w http.ResponseWriter, r *http.Request) error {
	requestID := r.PathValue("requestId")
	var req sandboxResetCloseNotStartedRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	if !adminRequestIDPattern.MatchString(requestID) || !adminAppIDPattern.MatchString(req.AppID) {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"종결 요청에 올바른 requestId와 appId가 필요해요")
	}
	wantConfirmation := fmt.Sprintf("CLOSE RESET %s %s", req.AppID, requestID)
	if req.Confirmation != wantConfirmation {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"typed confirmation이 종결 요청과 맞지 않아요")
	}
	if h.ledger.Environment() != domain.EnvSandbox {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"sandbox 초기화 종결은 sandbox 원장에서만 수행할 수 있어요")
	}

	login := actorLogin(ActorFrom(r.Context()))
	in := ledger.SandboxResetClosureInput{
		RequestID:  requestID,
		AppID:      req.AppID,
		ActorLogin: login,
	}
	applied, err := h.ledger.CloseSandboxResetNotStarted(r.Context(), in)
	if err != nil {
		h.auditSandboxResetCloseNotStarted(r.Context(), in,
			string(platformerr.CodeOf(err)), false)
		return err
	}
	outcome := "already_closed"
	if applied {
		outcome = "ok"
	}
	h.auditSandboxResetCloseNotStarted(r.Context(), in, outcome, applied)
	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"requestId":           requestID,
		"appId":               req.AppID,
		"expectedEnvironment": string(domain.EnvSandbox),
		"operation":           "sandbox_reset",
		"state":               string(ledger.SandboxResetClosedNotStarted),
		"applied":             applied,
	})
	return nil
}

func makeSandboxResetStatusResult(
	requestID string,
	status ledger.SandboxResetStatus,
) (sandboxResetStatusResult, error) {
	state := string(status.State)
	if status.RequestID != requestID ||
		!adminRequestIDPattern.MatchString(status.RequestID) ||
		!adminAppIDPattern.MatchString(status.AppID) {
		return sandboxResetStatusResult{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"sandbox 초기화 상태의 대상 binding이 올바르지 않아요")
	}
	switch status.State {
	case ledger.SandboxResetClosedNotStarted:
		if status.PlatformUserID != "" || !status.ResetAt.IsZero() || len(status.OrderKeys) != 0 {
			return sandboxResetStatusResult{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
				"미시작 종결 상태에 reset 대상이나 결과가 포함됐어요")
		}
	case ledger.SandboxResetPrepared, ledger.SandboxResetCompleted:
		if !adminPlatformUserPattern.MatchString(status.PlatformUserID) || status.ResetAt.IsZero() {
			return sandboxResetStatusResult{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 상태의 대상 binding이 올바르지 않아요")
		}
		if status.State == ledger.SandboxResetPrepared && len(status.OrderKeys) != 0 {
			return sandboxResetStatusResult{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
				"준비 중인 sandbox 초기화에 완료 결과가 포함됐어요")
		}
		if err := validateSandboxResetOrderKeys(status.OrderKeys); err != nil {
			return sandboxResetStatusResult{}, err
		}
	default:
		return sandboxResetStatusResult{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"sandbox 초기화 상태 값이 올바르지 않아요")
	}

	return sandboxResetStatusResult{
		RequestID:           status.RequestID,
		AppID:               status.AppID,
		ExpectedEnvironment: string(domain.EnvSandbox),
		Operation:           "sandbox_reset",
		State:               state,
	}, nil
}

func validateSandboxResetOrderKeys(orderKeys []string) error {
	previous := ""
	for _, orderKey := range orderKeys {
		if !adminOrderKeyPattern.MatchString(orderKey) ||
			(previous != "" && orderKey <= previous) {
			return platformerr.New(platformerr.CodeLedgerStateInvalid,
				"sandbox 초기화 결과의 주문 식별자가 올바르지 않아요")
		}
		previous = orderKey
	}
	return nil
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
	if h.ledger.Environment() != domain.EnvSandbox {
		return platformerr.New(platformerr.CodeEnvironmentMismatch,
			"sandbox 초기화 요청과 Admin 원장 환경이 달라요")
	}
	wantConfirmation := fmt.Sprintf("RESET %s %s", req.AppID, req.PlatformUserID)
	if req.Confirmation != wantConfirmation {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"typed confirmation이 요청 내용과 맞지 않아요")
	}

	actor := ActorFrom(r.Context())
	login := actorLogin(actor)
	in := ledger.SandboxResetInput{
		RequestID:      req.RequestID,
		PlatformUserID: req.PlatformUserID,
		AppID:          req.AppID,
		ActorLogin:     login,
		Reason:         req.Reason,
	}
	// exact retry는 mutable 앱·사용자 상태와 rate gate보다 먼저 원장에서
	// 확정한다. 초기화 commit 뒤 응답 유실도 같은 requestId로 복구해야 한다.
	if orderKeys, found, err := h.ledger.FindSandboxResetReplay(r.Context(), in); err != nil {
		h.auditSandboxReset(r.Context(), req, login, string(platformerr.CodeOf(err)), 0)
		return err
	} else if found {
		h.auditSandboxReset(r.Context(), req, login, "replayed_or_resumed", len(orderKeys))
		writeSandboxResetResult(w, req, orderKeys)
		return nil
	}

	if _, err := h.validateIAPContext(r.Context(), req.AppID, req.PlatformUserID, req.ExpectedEnvironment); err != nil {
		h.auditSandboxReset(r.Context(), req, login, string(platformerr.CodeOf(err)), 0)
		return err
	}
	if err := h.ledger.CheckAdminMutationRate(r.Context(), actor.Email); err != nil {
		h.auditSandboxReset(r.Context(), req, login, string(platformerr.CodeOf(err)), 0)
		return err
	}

	orderKeys, err := h.ledger.MarkSandboxReset(r.Context(), in)
	if err != nil {
		h.auditSandboxReset(r.Context(), req, login, string(platformerr.CodeOf(err)), 0)
		return err
	}
	h.auditSandboxReset(r.Context(), req, login, "ok", len(orderKeys))
	writeSandboxResetResult(w, req, orderKeys)
	return nil
}

func writeSandboxResetResult(w http.ResponseWriter, req sandboxResetRequest, orderKeys []string) {
	writeSandboxResetResultValues(w, req.RequestID, req.AppID, req.PlatformUserID, orderKeys)
}

func writeSandboxResetResultValues(
	w http.ResponseWriter,
	requestID, appID, platformUserID string,
	orderKeys []string,
) {
	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"requestId":           requestID,
		"appId":               appID,
		"platformUserId":      platformUserID,
		"expectedEnvironment": string(domain.EnvSandbox),
		"operation":           "sandbox_reset",
		"resetOrderKeys":      append([]string{}, orderKeys...),
	})
}

func (h *Handler) auditSandboxResetResume(
	ctx context.Context,
	status ledger.SandboxResetStatus,
	login, outcome string,
	count int,
) {
	if h.auditor == nil {
		return
	}
	h.auditor.Record(ctx, "iap.sandbox_reset_resume", status.AppID,
		status.PlatformUserID, outcome, map[string]any{
			"request_id":  status.RequestID,
			"environment": string(domain.EnvSandbox),
			"actor":       login,
			"state":       string(status.State),
			"order_count": count,
		})
}

func (h *Handler) auditSandboxResetCloseNotStarted(
	ctx context.Context,
	in ledger.SandboxResetClosureInput,
	outcome string,
	applied bool,
) {
	if h.auditor == nil {
		return
	}
	h.auditor.Record(ctx, "iap.sandbox_reset_close_not_started", in.AppID,
		"", outcome, map[string]any{
			"request_id":  in.RequestID,
			"environment": string(domain.EnvSandbox),
			"actor":       in.ActorLogin,
			"applied":     applied,
		})
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
	// 0은 점검 해제라는 유효한 값이므로 pointer로 필드 누락과 구분한다.
	Minutes *int `json:"minutes"`
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
	if !adminAppIDPattern.MatchString(req.AppID) || req.Minutes == nil ||
		*req.Minutes < 0 || *req.Minutes > 1440 {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"앱 식별자와 0~1440분의 점검 시간이 필요해요")
	}
	minutes := *req.Minutes
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
				string(platformerr.CodeOf(err)), map[string]any{"minutes": minutes, "actor": login})
		}
		return err
	}

	if err := h.config.SetMaintenance(r.Context(), req.AppID, minutes, login); err != nil {
		return err
	}

	outcome := "off"
	if minutes > 0 {
		outcome = "on"
	}
	if h.auditor != nil {
		h.auditor.Record(r.Context(), "config.maintenance", req.AppID, "", outcome,
			map[string]any{"minutes": minutes, "actor": login})
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"appId":   req.AppID,
		"active":  minutes > 0,
		"minutes": minutes,
	})
	return nil
}
