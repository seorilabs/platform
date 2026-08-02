package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
	orders       []ledger.OrderSummary
	entitlements []ledger.UserEntitlement
	grants       []ledger.OperatorRecord
	revocations  []ledger.OperatorRecord
	deadLetters  int
	err          error

	grantCalls  []ledger.OperatorInput
	revokeCalls []ledger.OperatorInput
	applied     bool

	resetKeys  []string
	resetCalls []ledger.SandboxResetInput
	rateCalls  []string
	rateErr    error
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
	user identity.SupportUser
	err  error
}

func (f *fakeUsers) LookupSupportUser(_ context.Context, reference string) (identity.SupportUser, error) {
	if f.err != nil {
		return identity.SupportUser{}, f.err
	}
	if f.user.PlatformUserID != "" {
		return f.user, nil
	}
	return identity.SupportUser{
		PlatformUserID: reference,
		AppID:          "a",
		SupportCode:    "A-TESTCODE",
		IsAnonymous:    true,
		CreatedAt:      time.Unix(1, 0).UTC(),
		LastSeenAt:     time.Unix(2, 0).UTC(),
	}, nil
}

type fakeApps struct {
	app registry.App
	err error
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
		IAP:      registry.IAPConfig{LedgerEnvironment: registry.LedgerSandbox},
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
		{http.MethodGet, "/v1/admin/users/A-TESTCODE", ""},
		{http.MethodGet, "/v1/admin/users/" + testPUID + "/entitlements", ""},
		{http.MethodGet, "/v1/admin/operator-grants", ""},
		{http.MethodGet, "/v1/admin/iap/catalog", ""},
		{http.MethodGet, "/v1/admin/health", ""},
		{http.MethodPost, "/v1/admin/entitlements/grant", `{}`},
		{http.MethodPost, "/v1/admin/entitlements/revoke", `{}`},
		{http.MethodPost, "/v1/admin/iap/sandbox-reset", `{}`},
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
		{OrderKey: "ok1", PlatformUserID: testPUID, EntitlementID: "sp_a", State: "active"},
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

func TestUserEntitlements(t *testing.T) {
	l := &fakeLedger{entitlements: []ledger.UserEntitlement{
		{EntitlementID: "sp_a", Active: true, Sources: []ledger.EntitlementSource{{
			Platform: "operator", ProductID: "sp_a", State: "active", OrderKey: "order-a",
		}}},
		// 비활성도 준다. 왜 없는지를 봐야 CS가 가능하다
		{EntitlementID: "sp_b", Active: false},
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

func TestIAPCatalogReturnsOnlyEntitlementIDs(t *testing.T) {
	h := newHandler(t, &fakeLedger{}, &fakeValidator{email: backofficeSA}, &fakeAuditor{})
	w := serve(t, h, http.MethodGet, "/v1/admin/iap/catalog", "", "tok", "reader")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	_, result, _ := decodeEnvelope(t, w)
	ids, _ := result["entitlements"].([]any)
	if len(ids) != 2 || ids[0] != "sp_a" {
		t.Errorf("entitlements = %v", ids)
	}
	if strings.Contains(w.Body.String(), "google_play") || strings.Contains(w.Body.String(), "app_store") {
		t.Errorf("SKU 정보가 노출됐다: %s", w.Body.String())
	}
}

func TestUserLookupExposesOnlySupportFields(t *testing.T) {
	user := identity.SupportUser{
		PlatformUserID: testPUID,
		AppID:          "a",
		SupportCode:    "A-TESTCODE",
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

	w := serve(t, h, http.MethodGet, "/v1/admin/users/A-TESTCODE", "", "tok", "reader")
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
			name:  "카탈로그 외 entitlement",
			body:  grantBody("r", testPUID, "sp_a", testGrantReason),
			users: &fakeUsers{}, apps: &fakeApps{}, catalog: &fakeCatalog{allowed: false},
			ledger: &fakeLedger{}, wantStatus: http.StatusUnprocessableEntity,
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
	serve(t, h, http.MethodPost, "/v1/admin/entitlements/revoke", body, "tok", "syous")

	if len(l.revokeCalls) != 1 {
		t.Errorf("회수 호출 %d회", len(l.revokeCalls))
	}
	if len(l.grantCalls) != 0 {
		t.Error("회수 요청이 지급으로 갔다")
	}
	if l.revokeCalls[0].GrantRequestID != "grant-r" {
		t.Errorf("grantRequestId = %q", l.revokeCalls[0].GrantRequestID)
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
