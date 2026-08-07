package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

const (
	backofficeSA     = "backoffice-admin@seorilabs-platform.iam.gserviceaccount.com"
	backofficeReadSA = "backoffice-read@seorilabs-platform.iam.gserviceaccount.com"
	testPUID         = "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

const (
	testGrantReason  = ledger.AdminReasonCustomerSupportCompensation
	testRevokeReason = ledger.AdminReasonIncorrectGrantCorrection
	testResetReason  = ledger.AdminReasonInternalValidation
)

// fakeValidator는 OIDC 검증을 대신한다.
type fakeValidator struct {
	email string
	err   error
}

func (f *fakeValidator) Validate(context.Context, string) (string, error) {
	return f.email, f.err
}

// fakeLedger는 원장을 대신한다.
type fakeLedger struct {
	orders              []ledger.OrderSummary
	entitlements        []ledger.UserEntitlement
	grants              []ledger.OperatorRecord
	revocations         []ledger.OperatorRecord
	deadLetters         int
	refundReviews       []ledger.RefundReviewSummary
	refundHealth        ledger.RefundReviewHealth
	refundDecisionCalls []ledger.RefundReviewDecisionInput
	refundReplayCalls   []ledger.RefundReviewDecisionInput
	refundDecision      ledger.RefundReviewDecisionResult
	refundReplayFound   bool
	refundReplayErr     error
	err                 error

	grantCalls  []ledger.OperatorInput
	revokeCalls []ledger.OperatorInput
	applied     bool
	replayCalls []ledger.OperatorInput
	replay      ledger.OperatorResult
	replayFound bool
	replayErr   error

	resetKeys         []string
	resetCalls        []ledger.SandboxResetInput
	resetReplayCalls  []ledger.SandboxResetInput
	resetReplayKeys   []string
	resetReplayFound  bool
	resetReplayErr    error
	resetStatus       ledger.SandboxResetStatus
	resetStatusCalls  []string
	resetStatusErr    error
	resetResumeKeys   []string
	resetResumeCalls  []string
	resetResumeErr    error
	resetCloseApplied bool
	resetCloseCalls   []ledger.SandboxResetClosureInput
	resetCloseErr     error
	rateCalls         []string
	rateErr           error
	// env가 비어 있으면 sandbox로 본다. production 거부를 볼 때만 채운다.
	env domain.Environment
}

func (f *fakeLedger) ListRecentOrders(context.Context, int) ([]ledger.OrderSummary, error) {
	return f.orders, f.err
}
func (f *fakeLedger) ListUserEntitlements(_ context.Context, _ string) ([]ledger.UserEntitlement, error) {
	return f.entitlements, f.err
}
func (f *fakeLedger) ListOperatorGrants(context.Context, int) ([]ledger.OperatorRecord, error) {
	return f.grants, f.err
}
func (f *fakeLedger) ListOperatorRevocations(context.Context, int) ([]ledger.OperatorRecord, error) {
	return f.revocations, f.err
}
func (f *fakeLedger) FindOperatorReplay(
	_ context.Context,
	in ledger.OperatorInput,
	_ bool,
) (ledger.OperatorResult, bool, error) {
	f.replayCalls = append(f.replayCalls, in)
	return f.replay, f.replayFound, f.replayErr
}
func (f *fakeLedger) FindSandboxResetReplay(
	_ context.Context,
	in ledger.SandboxResetInput,
) ([]string, bool, error) {
	f.resetReplayCalls = append(f.resetReplayCalls, in)
	return f.resetReplayKeys, f.resetReplayFound, f.resetReplayErr
}
func (f *fakeLedger) GetSandboxResetStatus(
	_ context.Context,
	requestID string,
) (ledger.SandboxResetStatus, error) {
	f.resetStatusCalls = append(f.resetStatusCalls, requestID)
	return f.resetStatus, f.resetStatusErr
}
func (f *fakeLedger) ResumeSandboxReset(_ context.Context, requestID string) ([]string, error) {
	f.resetResumeCalls = append(f.resetResumeCalls, requestID)
	return f.resetResumeKeys, f.resetResumeErr
}
func (f *fakeLedger) CloseSandboxResetNotStarted(
	_ context.Context,
	in ledger.SandboxResetClosureInput,
) (bool, error) {
	f.resetCloseCalls = append(f.resetCloseCalls, in)
	return f.resetCloseApplied, f.resetCloseErr
}
func (f *fakeLedger) OperatorGrant(_ context.Context, in ledger.OperatorInput) (ledger.OperatorResult, error) {
	f.grantCalls = append(f.grantCalls, in)
	if f.err != nil {
		return ledger.OperatorResult{}, f.err
	}
	return ledger.OperatorResult{Applied: f.applied, Entitlements: []string{in.EntitlementID}}, nil
}
func (f *fakeLedger) OperatorRevoke(_ context.Context, in ledger.OperatorInput) (ledger.OperatorResult, error) {
	f.revokeCalls = append(f.revokeCalls, in)
	if f.err != nil {
		return ledger.OperatorResult{}, f.err
	}
	return ledger.OperatorResult{Applied: f.applied, Entitlements: []string{}}, nil
}
func (f *fakeLedger) CountDeadLetters(context.Context) (int, error) {
	return f.deadLetters, f.err
}
func (f *fakeLedger) ListRefundReviews(context.Context, string, string, int) ([]ledger.RefundReviewSummary, error) {
	return f.refundReviews, f.err
}
func (f *fakeLedger) FindRefundReviewDecisionReplay(
	_ context.Context, in ledger.RefundReviewDecisionInput,
) (ledger.RefundReviewDecisionResult, bool, error) {
	f.refundReplayCalls = append(f.refundReplayCalls, in)
	return f.refundDecision, f.refundReplayFound, f.refundReplayErr
}
func (f *fakeLedger) DecideRefundReview(
	_ context.Context, in ledger.RefundReviewDecisionInput,
) (ledger.RefundReviewDecisionResult, error) {
	f.refundDecisionCalls = append(f.refundDecisionCalls, in)
	if f.err != nil {
		return ledger.RefundReviewDecisionResult{}, f.err
	}
	result := f.refundDecision
	if result.RequestID == "" {
		result = ledger.RefundReviewDecisionResult{
			Applied: true, RequestID: in.RequestID, ReviewID: in.ReviewID,
			AppID: in.AppID, ExpectedEnvironment: in.ExpectedEnvironment,
			State: ledger.RefundReviewDecided, RefundPreference: in.RefundPreference,
			SampleContentProvided: in.SampleContentProvided,
		}
	}
	return result, nil
}
func (f *fakeLedger) RefundReviewHealth(context.Context) (ledger.RefundReviewHealth, error) {
	return f.refundHealth, f.err
}
func (f *fakeLedger) MarkSandboxReset(_ context.Context, in ledger.SandboxResetInput) ([]string, error) {
	f.resetCalls = append(f.resetCalls, in)
	if f.err != nil {
		return nil, f.err
	}
	return f.resetKeys, nil
}
func (f *fakeLedger) CheckAdminMutationRate(_ context.Context, principal string) error {
	f.rateCalls = append(f.rateCalls, principal)
	return f.rateErr
}

func (f *fakeLedger) Environment() domain.Environment {
	if f.env == "" {
		return domain.EnvSandbox
	}
	return f.env
}

// fakeConfig는 RemoteConfig 조작을 대신한다.
type fakeConfig struct {
	calls []maintenanceCall
	err   error
}

type maintenanceCall struct {
	appID   string
	minutes int
	actor   string
}

type fakeUsers struct {
	user       identity.SupportUser
	err        error
	references []string

	counts    identity.UserCounts
	countErr  error
	countedAt []time.Time
}

func (f *fakeUsers) CountUsers(_ context.Context, now time.Time) (identity.UserCounts, error) {
	f.countedAt = append(f.countedAt, now)
	if f.countErr != nil {
		return identity.UserCounts{}, f.countErr
	}
	return f.counts, nil
}

func (f *fakeUsers) LookupSupportUser(_ context.Context, reference string) (identity.SupportUser, error) {
	f.references = append(f.references, reference)
	if f.err != nil {
		return identity.SupportUser{}, f.err
	}
	if f.user.PlatformUserID != "" {
		return f.user, nil
	}
	return identity.SupportUser{
		PlatformUserID: reference,
		AppID:          "a",
		SupportCode:    "A-TESTC0DE",
		IsAnonymous:    true,
		CreatedAt:      time.Unix(1, 0).UTC(),
		LastSeenAt:     time.Unix(2, 0).UTC(),
	}, nil
}

type fakeApps struct {
	app registry.App
	err error
	// list는 환경 대조용이다. 비어 있으면 app 하나만 있는 것으로 본다.
	list    []registry.App
	listErr error
}

func (f *fakeApps) List(context.Context) ([]registry.App, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.list != nil {
		return f.list, nil
	}
	if f.app.AppID != "" {
		return []registry.App{f.app}, nil
	}
	return nil, nil
}

func (f *fakeApps) Get(_ context.Context, appID string) (registry.App, error) {
	if f.err != nil {
		return registry.App{}, f.err
	}
	if f.app.AppID != "" {
		return f.app, nil
	}
	return registry.App{
		AppID:    appID,
		Status:   registry.StatusActive,
		Features: map[string]bool{"iap": true},
		IAP: registry.IAPConfig{
			LedgerEnvironment:     registry.LedgerSandbox,
			Markets:               []string{"google_play"},
			GooglePlayPackageName: "com.seorilabs.lizardtycoon",
			EntitlementIDs:        []string{"sp_a"},
		},
	}, nil
}

type fakeCatalog struct{ allowed bool }

func (f *fakeCatalog) Has(string) bool { return f.allowed }
func (f *fakeCatalog) IDs() []string   { return []string{"sp_a", "sp_b"} }

func (f *fakeConfig) SetMaintenance(_ context.Context, appID string, minutes int, actor string) error {
	f.calls = append(f.calls, maintenanceCall{appID, minutes, actor})
	return f.err
}

// fakeAuditor는 감사 기록을 모은다.
type fakeAuditor struct {
	records []auditRecord
}

type auditRecord struct {
	action  string
	puid    string
	outcome string
	detail  map[string]any
}

func (f *fakeAuditor) Record(_ context.Context, action, _, puid, outcome string, detail map[string]any) {
	f.records = append(f.records, auditRecord{action, puid, outcome, detail})
}

func newHandler(t *testing.T, l *fakeLedger, v *fakeValidator, a *fakeAuditor, allowed ...string) *Handler {
	t.Helper()

	if len(allowed) == 0 {
		allowed = []string{backofficeSA}
	}
	auth, err := NewAuthenticator(v, []string{backofficeReadSA}, allowed)
	if err != nil {
		t.Fatalf("인증기 생성 실패: %v", err)
	}
	h, err := NewHandler(
		l, &fakeConfig{}, &fakeUsers{}, &fakeApps{}, &fakeCatalog{allowed: true}, auth, a,
	)
	if err != nil {
		t.Fatalf("핸들러 생성 실패: %v", err)
	}
	return h
}

func grantBody(requestID, puid, entitlementID, reason string) string {
	return fmt.Sprintf(
		`{"requestId":%q,"platformUserId":%q,"entitlementId":%q,"reason":%q,`+
			`"appId":"a","expectedEnvironment":"sandbox","confirmation":%q}`,
		requestID, puid, entitlementID, reason,
		fmt.Sprintf("GRANT a %s %s", puid, entitlementID),
	)
}

func revokeBody(requestID, grantRequestID, puid, entitlementID, reason string) string {
	return fmt.Sprintf(
		`{"requestId":%q,"grantRequestId":%q,"platformUserId":%q,"entitlementId":%q,"reason":%q,`+
			`"appId":"a","expectedEnvironment":"sandbox","confirmation":%q}`,
		requestID, grantRequestID, puid, entitlementID, reason,
		fmt.Sprintf("REVOKE a %s %s %s", puid, entitlementID, grantRequestID),
	)
}

func sandboxResetBody(requestID, puid, reason string, appleConfirmed bool) string {
	return fmt.Sprintf(
		`{"requestId":%q,"platformUserId":%q,"reason":%q,"appId":"a",`+
			`"expectedEnvironment":"sandbox","confirmation":%q,"appleClearedConfirmed":%t}`,
		requestID, puid, reason, fmt.Sprintf("RESET a %s", puid), appleConfirmed,
	)
}

func sandboxResetResumeBody(requestID, appID string) string {
	return fmt.Sprintf(`{"appId":%q,"confirmation":%q}`,
		appID, fmt.Sprintf("RESUME RESET %s %s", appID, requestID))
}

func sandboxResetCloseBody(requestID, appID string) string {
	return fmt.Sprintf(
		`{"appId":%q,"confirmation":%q}`,
		appID, fmt.Sprintf("CLOSE RESET %s %s", appID, requestID),
	)
}

func refundDecisionBody(requestID, appID, reviewID, preference, reason string, sample bool) string {
	return fmt.Sprintf(
		`{"requestId":%q,"expectedEnvironment":"sandbox","refundPreference":%q,`+
			`"sampleContentProvided":%t,"reason":%q,"confirmation":%q}`,
		requestID, preference, sample, reason,
		fmt.Sprintf("RESPOND REFUND %s %s %s", appID, reviewID, preference),
	)
}

func serve(t *testing.T, h *Handler, method, path, body, token, actor string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	h.Register(mux)

	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if actor != "" {
		r.Header.Set("X-Seori-Actor", actor)
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) (bool, map[string]any, string) {
	t.Helper()

	var env struct {
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("응답 해석 실패: %v (body=%s)", err, w.Body.String())
	}
	return env.OK, env.Result, env.Error.Code
}

func assertExactJSONKeys(t *testing.T, got map[string]any, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("JSON keys = %v, want %v", got, want)
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON key %q가 없다: %v", key, got)
		}
	}
}

// 인증 없는 Admin 경로를 하나라도 열면 원장 전체가 노출된다.
func TestAllRoutesRequireAuth(t *testing.T) {
	h := newHandler(t, &fakeLedger{}, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/orders/recent", ""},
		{http.MethodGet, "/v1/admin/users/A-TESTC0DE", ""},
		{http.MethodGet, "/v1/admin/users/" + testPUID + "/entitlements", ""},
		{http.MethodGet, "/v1/admin/operator-grants", ""},
		{http.MethodGet, "/v1/admin/apps/a/iap/catalog", ""},
		{http.MethodGet, "/v1/admin/apps/a/iap/refund-reviews", ""},
		{http.MethodGet, "/v1/admin/iap/sandbox-resets/reset-1", ""},
		{http.MethodGet, "/v1/admin/health", ""},
		{http.MethodPost, "/v1/admin/entitlements/grant", `{}`},
		{http.MethodPost, "/v1/admin/entitlements/revoke", `{}`},
		{http.MethodPost, "/v1/admin/iap/sandbox-reset", `{}`},
		{http.MethodPost, "/v1/admin/iap/sandbox-resets/reset-1/resume", `{}`},
		{http.MethodPost, "/v1/admin/iap/sandbox-resets/reset-1/close-not-started", `{}`},
		{http.MethodPost, "/v1/admin/apps/a/iap/refund-reviews/" + strings.Repeat("a", 64) + "/decision", `{}`},
		{http.MethodPost, "/v1/admin/config/maintenance", `{}`},
	}

	for _, rt := range routes {
		t.Run(rt.path, func(t *testing.T) {
			// 토큰 없이
			w := serve(t, h, rt.method, rt.path, rt.body, "", "")
			if w.Code != http.StatusUnauthorized {
				t.Errorf("토큰 없이 status = %d, want 401", w.Code)
			}
		})
	}
}

func TestReadAndWriteAllowlistsAreSeparated(t *testing.T) {
	const (
		readSA  = "backoffice-read@example.iam.gserviceaccount.com"
		writeSA = "backoffice-write@example.iam.gserviceaccount.com"
	)
	newWithPrincipal := func(t *testing.T, email string) *Handler {
		t.Helper()
		auth, err := NewAuthenticator(
			&fakeValidator{email: email}, []string{readSA}, []string{writeSA},
		)
		if err != nil {
			t.Fatal(err)
		}
		h, err := NewHandler(
			&fakeLedger{applied: true}, &fakeConfig{}, &fakeUsers{}, &fakeApps{},
			&fakeCatalog{allowed: true}, auth, &fakeAuditor{},
		)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	t.Run("read 계정은 GET만 가능", func(t *testing.T) {
		h := newWithPrincipal(t, readSA)
		if w := serve(t, h, http.MethodGet, "/v1/admin/health", "", "tok", "reader"); w.Code != http.StatusOK {
			t.Fatalf("GET status = %d", w.Code)
		}
		w := serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant",
			grantBody("r", testPUID, "sp_a", testGrantReason), "tok", "reader")
		if w.Code != http.StatusForbidden {
			t.Errorf("POST status = %d, want 403", w.Code)
		}
		w = serve(t, h, http.MethodPost,
			"/v1/admin/iap/sandbox-resets/reset-1/close-not-started",
			sandboxResetCloseBody("reset-1", "a"), "tok", "reader")
		if w.Code != http.StatusForbidden {
			t.Errorf("closure POST status = %d, want 403", w.Code)
		}
	})

	t.Run("write 계정은 GET과 mutation 가능", func(t *testing.T) {
		h := newWithPrincipal(t, writeSA)
		if w := serve(t, h, http.MethodGet, "/v1/admin/health", "", "tok", "writer"); w.Code != http.StatusOK {
			t.Fatalf("GET status = %d", w.Code)
		}
		w := serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant",
			grantBody("r", testPUID, "sp_a", testGrantReason), "tok", "writer")
		if w.Code != http.StatusOK {
			t.Errorf("POST status = %d, body=%s", w.Code, w.Body.String())
		}
	})
}

func TestRejectsInvalidToken(t *testing.T) {
	v := &fakeValidator{err: errors.New("invalid token")}
	h := newHandler(t, &fakeLedger{}, v, &fakeAuditor{})

	w := serve(t, h, http.MethodGet, "/v1/admin/health", "", "bad-token", "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// 허용 목록 밖의 서비스 계정은 신원이 확인돼도 거부한다.
func TestRejectsUnallowedServiceAccount(t *testing.T) {
	v := &fakeValidator{email: "someone-else@evil.example"}
	l := &fakeLedger{}
	h := newHandler(t, l, v, &fakeAuditor{}, backofficeSA)

	w := serve(t, h, http.MethodGet, "/v1/admin/orders/recent", "", "tok", "")

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	_, _, code := decodeEnvelope(t, w)
	if code != string(platformerr.CodeAuthForbidden) {
		t.Errorf("code = %q, want auth_forbidden", code)
	}
}

func TestAllowedServiceAccountPasses(t *testing.T) {
	v := &fakeValidator{email: backofficeSA}
	h := newHandler(t, &fakeLedger{deadLetters: 3}, v, &fakeAuditor{}, backofficeSA)

	w := serve(t, h, http.MethodGet, "/v1/admin/health", "", "tok", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	ok, result, _ := decodeEnvelope(t, w)
	if !ok {
		t.Fatal("성공을 기대했다")
	}
	if result["deadLetterCount"] != float64(3) {
		t.Errorf("deadLetterCount = %v", result["deadLetterCount"])
	}
	if result["environment"] != "sandbox" {
		t.Errorf("environment = %v", result["environment"])
	}
}

func TestRecentOrders(t *testing.T) {
	l := &fakeLedger{orders: []ledger.OrderSummary{
		{
			OrderKey: strings.Repeat("a", 64), PlatformUserID: testPUID,
			EntitlementID: "sp_a", Platform: "google_play", ProductID: "sku_a", State: "active",
		},
	}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodGet, "/v1/admin/orders/recent?limit=10", "", "tok", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	_, result, _ := decodeEnvelope(t, w)
	orders, _ := result["orders"].([]any)
	if len(orders) != 1 {
		t.Errorf("주문 %d건", len(orders))
	}
	if order, _ := orders[0].(map[string]any); order["appId"] != "a" {
		t.Errorf("appId = %v, want identity binding의 a", order["appId"])
	} else {
		assertExactJSONKeys(t, order,
			"orderKey", "appId", "platformUserId", "entitlementId", "platform",
			"productId", "state", "purchasedAt", "observedAt", "tombstone")
	}
}

func TestRecentOrdersRejectsUnsafeOwnerBinding(t *testing.T) {
	tests := []struct {
		name  string
		order ledger.OrderSummary
	}{
		{
			name: "PUID 자리에 PII가 들어간 tombstone",
			order: ledger.OrderSummary{
				OrderKey: strings.Repeat("b", 64), PlatformUserID: "person@example.com",
				Platform: "app_store", State: "revoked", Tombstone: true,
			},
		},
		{
			name: "일반 주문인데 owner가 없음",
			order: ledger.OrderSummary{
				OrderKey: strings.Repeat("c", 64), EntitlementID: "sp_a",
				Platform: "app_store", ProductID: "sku_a", State: "active",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(t, &fakeLedger{orders: []ledger.OrderSummary{tt.order}},
				&fakeValidator{email: backofficeSA}, &fakeAuditor{})
			w := serve(t, h, http.MethodGet, "/v1/admin/orders/recent", "", "tok", "reader")
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "person@example.com") {
				t.Fatalf("잘못된 원장 값이 응답에 노출됐다: %s", w.Body.String())
			}
		})
	}
}

func TestRecentOrdersAllowsOwnerlessTombstone(t *testing.T) {
	h := newHandler(t, &fakeLedger{orders: []ledger.OrderSummary{{
		OrderKey: strings.Repeat("d", 64), Platform: "app_store", State: "revoked", Tombstone: true,
	}}}, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
	w := serve(t, h, http.MethodGet, "/v1/admin/orders/recent", "", "tok", "reader")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	orders, _ := result["orders"].([]any)
	order, _ := orders[0].(map[string]any)
	if order["platformUserId"] != "" || order["appId"] != "" {
		t.Errorf("ownerless tombstone = %v", order)
	}
}

func TestUserEntitlements(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	l := &fakeLedger{entitlements: []ledger.UserEntitlement{
		{EntitlementID: "sp_a", Active: true, UpdatedAt: now, Sources: []ledger.EntitlementSource{{
			Platform: "operator", ProductID: "sp_a", State: "active",
			OrderKey: strings.Repeat("a", 64), Observed: now,
		}}},
		// 비활성도 준다. 왜 없는지를 봐야 CS가 가능하다
		{EntitlementID: "sp_b", Active: false, UpdatedAt: now},
	}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodGet, "/v1/admin/users/"+testPUID+"/entitlements", "", "tok", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	_, result, _ := decodeEnvelope(t, w)
	if result["platformUserId"] != testPUID {
		t.Errorf("puid = %v", result["platformUserId"])
	}
	list, _ := result["entitlements"].([]any)
	if len(list) != 2 {
		t.Errorf("entitlement %d건, want 2", len(list))
	}
	first, _ := list[0].(map[string]any)
	assertExactJSONKeys(t, first, "entitlementId", "active", "updatedAt", "sources")
	sources, _ := first["sources"].([]any)
	source, _ := sources[0].(map[string]any)
	assertExactJSONKeys(t, source, "platform", "productId", "state", "orderKey", "observedAt")
}

func TestUserEntitlementsRejectsUnsafeLedgerValues(t *testing.T) {
	l := &fakeLedger{entitlements: []ledger.UserEntitlement{{
		EntitlementID: "sp_a",
		UpdatedAt:     time.Unix(1, 0).UTC(),
		Sources: []ledger.EntitlementSource{{
			Platform: "app_store", ProductID: "person@example.com", State: "active",
			OrderKey: strings.Repeat("a", 64), Observed: time.Unix(1, 0).UTC(),
		}},
	}}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
	w := serve(t, h, http.MethodGet, "/v1/admin/users/"+testPUID+"/entitlements", "", "tok", "reader")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "person@example.com") {
		t.Fatalf("unsafe source가 응답에 노출됐다: %s", w.Body.String())
	}
}

func TestIAPCatalogReturnsOnlyEntitlementIDs(t *testing.T) {
	h := newHandler(t, &fakeLedger{}, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
	w := serve(t, h, http.MethodGet, "/v1/admin/apps/a/iap/catalog", "", "tok", "reader")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	ids, _ := result["entitlements"].([]any)
	if len(ids) != 1 || ids[0] != "sp_a" {
		t.Errorf("entitlements = %v", ids)
	}
	if result["appId"] != "a" {
		t.Errorf("appId = %v", result["appId"])
	}
	if strings.Contains(w.Body.String(), "google_play") || strings.Contains(w.Body.String(), "app_store") {
		t.Errorf("SKU 정보가 노출됐다: %s", w.Body.String())
	}
}

func TestRefundReviewsExposeOnlySafeProjection(t *testing.T) {
	reviewID := strings.Repeat("a", 64)
	now := time.Now().UTC().Truncate(time.Second)
	sample := false
	l := &fakeLedger{refundReviews: []ledger.RefundReviewSummary{{
		ReviewID: reviewID, AppID: "a", Environment: "sandbox",
		State: ledger.RefundReviewDecided, RefundReason: 7,
		ReceivedAt: now, DueAt: now.Add(24 * time.Hour), RequestID: "request-1",
		RefundPreference:      ledger.RefundPreferenceNeutral,
		SampleContentProvided: &sample, DecisionReason: ledger.RefundReasonInsufficientEvidence,
		DecidedAt: now.Add(time.Minute),
	}}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
	w := serve(t, h, http.MethodGet, "/v1/admin/apps/a/iap/refund-reviews", "", "tok", "reader")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	for _, forbidden := range []string{"orderId", "pendingRefundToken", "ciphertext", "platformUserId"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("금지 필드 %q가 노출됐다: %s", forbidden, w.Body.String())
		}
	}
	_, result, _ := decodeEnvelope(t, w)
	items, _ := result["refundReviews"].([]any)
	item, _ := items[0].(map[string]any)
	if item["reviewId"] != reviewID || item["sampleContentProvided"] != false {
		t.Fatalf("review=%v", item)
	}
}

func TestRefundReviewDecisionIsQueuedWithTargetBinding(t *testing.T) {
	reviewID := strings.Repeat("b", 64)
	l := &fakeLedger{}
	audit := &fakeAuditor{}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, audit)
	body := refundDecisionBody("11111111-1111-4111-8111-111111111111", "a", reviewID,
		ledger.RefundPreferenceDecline, ledger.RefundReasonVerifiedFulfillment, true)
	w := serve(t, h, http.MethodPost,
		"/v1/admin/apps/a/iap/refund-reviews/"+reviewID+"/decision",
		body, "tok", "operator")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(l.refundReplayCalls) != 1 || len(l.refundDecisionCalls) != 1 || len(l.rateCalls) != 1 {
		t.Fatalf("replay=%d decide=%d rate=%d",
			len(l.refundReplayCalls), len(l.refundDecisionCalls), len(l.rateCalls))
	}
	in := l.refundDecisionCalls[0]
	if in.ReviewID != reviewID || in.AppID != "a" || in.ActorLogin != "operator" ||
		in.RefundPreference != ledger.RefundPreferenceDecline || !in.SampleContentProvided {
		t.Fatalf("input=%#v", in)
	}
	_, result, _ := decodeEnvelope(t, w)
	assertExactJSONKeys(t, result, "applied", "requestId", "reviewId", "appId",
		"expectedEnvironment", "state", "refundPreference", "sampleContentProvided", "operation")
}

func TestRefundReviewDecisionRequiresExplicitSampleContentValue(t *testing.T) {
	reviewID := strings.Repeat("c", 64)
	h := newHandler(t, &fakeLedger{}, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
	body := fmt.Sprintf(
		`{"requestId":"22222222-2222-4222-8222-222222222222","expectedEnvironment":"sandbox",`+
			`"refundPreference":"NEUTRAL","reason":"insufficient_evidence",`+
			`"confirmation":"RESPOND REFUND a %s NEUTRAL"}`, reviewID)
	w := serve(t, h, http.MethodPost,
		"/v1/admin/apps/a/iap/refund-reviews/"+reviewID+"/decision",
		body, "tok", "operator")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRefundReviewExactReplaySkipsMutableRateGate(t *testing.T) {
	reviewID := strings.Repeat("d", 64)
	l := &fakeLedger{
		refundReplayFound: true,
		refundDecision: ledger.RefundReviewDecisionResult{
			Applied: false, RequestID: "33333333-3333-4333-8333-333333333333", ReviewID: reviewID, AppID: "a",
			ExpectedEnvironment: "sandbox", State: ledger.RefundReviewResponded,
			RefundPreference: ledger.RefundPreferenceApprove, SampleContentProvided: false,
		},
		rateErr: platformerr.New(platformerr.CodeRateLimited, "limit"),
	}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
	w := serve(t, h, http.MethodPost,
		"/v1/admin/apps/a/iap/refund-reviews/"+reviewID+"/decision",
		refundDecisionBody("33333333-3333-4333-8333-333333333333", "a", reviewID,
			ledger.RefundPreferenceApprove, ledger.RefundReasonCustomerRefundSupported, false),
		"tok", "operator")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(l.rateCalls) != 0 || len(l.refundDecisionCalls) != 0 {
		t.Fatalf("exact retry가 mutable 경로를 탔다: rate=%v decide=%v", l.rateCalls, l.refundDecisionCalls)
	}
}

func TestRefundReviewExactReplayReturnsTerminalDeliveryFailure(t *testing.T) {
	reviewID := strings.Repeat("9", 64)
	l := &fakeLedger{
		refundReplayFound: true,
		refundDecision: ledger.RefundReviewDecisionResult{
			Applied: false, RequestID: "44444444-4444-4444-8444-444444444444", ReviewID: reviewID, AppID: "a",
			ExpectedEnvironment: "sandbox", State: ledger.RefundReviewFailed,
			RefundPreference: ledger.RefundPreferenceNeutral, SampleContentProvided: false,
		},
	}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
	w := serve(t, h, http.MethodPost,
		"/v1/admin/apps/a/iap/refund-reviews/"+reviewID+"/decision",
		refundDecisionBody("44444444-4444-4444-8444-444444444444", "a", reviewID,
			ledger.RefundPreferenceNeutral, ledger.RefundReasonInsufficientEvidence, false),
		"tok", "operator")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	if result["state"] != ledger.RefundReviewFailed || result["applied"] != false {
		t.Fatalf("result=%v", result)
	}
}

func TestRefundReviewRejectsMismatchedLedgerResponse(t *testing.T) {
	reviewID := strings.Repeat("e", 64)
	l := &fakeLedger{refundDecision: ledger.RefundReviewDecisionResult{
		Applied: true, RequestID: "55555555-5555-4555-8555-555555555555", ReviewID: strings.Repeat("f", 64),
		AppID: "a", ExpectedEnvironment: "sandbox", State: ledger.RefundReviewDecided,
		RefundPreference: ledger.RefundPreferenceDecline, SampleContentProvided: true,
	}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
	w := serve(t, h, http.MethodPost,
		"/v1/admin/apps/a/iap/refund-reviews/"+reviewID+"/decision",
		refundDecisionBody("55555555-5555-4555-8555-555555555555", "a", reviewID,
			ledger.RefundPreferenceDecline, ledger.RefundReasonVerifiedFulfillment, true),
		"tok", "operator")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestIAPCatalogFailsClosedAcrossAppAndGlobalCatalog(t *testing.T) {
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		apps       *fakeApps
		catalog    *fakeCatalog
		wantStatus int
	}{
		{
			name: "IAP 활성 앱의 빈 allowlist",
			apps: &fakeApps{app: registry.App{
				AppID: "a", Status: registry.StatusActive, Features: map[string]bool{"iap": true},
				IAP: registry.IAPConfig{LedgerEnvironment: registry.LedgerSandbox},
			}},
			catalog: &fakeCatalog{allowed: true}, wantStatus: http.StatusForbidden,
		},
		{
			name: "앱 allowlist와 전역 카탈로그 불일치",
			apps: &fakeApps{app: registry.App{
				AppID: "a", Status: registry.StatusActive, Features: map[string]bool{"iap": true},
				IAP: registry.IAPConfig{
					LedgerEnvironment: registry.LedgerSandbox,
					EntitlementIDs:    []string{"sp_a"},
				},
			}},
			catalog: &fakeCatalog{allowed: false}, wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := NewHandler(
				&fakeLedger{}, &fakeConfig{}, &fakeUsers{}, tt.apps, tt.catalog, auth, &fakeAuditor{},
			)
			if err != nil {
				t.Fatal(err)
			}
			w := serve(t, h, http.MethodGet, "/v1/admin/apps/a/iap/catalog", "", "tok", "reader")
			if w.Code != tt.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}

func TestUserLookupExposesOnlySupportFields(t *testing.T) {
	user := identity.SupportUser{
		PlatformUserID: testPUID,
		AppID:          "a",
		SupportCode:    "A-TESTC0DE",
		IsAnonymous:    true,
		CreatedAt:      time.Unix(1, 0).UTC(),
		LastSeenAt:     time.Unix(2, 0).UTC(),
	}
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(
		&fakeLedger{}, &fakeConfig{}, &fakeUsers{user: user}, &fakeApps{},
		&fakeCatalog{allowed: true}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatal(err)
	}

	w := serve(t, h, http.MethodGet, "/v1/admin/users/A-TESTC0DE", "", "tok", "reader")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "appUserId") || strings.Contains(w.Body.String(), "firebase") {
		t.Fatalf("PII 성격의 앱 사용자 식별자가 노출됐다: %s", w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	got, _ := result["user"].(map[string]any)
	if got["platformUserId"] != user.PlatformUserID || got["supportCode"] != user.SupportCode {
		t.Errorf("user = %v", got)
	}
	assertExactJSONKeys(t, got,
		"platformUserId", "appId", "supportCode", "isAnonymous", "createdAt", "lastSeenAt")
}

func TestUserLookupRejectsUnsafeStoredSupportFields(t *testing.T) {
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(
		&fakeLedger{}, &fakeConfig{}, &fakeUsers{user: identity.SupportUser{
			PlatformUserID: testPUID,
			AppID:          "a",
			SupportCode:    "person@example.com",
		}}, &fakeApps{}, &fakeCatalog{allowed: true}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatal(err)
	}
	w := serve(t, h, http.MethodGet, "/v1/admin/users/A-TESTC0DE", "", "tok", "reader")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "person@example.com") {
		t.Fatalf("잘못된 지원 문서 값이 응답에 노출됐다: %s", w.Body.String())
	}
}

func TestUserLookupRejectsPIIReferenceBeforeRepository(t *testing.T) {
	users := &fakeUsers{}
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(
		&fakeLedger{}, &fakeConfig{}, users, &fakeApps{},
		&fakeCatalog{allowed: true}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatal(err)
	}
	w := serve(t, h, http.MethodGet, "/v1/admin/users/person@example.com", "", "tok", "reader")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(users.references) != 0 {
		t.Fatalf("PII reference가 repository에 도달했다: %v", users.references)
	}
}

func TestOperatorHistoryUsesExplicitResponseDTO(t *testing.T) {
	l := &fakeLedger{
		grants: []ledger.OperatorRecord{{
			RequestID: "grant-1", PlatformUserID: testPUID, EntitlementID: "sp_a",
			ActorLogin: "ih", Reason: testGrantReason, AppID: "a", Kind: "grant",
		}},
		revocations: []ledger.OperatorRecord{{
			RequestID: "revoke-1", GrantRequestID: "grant-1", PlatformUserID: testPUID,
			EntitlementID: "sp_a", ActorLogin: "ih", Reason: testRevokeReason,
			AppID: "a", Kind: "revoke",
		}},
	}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
	w := serve(t, h, http.MethodGet, "/v1/admin/operator-grants", "", "tok", "reader")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	grants, _ := result["grants"].([]any)
	grant, _ := grants[0].(map[string]any)
	assertExactJSONKeys(t, grant,
		"requestId", "platformUserId", "entitlementId", "actorLogin", "reason",
		"appId", "createdAt", "kind")
	revocations, _ := result["revocations"].([]any)
	revoke, _ := revocations[0].(map[string]any)
	assertExactJSONKeys(t, revoke,
		"requestId", "grantRequestId", "platformUserId", "entitlementId", "actorLogin",
		"reason", "appId", "createdAt", "kind")
}

// 지급은 requestId와 reason이 있어야 한다.
func TestGrantRequiresRequestIdAndReason(t *testing.T) {
	// 핸들러와 원장이 각각 검증한다. 여기서는 검증된 값이 원장까지
	// 그대로 도달하는지 본다.
	l := &fakeLedger{applied: true}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	body := grantBody("req-1", testPUID, "sp_a", testGrantReason)
	w := serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant", body, "tok", "syous")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(l.grantCalls) != 1 {
		t.Fatalf("지급 호출 %d회", len(l.grantCalls))
	}

	got := l.grantCalls[0]
	if got.RequestID != "req-1" {
		t.Errorf("requestId = %q", got.RequestID)
	}
	if got.Reason != testGrantReason {
		t.Errorf("reason = %q", got.Reason)
	}
	// 누가 눌렀는지가 원장에 남아야 한다
	if got.ActorLogin != "syous" {
		t.Errorf("actorLogin = %q, want 헤더 값", got.ActorLogin)
	}
	if len(l.rateCalls) != 1 || l.rateCalls[0] != backofficeSA {
		t.Errorf("rate gate principal = %v, want 검증된 OIDC 계정", l.rateCalls)
	}
	_, result, _ := decodeEnvelope(t, w)
	assertExactJSONKeys(t, result,
		"applied", "entitlements", "requestId", "appId", "platformUserId",
		"entitlementId", "expectedEnvironment", "operation")
	if result["requestId"] != "req-1" || result["appId"] != "a" ||
		result["platformUserId"] != testPUID || result["entitlementId"] != "sp_a" ||
		result["expectedEnvironment"] != "sandbox" || result["operation"] != "grant" {
		t.Errorf("조작 응답 target echo = %v", result)
	}
}

func TestIAPMutationValidationFailsClosed(t *testing.T) {
	allowed := []string{backofficeSA}
	auth, err := NewAuthenticator(&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, allowed)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		body       string
		users      *fakeUsers
		apps       *fakeApps
		catalog    *fakeCatalog
		ledger     *fakeLedger
		wantStatus int
	}{
		{
			name: "원장 환경 불일치",
			body: strings.Replace(grantBody("r", testPUID, "sp_a", testGrantReason),
				`"expectedEnvironment":"sandbox"`, `"expectedEnvironment":"production"`, 1),
			users: &fakeUsers{}, apps: &fakeApps{}, catalog: &fakeCatalog{allowed: true},
			ledger: &fakeLedger{}, wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "typed confirmation 불일치",
			body: strings.Replace(grantBody("r", testPUID, "sp_a", testGrantReason),
				fmt.Sprintf("GRANT a %s sp_a", testPUID), "GRANT wrong", 1),
			users: &fakeUsers{}, apps: &fakeApps{}, catalog: &fakeCatalog{allowed: true},
			ledger: &fakeLedger{}, wantStatus: http.StatusBadRequest,
		},
		{
			name: "자유 서술 reason",
			body: strings.Replace(grantBody("r", testPUID, "sp_a", testGrantReason),
				testGrantReason, "person@example.com 영수증 첨부", 1),
			users: &fakeUsers{}, apps: &fakeApps{}, catalog: &fakeCatalog{allowed: true},
			ledger: &fakeLedger{}, wantStatus: http.StatusBadRequest,
		},
		{
			name: "선두 하이픈 appId",
			body: strings.NewReplacer(
				`"appId":"a"`, `"appId":"-a"`,
				"GRANT a ", "GRANT -a ",
			).Replace(grantBody("r", testPUID, "sp_a", testGrantReason)),
			users: &fakeUsers{}, apps: &fakeApps{}, catalog: &fakeCatalog{allowed: true},
			ledger: &fakeLedger{}, wantStatus: http.StatusBadRequest,
		},
		{
			name:  "앱 IAP 비활성",
			body:  grantBody("r", testPUID, "sp_a", testGrantReason),
			users: &fakeUsers{},
			apps: &fakeApps{app: registry.App{
				AppID: "a", Status: registry.StatusActive, Features: map[string]bool{"iap": false},
				IAP: registry.IAPConfig{LedgerEnvironment: registry.LedgerSandbox},
			}},
			catalog: &fakeCatalog{allowed: true}, ledger: &fakeLedger{},
			wantStatus: http.StatusForbidden,
		},
		{
			name:  "다른 앱 사용자",
			body:  grantBody("r", testPUID, "sp_a", testGrantReason),
			users: &fakeUsers{user: identity.SupportUser{PlatformUserID: testPUID, AppID: "other"}},
			apps:  &fakeApps{}, catalog: &fakeCatalog{allowed: true}, ledger: &fakeLedger{},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "앱 allowlist 외 entitlement",
			body: strings.Replace(grantBody("r", testPUID, "sp_a", testGrantReason),
				"sp_a", "sp_b", 2),
			users: &fakeUsers{}, apps: &fakeApps{}, catalog: &fakeCatalog{allowed: true},
			ledger: &fakeLedger{}, wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:  "앱 allowlist와 전역 카탈로그 불일치",
			body:  grantBody("r", testPUID, "sp_a", testGrantReason),
			users: &fakeUsers{}, apps: &fakeApps{}, catalog: &fakeCatalog{allowed: false},
			ledger: &fakeLedger{}, wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:  "durable rate gate 초과",
			body:  grantBody("r", testPUID, "sp_a", testGrantReason),
			users: &fakeUsers{}, apps: &fakeApps{}, catalog: &fakeCatalog{allowed: true},
			ledger:     &fakeLedger{rateErr: platformerr.New(platformerr.CodeRateLimited, "limit")},
			wantStatus: http.StatusTooManyRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := NewHandler(
				tt.ledger, &fakeConfig{}, tt.users, tt.apps, tt.catalog, auth, &fakeAuditor{},
			)
			if err != nil {
				t.Fatal(err)
			}
			w := serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant", tt.body, "tok", "syous")
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d, body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
			if len(tt.ledger.grantCalls) != 0 {
				t.Error("검증 실패 요청이 원장 mutation에 도달했다")
			}
		})
	}
}

// 신규 조작은 앱과 사용자 binding을 확인하므로 삭제되었거나 존재하지 않는
// 사용자는 세 operation 모두 동일한 user_not_found 계약으로 거부한다.
func TestIAPMutationsReturnUserNotFound(t *testing.T) {
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		body string
	}{
		{
			name: "운영자 지급",
			path: "/v1/admin/entitlements/grant",
			body: grantBody("grant-missing-user", testPUID, "sp_a", testGrantReason),
		},
		{
			name: "운영자 회수",
			path: "/v1/admin/entitlements/revoke",
			body: revokeBody("revoke-missing-user", "grant-1", testPUID, "sp_a", testRevokeReason),
		},
		{
			name: "sandbox 초기화",
			path: "/v1/admin/iap/sandbox-reset",
			body: sandboxResetBody("reset-missing-user", testPUID, testResetReason, true),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &fakeLedger{}
			h, err := NewHandler(
				l,
				&fakeConfig{},
				&fakeUsers{err: platformerr.New(platformerr.CodeUserNotFound, "deleted")},
				&fakeApps{},
				&fakeCatalog{allowed: true},
				auth,
				&fakeAuditor{},
			)
			if err != nil {
				t.Fatal(err)
			}

			w := serve(t, h, http.MethodPost, tt.path, tt.body, "tok", "syous")
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
			}
			ok, _, code := decodeEnvelope(t, w)
			if ok || code != string(platformerr.CodeUserNotFound) {
				t.Errorf("ok=%v code=%q, want false/%q", ok, code, platformerr.CodeUserNotFound)
			}
			if len(l.grantCalls) != 0 || len(l.revokeCalls) != 0 || len(l.resetCalls) != 0 || len(l.rateCalls) != 0 {
				t.Errorf("user_not_found 뒤 mutation/rate gate가 호출됐다: grant=%d revoke=%d reset=%d rate=%d",
					len(l.grantCalls), len(l.revokeCalls), len(l.resetCalls), len(l.rateCalls))
			}
		})
	}
}

// GitHub login이 없거나 형식이 틀리면 OIDC email 원문 대신 전체 sha256을 남긴다.
func TestActorFallsBackToHashedOIDCPrincipal(t *testing.T) {
	want := fmt.Sprintf("oidc_sha256:%x", sha256.Sum256([]byte(backofficeSA)))
	for _, header := range []string{"", "person@example.com"} {
		t.Run(header, func(t *testing.T) {
			l := &fakeLedger{applied: true}
			h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

			body := grantBody("r", testPUID, "sp_a", testGrantReason)
			serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant", body, "tok", header)

			if got := l.grantCalls[0].ActorLogin; got != want || strings.Contains(got, "@") {
				t.Errorf("actorLogin = %q, want %q", got, want)
			}
		})
	}
}

func TestActorLoginFailsClosedWithoutVerifiedPrincipal(t *testing.T) {
	if got := actorLogin(Actor{}); got != "" {
		t.Errorf("actorLogin = %q, want empty fail-closed value", got)
	}
}

func TestExactOperatorReplayBypassesMutablePreconditionsAndRate(t *testing.T) {
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatal(err)
	}
	l := &fakeLedger{
		replay:      ledger.OperatorResult{Applied: false, Entitlements: []string{"sp_a"}},
		replayFound: true,
		rateErr:     platformerr.New(platformerr.CodeRateLimited, "limit"),
	}
	h, err := NewHandler(
		l, &fakeConfig{},
		&fakeUsers{err: platformerr.New(platformerr.CodeUserNotFound, "deleted")},
		&fakeApps{err: platformerr.New(platformerr.CodeAppPaused, "paused")},
		&fakeCatalog{allowed: false}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatal(err)
	}
	w := serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant",
		grantBody("same-request", testPUID, "sp_a", testGrantReason), "tok", "syous")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	if result["applied"] != false {
		t.Errorf("applied=%v", result["applied"])
	}
	if len(l.replayCalls) != 1 || len(l.rateCalls) != 0 || len(l.grantCalls) != 0 {
		t.Fatalf("replay=%d rate=%d grant=%d", len(l.replayCalls), len(l.rateCalls), len(l.grantCalls))
	}
}

func TestExactSandboxReplayBypassesMutablePreconditionsAndRate(t *testing.T) {
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatal(err)
	}
	l := &fakeLedger{
		resetReplayKeys:  []string{"order-key"},
		resetReplayFound: true,
		rateErr:          platformerr.New(platformerr.CodeRateLimited, "limit"),
	}
	h, err := NewHandler(
		l, &fakeConfig{},
		&fakeUsers{err: platformerr.New(platformerr.CodeUserNotFound, "deleted")},
		&fakeApps{err: platformerr.New(platformerr.CodeAppPaused, "paused")},
		&fakeCatalog{allowed: false}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatal(err)
	}
	w := serve(t, h, http.MethodPost, "/v1/admin/iap/sandbox-reset",
		sandboxResetBody("same-reset", testPUID, testResetReason, true), "tok", "syous")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(l.resetReplayCalls) != 1 || len(l.rateCalls) != 0 || len(l.resetCalls) != 0 {
		t.Fatalf("replay=%d rate=%d reset=%d",
			len(l.resetReplayCalls), len(l.rateCalls), len(l.resetCalls))
	}
}

// 불변식 8. 권한을 결정하는 필드를 요청에 주입할 수 없다.
func TestRejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{name: "실행자 위조", field: `"actorLogin":"위조"`},
		{name: "지급 요청에 회수 원본 주입", field: `"grantRequestId":""`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &fakeLedger{}
			h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

			body := strings.TrimSuffix(grantBody("r", testPUID, "sp_a", testGrantReason), "}") + "," + tt.field + "}"
			w := serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant", body, "tok", "")

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			if len(l.grantCalls) != 0 {
				t.Error("주입된 요청이 원장에 도달했다")
			}
		})
	}
}

// 조작은 성패와 무관하게 감사에 남아야 한다.
func TestOperationsAreAudited(t *testing.T) {
	t.Run("성공", func(t *testing.T) {
		a := &fakeAuditor{}
		l := &fakeLedger{applied: true}
		h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

		body := grantBody("r", testPUID, "sp_a", testGrantReason)
		serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant", body, "tok", "syous")

		if len(a.records) != 1 {
			t.Fatalf("감사 기록 %d건", len(a.records))
		}
		rec := a.records[0]
		if rec.action != "iap.operator_grant" {
			t.Errorf("action = %q", rec.action)
		}
		if rec.outcome != "ok" {
			t.Errorf("outcome = %q", rec.outcome)
		}
		if rec.detail["reason"] != testGrantReason {
			t.Errorf("reason이 감사에 없다: %v", rec.detail)
		}
	})

	t.Run("실패도 남는다", func(t *testing.T) {
		a := &fakeAuditor{}
		l := &fakeLedger{err: platformerr.New(platformerr.CodeRequestInvalid, "사유 없음")}
		h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

		body := grantBody("r", testPUID, "sp_a", testGrantReason)
		w := serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant", body, "tok", "syous")

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d", w.Code)
		}
		if len(a.records) != 1 {
			t.Fatalf("실패가 감사에 안 남았다")
		}
		if a.records[0].outcome != string(platformerr.CodeRequestInvalid) {
			t.Errorf("outcome = %q", a.records[0].outcome)
		}
	})

	t.Run("검증 실패 payload는 PII를 감사에 복사하지 않는다", func(t *testing.T) {
		a := &fakeAuditor{}
		l := &fakeLedger{}
		h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)
		body := grantBody("r", "person@example.com", "receipt@example.com", "이름 홍길동")
		serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant", body, "tok", "ih")

		if len(a.records) != 1 {
			t.Fatalf("감사 기록 = %d건", len(a.records))
		}
		encoded := fmt.Sprintf("%s %v", a.records[0].puid, a.records[0].detail)
		if strings.Contains(encoded, "@") || strings.Contains(encoded, "홍길동") {
			t.Errorf("검증 실패 PII가 감사에 복사됐다: %s", encoded)
		}
		if a.records[0].puid != "" || a.records[0].detail["reason"] != nil {
			t.Errorf("검증 전 필드가 감사에 남았다: %+v", a.records[0])
		}
	})

	// 이미 처리된 요청은 실패가 아니다. 구분해서 남긴다.
	t.Run("멱등 재요청", func(t *testing.T) {
		a := &fakeAuditor{}
		l := &fakeLedger{applied: false}
		h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

		body := grantBody("r", testPUID, "sp_a", testGrantReason)
		w := serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant", body, "tok", "syous")

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
		_, result, _ := decodeEnvelope(t, w)
		if result["applied"] != false {
			t.Errorf("applied = %v, want false", result["applied"])
		}
		if a.records[0].outcome != "already_applied" {
			t.Errorf("outcome = %q", a.records[0].outcome)
		}
	})
}

func TestRevokeRoutesToRevoke(t *testing.T) {
	l := &fakeLedger{applied: true}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	body := revokeBody("r", "grant-r", testPUID, "sp_a", testRevokeReason)
	w := serve(t, h, http.MethodPost, "/v1/admin/entitlements/revoke", body, "tok", "syous")

	if len(l.revokeCalls) != 1 {
		t.Errorf("회수 호출 %d회", len(l.revokeCalls))
	}
	if len(l.grantCalls) != 0 {
		t.Error("회수 요청이 지급으로 갔다")
	}
	if l.revokeCalls[0].GrantRequestID != "grant-r" {
		t.Errorf("grantRequestId = %q", l.revokeCalls[0].GrantRequestID)
	}
	_, result, _ := decodeEnvelope(t, w)
	if result["operation"] != "revoke" || result["grantRequestId"] != "grant-r" {
		t.Errorf("회수 응답 target echo = %v", result)
	}
}

func TestNewValidation(t *testing.T) {
	allowed := []string{backofficeSA}
	auth, _ := NewAuthenticator(&fakeValidator{}, []string{backofficeReadSA}, allowed)
	users := &fakeUsers{}
	apps := &fakeApps{}
	cat := &fakeCatalog{allowed: true}

	if _, err := NewHandler(nil, &fakeConfig{}, users, apps, cat, auth, nil); err == nil {
		t.Error("원장 없이 통과시켰다")
	}
	if _, err := NewHandler(&fakeLedger{}, &fakeConfig{}, users, apps, cat, nil, nil); err == nil {
		t.Error("인증기 없이 통과시켰다")
	}
	if _, err := NewAuthenticator(nil, []string{backofficeReadSA}, allowed); err == nil {
		t.Error("검증기 없이 통과시켰다")
	}
	if _, err := NewAuthenticator(&fakeValidator{}, nil, allowed); err == nil {
		t.Error("read allowlist 없이 통과시켰다")
	}
	if _, err := NewAuthenticator(&fakeValidator{},
		[]string{"Shared@Example.com"}, []string{"shared@example.com"}); err == nil {
		t.Error("read/write allowlist 교집합을 통과시켰다")
	}
}

// break-glass의 핵심 경로다. 백오피스가 죽어도 이건 돼야 한다.
func TestMaintenanceToggle(t *testing.T) {
	newWithConfig := func(t *testing.T, cfg *fakeConfig, a *fakeAuditor) *Handler {
		t.Helper()
		allowed := []string{backofficeSA}
		auth, err := NewAuthenticator(
			&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, allowed,
		)
		if err != nil {
			t.Fatalf("인증기 생성 실패: %v", err)
		}
		h, err := NewHandler(
			&fakeLedger{}, cfg, &fakeUsers{}, &fakeApps{}, &fakeCatalog{allowed: true}, auth, a,
		)
		if err != nil {
			t.Fatalf("핸들러 생성 실패: %v", err)
		}
		return h
	}

	t.Run("점검 모드를 켠다", func(t *testing.T) {
		cfg := &fakeConfig{}
		a := &fakeAuditor{}
		h := newWithConfig(t, cfg, a)

		body := `{"appId":"lizard-tycoon","minutes":30}`
		w := serve(t, h, http.MethodPost, "/v1/admin/config/maintenance", body, "tok", "syous")

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
		if len(cfg.calls) != 1 {
			t.Fatalf("호출 %d회", len(cfg.calls))
		}
		got := cfg.calls[0]
		if got.appID != "lizard-tycoon" || got.minutes != 30 {
			t.Errorf("호출 = %+v", got)
		}
		// 누가 켰는지 남아야 한다
		if got.actor != "syous" {
			t.Errorf("actor = %q", got.actor)
		}
		if len(a.records) != 1 || a.records[0].outcome != "on" {
			t.Errorf("감사 기록 = %+v", a.records)
		}
	})

	t.Run("0분이면 끈다", func(t *testing.T) {
		cfg := &fakeConfig{}
		a := &fakeAuditor{}
		h := newWithConfig(t, cfg, a)

		body := `{"appId":"lizard-tycoon","minutes":0}`
		w := serve(t, h, http.MethodPost, "/v1/admin/config/maintenance", body, "tok", "syous")

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		_, result, _ := decodeEnvelope(t, w)
		if result["active"] != false {
			t.Errorf("active = %v, want false", result["active"])
		}
		if a.records[0].outcome != "off" {
			t.Errorf("outcome = %q", a.records[0].outcome)
		}
	})

	t.Run("앱 식별자가 없으면 거부", func(t *testing.T) {
		cfg := &fakeConfig{}
		h := newWithConfig(t, cfg, &fakeAuditor{})

		w := serve(t, h, http.MethodPost, "/v1/admin/config/maintenance", `{"minutes":30}`, "tok", "")

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		if len(cfg.calls) != 0 {
			t.Error("앱 없이 점검 모드를 건드렸다")
		}
	})

	t.Run("minutes 필드가 없으면 점검 해제로 해석하지 않고 거부", func(t *testing.T) {
		cfg := &fakeConfig{}
		h := newWithConfig(t, cfg, &fakeAuditor{})

		w := serve(t, h, http.MethodPost, "/v1/admin/config/maintenance",
			`{"appId":"lizard-tycoon"}`, "tok", "syous")

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		if len(cfg.calls) != 0 {
			t.Error("minutes 없는 요청이 점검 모드를 건드렸다")
		}
	})

	t.Run("미등록 형식 앱 식별자를 거부", func(t *testing.T) {
		cfg := &fakeConfig{}
		h := newWithConfig(t, cfg, &fakeAuditor{})
		w := serve(t, h, http.MethodPost, "/v1/admin/config/maintenance",
			`{"appId":"person@example.com","minutes":30}`, "tok", "ih")
		if w.Code != http.StatusBadRequest || len(cfg.calls) != 0 {
			t.Errorf("status=%d calls=%v", w.Code, cfg.calls)
		}
	})

	t.Run("paused 앱과 rate 초과는 config에 도달하지 않는다", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			ledger *fakeLedger
			apps   *fakeApps
			status int
		}{
			{
				name: "paused 앱", ledger: &fakeLedger{},
				apps: &fakeApps{app: registry.App{
					AppID: "lizard-tycoon", DisplayName: "Lizard", Status: registry.StatusPaused,
				}},
				status: http.StatusForbidden,
			},
			{
				name:   "rate 초과",
				ledger: &fakeLedger{rateErr: platformerr.New(platformerr.CodeRateLimited, "limit")},
				apps:   &fakeApps{}, status: http.StatusTooManyRequests,
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				cfg := &fakeConfig{}
				auth, err := NewAuthenticator(
					&fakeValidator{email: backofficeSA}, []string{backofficeReadSA}, []string{backofficeSA},
				)
				if err != nil {
					t.Fatal(err)
				}
				h, err := NewHandler(
					tt.ledger, cfg, &fakeUsers{}, tt.apps, &fakeCatalog{allowed: true}, auth, &fakeAuditor{},
				)
				if err != nil {
					t.Fatal(err)
				}
				w := serve(t, h, http.MethodPost, "/v1/admin/config/maintenance",
					`{"appId":"lizard-tycoon","minutes":30}`, "tok", "ih")
				if w.Code != tt.status || len(cfg.calls) != 0 {
					t.Errorf("status=%d want=%d calls=%v", w.Code, tt.status, cfg.calls)
				}
			})
		}
	})

	// 본문 텍스트를 받지 않는다. 장애 중에 자유 텍스트 입력에
	// 의존하면 안 되고, 문구는 서버가 갖고 있다.
	t.Run("메시지 필드를 거부한다", func(t *testing.T) {
		cfg := &fakeConfig{}
		h := newWithConfig(t, cfg, &fakeAuditor{})

		body := `{"appId":"lizard-tycoon","minutes":30,"message":"임의 문구"}`
		w := serve(t, h, http.MethodPost, "/v1/admin/config/maintenance", body, "tok", "")

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		if len(cfg.calls) != 0 {
			t.Error("임의 문구가 통과했다")
		}
	})
}

func TestSandboxResetStatusReturnsImmutableTarget(t *testing.T) {
	resetAt := time.Date(2026, 8, 2, 3, 4, 5, 0, time.UTC)
	orderKey := strings.Repeat("a", 64)
	l := &fakeLedger{resetStatus: ledger.SandboxResetStatus{
		RequestID:      "reset-1",
		PlatformUserID: testPUID,
		AppID:          "a",
		State:          "completed",
		ResetAt:        resetAt,
		OrderKeys:      []string{orderKey},
	}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodGet,
		"/v1/admin/iap/sandbox-resets/reset-1", "", "tok", "reader")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	assertExactJSONKeys(t, result, "requestId", "appId",
		"expectedEnvironment", "operation", "state")
	if result["requestId"] != "reset-1" || result["appId"] != "a" ||
		result["state"] != "completed" ||
		result["expectedEnvironment"] != "sandbox" || result["operation"] != "sandbox_reset" ||
		result["platformUserId"] != nil || result["resetOrderKeys"] != nil || result["resetAt"] != nil {
		t.Errorf("상태 응답 target binding = %v", result)
	}
	if len(l.resetStatusCalls) != 1 || l.resetStatusCalls[0] != "reset-1" {
		t.Errorf("status calls = %v", l.resetStatusCalls)
	}
}

func TestSandboxResetStatusReturnsPIIFreeClosedState(t *testing.T) {
	l := &fakeLedger{resetStatus: ledger.SandboxResetStatus{
		RequestID: "reset-1",
		AppID:     "a",
		State:     ledger.SandboxResetClosedNotStarted,
	}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodGet,
		"/v1/admin/iap/sandbox-resets/reset-1", "", "tok", "reader")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	assertExactJSONKeys(t, result, "requestId", "appId",
		"expectedEnvironment", "operation", "state")
	if result["requestId"] != "reset-1" || result["appId"] != "a" ||
		result["state"] != string(ledger.SandboxResetClosedNotStarted) ||
		result["platformUserId"] != nil || result["resetOrderKeys"] != nil ||
		result["resetAt"] != nil {
		t.Errorf("미시작 종결 상태 응답 = %v", result)
	}
}

func TestSandboxResetStatusFailsClosedOnInvalidClosedState(t *testing.T) {
	tests := []struct {
		name   string
		status ledger.SandboxResetStatus
	}{
		{
			name: "PUID 포함",
			status: ledger.SandboxResetStatus{
				RequestID: "reset-1", AppID: "a",
				State: ledger.SandboxResetClosedNotStarted, PlatformUserID: testPUID,
			},
		},
		{
			name: "reset 시각 포함",
			status: ledger.SandboxResetStatus{
				RequestID: "reset-1", AppID: "a",
				State: ledger.SandboxResetClosedNotStarted, ResetAt: time.Now().UTC(),
			},
		},
		{
			name: "주문 결과 포함",
			status: ledger.SandboxResetStatus{
				RequestID: "reset-1", AppID: "a",
				State:     ledger.SandboxResetClosedNotStarted,
				OrderKeys: []string{strings.Repeat("a", 64)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &fakeLedger{resetStatus: tt.status}
			h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

			w := serve(t, h, http.MethodGet,
				"/v1/admin/iap/sandbox-resets/reset-1", "", "tok", "reader")
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500. body=%s", w.Code, w.Body.String())
			}
			_, _, code := decodeEnvelope(t, w)
			if code != string(platformerr.CodeLedgerStateInvalid) {
				t.Errorf("code = %q, want %q", code, platformerr.CodeLedgerStateInvalid)
			}
		})
	}
}

func TestSandboxResetStatusFailsClosedOnInvalidBinding(t *testing.T) {
	l := &fakeLedger{resetStatus: ledger.SandboxResetStatus{
		RequestID:      "different-request",
		PlatformUserID: testPUID,
		AppID:          "a",
		State:          "prepared",
		ResetAt:        time.Now().UTC(),
	}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodGet,
		"/v1/admin/iap/sandbox-resets/reset-1", "", "tok", "reader")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500. body=%s", w.Code, w.Body.String())
	}
	_, _, code := decodeEnvelope(t, w)
	if code != string(platformerr.CodeLedgerStateInvalid) {
		t.Errorf("code = %q, want %q", code, platformerr.CodeLedgerStateInvalid)
	}
}

func TestSandboxResetStatusRejectsInvalidRequestIDBeforeLedger(t *testing.T) {
	l := &fakeLedger{}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodGet,
		"/v1/admin/iap/sandbox-resets/%20", "", "tok", "reader")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body=%s", w.Code, w.Body.String())
	}
	if len(l.resetStatusCalls) != 0 {
		t.Errorf("invalid requestId가 ledger에 도달했다: %v", l.resetStatusCalls)
	}
}

func TestSandboxResetStatusReturnsNotFoundForAbsentIntent(t *testing.T) {
	l := &fakeLedger{resetStatusErr: platformerr.New(
		platformerr.CodeSandboxResetNotFound, "absent")}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodGet,
		"/v1/admin/iap/sandbox-resets/reset-1", "", "tok", "reader")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404. body=%s", w.Code, w.Body.String())
	}
	_, _, code := decodeEnvelope(t, w)
	if code != string(platformerr.CodeSandboxResetNotFound) {
		t.Errorf("code = %q, want %q", code, platformerr.CodeSandboxResetNotFound)
	}
}

func TestResumeSandboxResetUsesImmutableIntentTarget(t *testing.T) {
	resetAt := time.Date(2026, 8, 2, 3, 4, 5, 0, time.UTC)
	orderKey := strings.Repeat("b", 64)
	a := &fakeAuditor{}
	l := &fakeLedger{
		resetStatus: ledger.SandboxResetStatus{
			RequestID:      "reset-1",
			PlatformUserID: testPUID,
			AppID:          "a",
			State:          "prepared",
			ResetAt:        resetAt,
		},
		resetResumeKeys: []string{orderKey},
		rateErr:         platformerr.New(platformerr.CodeRateLimited, "limit"),
	}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

	w := serve(t, h, http.MethodPost,
		"/v1/admin/iap/sandbox-resets/reset-1/resume",
		sandboxResetResumeBody("reset-1", "a"), "tok", "syous")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	assertExactJSONKeys(t, result, "requestId", "appId", "platformUserId",
		"expectedEnvironment", "operation", "resetOrderKeys")
	if result["requestId"] != "reset-1" || result["appId"] != "a" ||
		result["platformUserId"] != testPUID {
		t.Errorf("resume 응답 target binding = %v", result)
	}
	if len(l.resetStatusCalls) != 1 || len(l.resetResumeCalls) != 1 ||
		l.resetResumeCalls[0] != "reset-1" || len(l.rateCalls) != 0 {
		t.Errorf("status=%v resume=%v rate=%v", l.resetStatusCalls, l.resetResumeCalls, l.rateCalls)
	}
	if len(a.records) != 1 {
		t.Fatalf("감사 기록 = %d건, want 1", len(a.records))
	}
	rec := a.records[0]
	if rec.action != "iap.sandbox_reset_resume" || rec.puid != testPUID ||
		rec.outcome != "ok" || rec.detail["actor"] != "syous" ||
		rec.detail["request_id"] != "reset-1" {
		t.Errorf("resume 감사 기록 = %+v", rec)
	}
}

func TestResumeSandboxResetRejectsMismatchedAppBeforeMutation(t *testing.T) {
	l := &fakeLedger{resetStatus: ledger.SandboxResetStatus{
		RequestID:      "reset-1",
		PlatformUserID: testPUID,
		AppID:          "a",
		State:          "prepared",
		ResetAt:        time.Now().UTC(),
	}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodPost,
		"/v1/admin/iap/sandbox-resets/reset-1/resume",
		sandboxResetResumeBody("reset-1", "b"), "tok", "syous")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409. body=%s", w.Code, w.Body.String())
	}
	if len(l.resetResumeCalls) != 0 {
		t.Errorf("다른 앱의 resume가 원장 mutation에 도달했다: %v", l.resetResumeCalls)
	}
}

func TestResumeSandboxResetRequiresExactConfirmation(t *testing.T) {
	l := &fakeLedger{}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodPost,
		"/v1/admin/iap/sandbox-resets/reset-1/resume",
		`{"appId":"a","confirmation":"RESUME RESET a wrong"}`, "tok", "syous")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body=%s", w.Code, w.Body.String())
	}
	if len(l.resetStatusCalls) != 0 || len(l.resetResumeCalls) != 0 {
		t.Errorf("confirmation 실패 뒤 ledger가 호출됐다: status=%v resume=%v",
			l.resetStatusCalls, l.resetResumeCalls)
	}
}

func TestResumeSandboxResetRejectsClosedRequest(t *testing.T) {
	l := &fakeLedger{resetStatus: ledger.SandboxResetStatus{
		RequestID: "reset-1",
		AppID:     "a",
		State:     ledger.SandboxResetClosedNotStarted,
	}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodPost,
		"/v1/admin/iap/sandbox-resets/reset-1/resume",
		sandboxResetResumeBody("reset-1", "a"), "tok", "syous")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409. body=%s", w.Code, w.Body.String())
	}
	_, _, code := decodeEnvelope(t, w)
	if code != string(platformerr.CodeSandboxResetClosed) {
		t.Errorf("code = %q, want %q", code, platformerr.CodeSandboxResetClosed)
	}
	if len(l.resetResumeCalls) != 0 {
		t.Errorf("종결된 reset이 resume mutation에 도달했다: %v", l.resetResumeCalls)
	}
}

func TestResumeSandboxResetPendingIsRetryableAndAudited(t *testing.T) {
	a := &fakeAuditor{}
	l := &fakeLedger{
		resetStatus: ledger.SandboxResetStatus{
			RequestID:      "reset-1",
			PlatformUserID: testPUID,
			AppID:          "a",
			State:          "prepared",
			ResetAt:        time.Now().UTC(),
		},
		resetResumeErr: platformerr.New(platformerr.CodeSandboxResetPending, "retry"),
	}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

	w := serve(t, h, http.MethodPost,
		"/v1/admin/iap/sandbox-resets/reset-1/resume",
		sandboxResetResumeBody("reset-1", "a"), "tok", "syous")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503. body=%s", w.Code, w.Body.String())
	}
	_, _, code := decodeEnvelope(t, w)
	if code != string(platformerr.CodeSandboxResetPending) {
		t.Errorf("code = %q, want %q", code, platformerr.CodeSandboxResetPending)
	}
	if len(a.records) != 1 || a.records[0].outcome != string(platformerr.CodeSandboxResetPending) {
		t.Errorf("pending 감사 기록 = %+v", a.records)
	}
}

func TestCloseSandboxResetNotStartedCreatesDurableClosure(t *testing.T) {
	a := &fakeAuditor{}
	l := &fakeLedger{resetCloseApplied: true}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

	w := serve(t, h, http.MethodPost,
		"/v1/admin/iap/sandbox-resets/reset-1/close-not-started",
		sandboxResetCloseBody("reset-1", "a"), "tok", "syous")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	assertExactJSONKeys(t, result, "requestId", "appId", "expectedEnvironment",
		"operation", "state", "applied")
	if result["requestId"] != "reset-1" || result["appId"] != "a" ||
		result["expectedEnvironment"] != "sandbox" || result["operation"] != "sandbox_reset" ||
		result["state"] != string(ledger.SandboxResetClosedNotStarted) || result["applied"] != true {
		t.Errorf("closure 응답 target binding = %v", result)
	}
	if len(l.resetCloseCalls) != 1 {
		t.Fatalf("closure 호출 = %d건, want 1", len(l.resetCloseCalls))
	}
	call := l.resetCloseCalls[0]
	if call.RequestID != "reset-1" || call.AppID != "a" || call.ActorLogin != "syous" {
		t.Errorf("closure 원장 입력 = %+v", call)
	}
	if len(a.records) != 1 {
		t.Fatalf("감사 기록 = %d건, want 1", len(a.records))
	}
	rec := a.records[0]
	if rec.action != "iap.sandbox_reset_close_not_started" || rec.puid != "" ||
		rec.outcome != "ok" || rec.detail["request_id"] != "reset-1" ||
		rec.detail["environment"] != "sandbox" || rec.detail["actor"] != "syous" ||
		rec.detail["applied"] != true {
		t.Errorf("closure 감사 기록 = %+v", rec)
	}
	if _, exists := rec.detail["confirmation"]; exists {
		t.Errorf("typed confirmation이 감사 로그에 남았다: %+v", rec.detail)
	}
}

func TestCloseSandboxResetNotStartedExactReplay(t *testing.T) {
	a := &fakeAuditor{}
	l := &fakeLedger{resetCloseApplied: false}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

	w := serve(t, h, http.MethodPost,
		"/v1/admin/iap/sandbox-resets/reset-1/close-not-started",
		sandboxResetCloseBody("reset-1", "a"), "tok", "syous")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	if result["applied"] != false || result["state"] != string(ledger.SandboxResetClosedNotStarted) {
		t.Errorf("closure replay 응답 = %v", result)
	}
	if len(a.records) != 1 || a.records[0].outcome != "already_closed" ||
		a.records[0].detail["applied"] != false {
		t.Errorf("closure replay 감사 기록 = %+v", a.records)
	}
}

func TestCloseSandboxResetNotStartedValidatesStrictTarget(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "typed confirmation 불일치",
			body: `{"appId":"a","confirmation":"CLOSE RESET a wrong"}`,
		},
		{
			name: "선두 하이픈 appId",
			body: sandboxResetCloseBody("reset-1", "-a"),
		},
		{
			name: "알 수 없는 필드",
			body: `{"appId":"a","confirmation":"CLOSE RESET a reset-1","platformUserId":"` + testPUID + `"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &fakeLedger{}
			h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

			w := serve(t, h, http.MethodPost,
				"/v1/admin/iap/sandbox-resets/reset-1/close-not-started",
				tt.body, "tok", "syous")
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400. body=%s", w.Code, w.Body.String())
			}
			if len(l.resetCloseCalls) != 0 {
				t.Errorf("검증 실패 closure가 ledger에 도달했다: %+v", l.resetCloseCalls)
			}
		})
	}
}

func TestCloseSandboxResetNotStartedRejectsAlreadyStarted(t *testing.T) {
	a := &fakeAuditor{}
	l := &fakeLedger{resetCloseErr: platformerr.New(
		platformerr.CodeSandboxResetAlreadyStarted, "started")}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

	w := serve(t, h, http.MethodPost,
		"/v1/admin/iap/sandbox-resets/reset-1/close-not-started",
		sandboxResetCloseBody("reset-1", "a"), "tok", "syous")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409. body=%s", w.Code, w.Body.String())
	}
	_, _, code := decodeEnvelope(t, w)
	if code != string(platformerr.CodeSandboxResetAlreadyStarted) {
		t.Errorf("code = %q, want %q", code, platformerr.CodeSandboxResetAlreadyStarted)
	}
	if len(a.records) != 1 ||
		a.records[0].outcome != string(platformerr.CodeSandboxResetAlreadyStarted) {
		t.Errorf("closure conflict 감사 기록 = %+v", a.records)
	}
}

func TestSandboxResetRecoveryEndpointsRejectProduction(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "상태 조회",
			method: http.MethodGet,
			path:   "/v1/admin/iap/sandbox-resets/reset-1",
		},
		{
			name:   "재개",
			method: http.MethodPost,
			path:   "/v1/admin/iap/sandbox-resets/reset-1/resume",
			body:   sandboxResetResumeBody("reset-1", "a"),
		},
		{
			name:   "미시작 종결",
			method: http.MethodPost,
			path:   "/v1/admin/iap/sandbox-resets/reset-1/close-not-started",
			body:   sandboxResetCloseBody("reset-1", "a"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &fakeLedger{env: domain.EnvProduction}
			h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
			w := serve(t, h, tt.method, tt.path, tt.body, "tok", "syous")

			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422. body=%s", w.Code, w.Body.String())
			}
			_, _, code := decodeEnvelope(t, w)
			if code != string(platformerr.CodeEnvironmentMismatch) {
				t.Errorf("code = %q, want %q", code, platformerr.CodeEnvironmentMismatch)
			}
			if len(l.resetStatusCalls) != 0 || len(l.resetResumeCalls) != 0 ||
				len(l.resetCloseCalls) != 0 {
				t.Errorf("production recovery가 ledger에 도달했다: status=%v resume=%v close=%v",
					l.resetStatusCalls, l.resetResumeCalls, l.resetCloseCalls)
			}
		})
	}
}

// sandbox 초기화는 production 원장에서 존재하지 않아야 한다.
//
// 실사용자 결제를 통째로 회수하는 경로가 되기 때문이다.
func TestSandboxResetRejectsProduction(t *testing.T) {
	l := &fakeLedger{env: domain.EnvProduction}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	body := sandboxResetBody("r", testPUID, testResetReason, true)
	w := serve(t, h, http.MethodPost, "/v1/admin/iap/sandbox-reset", body, "tok", "syous")

	if w.Code == http.StatusOK {
		t.Fatal("production 원장에서 초기화가 통과했다")
	}
	if len(l.resetCalls) != 0 {
		t.Error("거부했는데 원장을 건드렸다")
	}
}

// Apple 쪽 삭제 확인 없이는 진행하지 않는다.
//
// 원장만 지우면 Apple에는 거래가 남아 다음 검증이 새 구매로 보고
// 다시 지급한다. 초기화한 줄 알았던 테스터가 상품을 그대로 갖는다.
func TestSandboxResetRequiresAppleConfirmation(t *testing.T) {
	l := &fakeLedger{}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	body := sandboxResetBody("r", testPUID, testResetReason, false)
	w := serve(t, h, http.MethodPost, "/v1/admin/iap/sandbox-reset", body, "tok", "syous")

	if w.Code == http.StatusOK {
		t.Fatal("Apple 삭제 확인 없이 통과했다")
	}
	if len(l.resetCalls) != 0 {
		t.Error("거부했는데 원장을 건드렸다")
	}
}

func TestSandboxResetMarksOrders(t *testing.T) {
	a := &fakeAuditor{}
	l := &fakeLedger{resetKeys: []string{"key-a", "key-b"}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

	body := sandboxResetBody("req-1", testPUID, testResetReason, true)
	w := serve(t, h, http.MethodPost, "/v1/admin/iap/sandbox-reset", body, "tok", "syous")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	if len(l.resetCalls) != 1 || l.resetCalls[0].RequestID != "req-1" {
		t.Errorf("초기화 호출 = %v", l.resetCalls)
	}
	if l.resetCalls[0].Reason != testResetReason || l.resetCalls[0].ActorLogin != "syous" {
		t.Errorf("초기화 영구 payload = %+v", l.resetCalls[0])
	}

	_, result, _ := decodeEnvelope(t, w)
	keys, ok := result["resetOrderKeys"].([]any)
	if !ok || len(keys) != 2 {
		t.Errorf("resetOrderKeys = %v", result["resetOrderKeys"])
	}

	// 되돌릴 수 없는 조작이므로 사유와 실행자가 반드시 남아야 한다.
	if len(a.records) != 1 {
		t.Fatalf("감사 기록 = %d건, want 1", len(a.records))
	}
	rec := a.records[0]
	if rec.action != "iap.sandbox_reset" || rec.outcome != "ok" {
		t.Errorf("record = %+v", rec)
	}
	if rec.detail["reason"] != testResetReason || rec.detail["actor"] != "syous" {
		t.Errorf("detail = %+v", rec.detail)
	}
}

// requestId나 reason이 없으면 받지 않는다.
func TestSandboxResetRequiresFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "requestId 없음",
			body: fmt.Sprintf(`{"platformUserId":%q,"reason":"x","appleClearedConfirmed":true}`, testPUID),
		},
		{
			name: "platformUserId 없음",
			body: `{"requestId":"r","reason":"x","appleClearedConfirmed":true}`,
		},
		{
			name: "reason 없음",
			body: fmt.Sprintf(`{"requestId":"r","platformUserId":%q,"appleClearedConfirmed":true}`, testPUID),
		},
		{
			name: "자유 서술 reason",
			body: strings.Replace(sandboxResetBody("r", testPUID, testResetReason, true),
				testResetReason, "person@example.com 영수증", 1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &fakeLedger{}
			h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
			w := serve(t, h, http.MethodPost, "/v1/admin/iap/sandbox-reset", tt.body, "tok", "syous")

			if w.Code == http.StatusOK {
				t.Error("필수 항목 없이 통과했다")
			}
			if len(l.resetCalls) != 0 {
				t.Error("거부했는데 원장을 건드렸다")
			}
		})
	}
}

// 레지스트리와 원장 환경이 어긋나면 health가 그걸 알려줘야 한다.
//
// 이 검사를 만든 이유는 증상이 한쪽에서만 나기 때문이다. LedgerEnvironment
// 대조는 admin 경로에만 있고 verify에는 없다. 어긋나도 유저 결제는 계속 되고
// 운영자만 아무것도 못 한다. 5xx도 없고 트래픽도 정상이라 대시보드로는 안 잡힌다.
//
// 2026-08-03에 실제로 겪었다. 서비스를 production으로 전환했는데 레지스트리가
// sandbox로 남아 admin이 전부 422였다.
func TestHealthReportsEnvironmentMismatch(t *testing.T) {
	iapApp := func(id string, env registry.LedgerEnvironment) registry.App {
		return registry.App{
			AppID:    id,
			Status:   registry.StatusActive,
			Features: map[string]bool{"iap": true},
			IAP: registry.IAPConfig{
				LedgerEnvironment: env,
				EntitlementIDs:    []string{"sp_a"},
			},
		}
	}

	tests := []struct {
		name   string
		ledger domain.Environment
		apps   []registry.App
		want   []string
	}{
		{
			name:   "환경이 같으면 비어 있다",
			ledger: domain.EnvProduction,
			apps:   []registry.App{iapApp("a", registry.LedgerProduction)},
			want:   nil,
		},
		{
			name:   "어긋난 앱을 돌려준다",
			ledger: domain.EnvProduction,
			apps:   []registry.App{iapApp("a", registry.LedgerSandbox)},
			want:   []string{"a"},
		},
		{
			name:   "환경이 비어 있어도 어긋난 것이다",
			ledger: domain.EnvProduction,
			apps:   []registry.App{iapApp("a", "")},
			want:   []string{"a"},
		},
		{
			// babycare처럼 인증 브리지만 쓰는 앱을 섞으면 신호가 묻힌다.
			name:   "IAP를 쓰지 않는 앱은 세지 않는다",
			ledger: domain.EnvProduction,
			apps: []registry.App{{
				AppID:    "bridge-only",
				Status:   registry.StatusActive,
				Features: map[string]bool{"iap": false},
			}},
			want: nil,
		},
		{
			name:   "여러 앱은 app_id 순으로 준다",
			ledger: domain.EnvProduction,
			apps: []registry.App{
				iapApp("z", registry.LedgerSandbox),
				iapApp("a", registry.LedgerSandbox),
			},
			want: []string{"a", "z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, err := NewAuthenticator(
				&fakeValidator{email: backofficeSA},
				[]string{backofficeReadSA}, []string{backofficeSA},
			)
			if err != nil {
				t.Fatalf("인증기 생성 실패: %v", err)
			}
			h, err := NewHandler(
				&fakeLedger{env: tt.ledger}, &fakeConfig{}, &fakeUsers{},
				&fakeApps{list: tt.apps}, &fakeCatalog{allowed: true}, auth, &fakeAuditor{},
			)
			if err != nil {
				t.Fatalf("핸들러 생성 실패: %v", err)
			}

			w := serve(t, h, http.MethodGet, "/v1/admin/health", "", "tok", "syous")
			if w.Code != http.StatusOK {
				t.Fatalf("health가 200이 아니다: %d", w.Code)
			}

			var got struct {
				Result struct {
					Environment string `json:"environment"`
					Mismatches  []struct {
						AppID    string `json:"appId"`
						Registry string `json:"registry"`
						Ledger   string `json:"ledger"`
					} `json:"environmentMismatches"`
				} `json:"result"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("응답 해석 실패: %v", err)
			}

			if len(got.Result.Mismatches) != len(tt.want) {
				t.Fatalf("불일치 개수가 다르다: %d, want %d (%+v)",
					len(got.Result.Mismatches), len(tt.want), got.Result.Mismatches)
			}
			for i, want := range tt.want {
				m := got.Result.Mismatches[i]
				if m.AppID != want {
					t.Errorf("[%d] appId = %q, want %q", i, m.AppID, want)
				}
				if m.Ledger != string(tt.ledger) {
					t.Errorf("[%d] ledger = %q, want %q", i, m.Ledger, tt.ledger)
				}
				if m.Registry == m.Ledger {
					t.Errorf("[%d] 같은 환경인데 불일치로 보고했다: %q", i, m.Registry)
				}
			}
		})
	}
}

// 응답의 environmentMismatches는 null이 아니라 []여야 한다.
//
// 소비자가 length로 판단하는데 null이면 그 자리에서 터진다.
// 백오피스가 이 값을 화면에 띄우므로 형식이 계약이다.
func TestHealthMismatchesIsAlwaysArray(t *testing.T) {
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA},
		[]string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatalf("인증기 생성 실패: %v", err)
	}
	h, err := NewHandler(
		&fakeLedger{}, &fakeConfig{}, &fakeUsers{},
		&fakeApps{list: []registry.App{}}, &fakeCatalog{allowed: true}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatalf("핸들러 생성 실패: %v", err)
	}

	w := serve(t, h, http.MethodGet, "/v1/admin/health", "", "tok", "syous")
	if !strings.Contains(w.Body.String(), `"environmentMismatches":[]`) {
		t.Errorf("빈 배열이 아니다: %s", w.Body.String())
	}
}

func TestMetricsReportsUserCounts(t *testing.T) {
	measuredAt := time.Date(2026, 8, 7, 13, 21, 28, 0, time.UTC)

	tests := []struct {
		name   string
		counts identity.UserCounts
		want   []string
	}{
		{
			name:   "집계값을 그대로 노출한다",
			counts: identity.UserCounts{Total: 1204, ActiveDay: 87, ActiveWeek: 341},
			want: []string{
				`"totalUsers":1204`,
				`"dailyActiveUsers":87`,
				`"weeklyActiveUsers":341`,
			},
		},
		{
			// 0은 "집계 실패"가 아니라 "아직 아무도 없음"이다. 화면이
			// 둘을 구분하려면 0도 정상 응답으로 와야 한다.
			name:   "0도 정상 응답이다",
			counts: identity.UserCounts{},
			want: []string{
				`"totalUsers":0`,
				`"dailyActiveUsers":0`,
				`"weeklyActiveUsers":0`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, err := NewAuthenticator(
				&fakeValidator{email: backofficeSA},
				[]string{backofficeReadSA}, []string{backofficeSA},
			)
			if err != nil {
				t.Fatalf("인증기 생성 실패: %v", err)
			}
			users := &fakeUsers{counts: tt.counts}
			h, err := NewHandler(
				&fakeLedger{}, &fakeConfig{}, users,
				&fakeApps{}, &fakeCatalog{allowed: true}, auth, &fakeAuditor{},
			)
			if err != nil {
				t.Fatalf("핸들러 생성 실패: %v", err)
			}
			h.WithClock(func() time.Time { return measuredAt })

			w := serve(t, h, http.MethodGet, "/v1/admin/metrics", "", "tok", "syous")
			if w.Code != http.StatusOK {
				t.Fatalf("지표 조회 실패: %d %s", w.Code, w.Body.String())
			}
			for _, want := range tt.want {
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("%s가 없다: %s", want, w.Body.String())
				}
			}
			// 활성 정의를 값으로 박아 둔 계약이다. 나중에 이벤트 기반
			// 집계를 붙일 때 화면이 정의 변화를 알아채는 유일한 신호다.
			if !strings.Contains(w.Body.String(), `"activitySource":"session_last_seen"`) {
				t.Errorf("활성 판정 근거가 없다: %s", w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"measuredAt":"2026-08-07T13:21:28Z"`) {
				t.Errorf("집계 기준 시각이 없다: %s", w.Body.String())
			}
			// 세 값이 같은 기준 시각으로 집계돼야 DAU가 WAU보다 큰
			// 착시가 생기지 않는다.
			if len(users.countedAt) != 1 || !users.countedAt[0].Equal(measuredAt) {
				t.Errorf("집계 기준 시각이 전달되지 않았다: %v", users.countedAt)
			}
		})
	}
}

// 지표는 read 등급이다. 조작 자격증명 없이도 규모를 볼 수 있어야 한다.
func TestMetricsIsReadRole(t *testing.T) {
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeReadSA},
		[]string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatalf("인증기 생성 실패: %v", err)
	}
	h, err := NewHandler(
		&fakeLedger{}, &fakeConfig{}, &fakeUsers{},
		&fakeApps{}, &fakeCatalog{allowed: true}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatalf("핸들러 생성 실패: %v", err)
	}

	w := serve(t, h, http.MethodGet, "/v1/admin/metrics", "", "tok", "reader")
	if w.Code != http.StatusOK {
		t.Fatalf("read 자격증명으로 지표를 못 봤다: %d %s", w.Code, w.Body.String())
	}
}

// 집계가 실패해도 health는 영향받지 않아야 한다.
//
// 개요 화면이 무너진 원인이 정확히 이 결합이었다. 조회 하나가
// 실패하면서 운영자가 원장 환경을 볼 창구까지 함께 닫혔다.
func TestMetricsFailureDoesNotAffectHealth(t *testing.T) {
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA},
		[]string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatalf("인증기 생성 실패: %v", err)
	}
	h, err := NewHandler(
		&fakeLedger{}, &fakeConfig{},
		&fakeUsers{countErr: errors.New("firestore 집계 불통")},
		&fakeApps{}, &fakeCatalog{allowed: true}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatalf("핸들러 생성 실패: %v", err)
	}

	if w := serve(t, h, http.MethodGet, "/v1/admin/metrics", "", "tok", "syous"); w.Code == http.StatusOK {
		t.Fatalf("집계 실패가 성공으로 나왔다: %s", w.Body.String())
	}

	w := serve(t, h, http.MethodGet, "/v1/admin/health", "", "tok", "syous")
	if w.Code != http.StatusOK {
		t.Fatalf("지표 실패로 health가 죽었다: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"environment"`) {
		t.Errorf("원장 환경이 없다: %s", w.Body.String())
	}
}

// 레지스트리 조회가 실패해도 health는 살아 있어야 한다.
//
// health는 진단 창구다. 여기서 죽으면 진짜 문제를 볼 창구까지 같이 닫힌다.
func TestHealthSurvivesRegistryFailure(t *testing.T) {
	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA},
		[]string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatalf("인증기 생성 실패: %v", err)
	}
	h, err := NewHandler(
		&fakeLedger{}, &fakeConfig{}, &fakeUsers{},
		&fakeApps{listErr: errors.New("firestore 불통")},
		&fakeCatalog{allowed: true}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatalf("핸들러 생성 실패: %v", err)
	}

	w := serve(t, h, http.MethodGet, "/v1/admin/health", "", "tok", "syous")
	if w.Code != http.StatusOK {
		t.Fatalf("레지스트리 실패로 health가 죽었다: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"deadLetterCount"`) {
		t.Errorf("나머지 진단 정보가 없다: %s", w.Body.String())
	}
}

// 불일치는 응답만이 아니라 로그로도 나와야 한다.
//
// 응답은 누가 health를 부를 때만 보인다. 알림을 걸 수 있는 신호는 로그다.
// 이 한 줄이 로그 기반 지표의 근거가 되므로 필드 이름이 계약이다.
func TestEnvironmentMismatchLogsWarning(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})))
	t.Cleanup(func() { slog.SetDefault(original) })

	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA},
		[]string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatalf("인증기 생성 실패: %v", err)
	}
	apps := []registry.App{
		{
			AppID: "zulu", Status: registry.StatusActive,
			Features: map[string]bool{"iap": true},
			IAP: registry.IAPConfig{
				LedgerEnvironment: registry.LedgerSandbox,
				EntitlementIDs:    []string{"sp_a"},
			},
		},
		{
			AppID: "alpha", Status: registry.StatusActive,
			Features: map[string]bool{"iap": true},
			IAP: registry.IAPConfig{
				LedgerEnvironment: registry.LedgerSandbox,
				EntitlementIDs:    []string{"sp_a"},
			},
		},
	}
	h, err := NewHandler(
		&fakeLedger{env: domain.EnvProduction}, &fakeConfig{}, &fakeUsers{},
		&fakeApps{list: apps}, &fakeCatalog{allowed: true}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatalf("핸들러 생성 실패: %v", err)
	}

	if w := serve(t, h, http.MethodGet, "/v1/admin/health", "", "tok", "syous"); w.Code != http.StatusOK {
		t.Fatalf("health가 200이 아니다: %d", w.Code)
	}

	var found bool
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["level"] != "WARN" || !strings.Contains(fmt.Sprint(entry["msg"]), "어긋나") {
			continue
		}
		found = true

		if got := entry["ledger_environment"]; got != string(domain.EnvProduction) {
			t.Errorf("ledger_environment = %v, want %v", got, domain.EnvProduction)
		}
		// 정렬된 목록이라 알림 본문이 실행마다 달라지지 않는다.
		if got := entry["apps"]; got != "alpha,zulu" {
			t.Errorf("apps = %v, want alpha,zulu", got)
		}
		if got := entry["count"]; got != float64(2) {
			t.Errorf("count = %v, want 2", got)
		}
	}
	if !found {
		t.Errorf("경고 로그가 없다: %s", buf.String())
	}
}

// 정상일 때는 경고를 남기지 않는다. 매번 찍으면 알림이 무의미해진다.
func TestNoWarningWhenEnvironmentsMatch(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})))
	t.Cleanup(func() { slog.SetDefault(original) })

	auth, err := NewAuthenticator(
		&fakeValidator{email: backofficeSA},
		[]string{backofficeReadSA}, []string{backofficeSA},
	)
	if err != nil {
		t.Fatalf("인증기 생성 실패: %v", err)
	}
	h, err := NewHandler(
		&fakeLedger{env: domain.EnvProduction}, &fakeConfig{}, &fakeUsers{},
		&fakeApps{list: []registry.App{{
			AppID: "a", Status: registry.StatusActive,
			Features: map[string]bool{"iap": true},
			IAP: registry.IAPConfig{
				LedgerEnvironment: registry.LedgerProduction,
				EntitlementIDs:    []string{"sp_a"},
			},
		}}}, &fakeCatalog{allowed: true}, auth, &fakeAuditor{},
	)
	if err != nil {
		t.Fatalf("핸들러 생성 실패: %v", err)
	}

	serve(t, h, http.MethodGet, "/v1/admin/health", "", "tok", "syous")

	if strings.Contains(buf.String(), "어긋나") {
		t.Errorf("정상인데 경고를 남겼다: %s", buf.String())
	}
}
