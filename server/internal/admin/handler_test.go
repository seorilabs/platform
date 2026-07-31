package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

const backofficeSA = "backoffice-admin@seorilabs-platform.iam.gserviceaccount.com"

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
func (f *fakeLedger) Environment() domain.Environment { return domain.EnvSandbox }

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

	auth, err := NewAuthenticator(v, allowed)
	if err != nil {
		t.Fatalf("인증기 생성 실패: %v", err)
	}
	h, err := NewHandler(l, &fakeConfig{}, auth, a)
	if err != nil {
		t.Fatalf("핸들러 생성 실패: %v", err)
	}
	return h
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

// 인증 없는 Admin 경로를 하나라도 열면 원장 전체가 노출된다.
func TestAllRoutesRequireAuth(t *testing.T) {
	h := newHandler(t, &fakeLedger{}, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/orders/recent", ""},
		{http.MethodGet, "/v1/admin/users/pu_1/entitlements", ""},
		{http.MethodGet, "/v1/admin/operator-grants", ""},
		{http.MethodGet, "/v1/admin/health", ""},
		{http.MethodPost, "/v1/admin/entitlements/grant", `{}`},
		{http.MethodPost, "/v1/admin/entitlements/revoke", `{}`},
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
		{OrderKey: "ok1", PlatformUserID: "pu_1", EntitlementID: "sp_a", State: "active"},
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
}

func TestUserEntitlements(t *testing.T) {
	l := &fakeLedger{entitlements: []ledger.UserEntitlement{
		{EntitlementID: "sp_a", Active: true},
		// 비활성도 준다. 왜 없는지를 봐야 CS가 가능하다
		{EntitlementID: "sp_b", Active: false},
	}}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	w := serve(t, h, http.MethodGet, "/v1/admin/users/pu_01J/entitlements", "", "tok", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	_, result, _ := decodeEnvelope(t, w)
	if result["platformUserId"] != "pu_01J" {
		t.Errorf("puid = %v", result["platformUserId"])
	}
	list, _ := result["entitlements"].([]any)
	if len(list) != 2 {
		t.Errorf("entitlement %d건, want 2", len(list))
	}
}

// 지급은 requestId와 reason이 있어야 한다.
func TestGrantRequiresRequestIdAndReason(t *testing.T) {
	// 원장이 검증하므로 핸들러는 그대로 전달만 한다.
	// 여기서는 값이 원장까지 도달하는지 본다.
	l := &fakeLedger{applied: true}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	body := `{"requestId":"req-1","platformUserId":"pu_1","entitlementId":"sp_a","reason":"CS 보상","appId":"lizard-tycoon"}`
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
	if got.Reason != "CS 보상" {
		t.Errorf("reason = %q", got.Reason)
	}
	// 누가 눌렀는지가 원장에 남아야 한다
	if got.ActorLogin != "syous" {
		t.Errorf("actorLogin = %q, want 헤더 값", got.ActorLogin)
	}
}

// 사람 계정 헤더가 없으면 서비스 계정으로라도 남긴다.
func TestActorFallsBackToServiceAccount(t *testing.T) {
	l := &fakeLedger{applied: true}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	body := `{"requestId":"r","platformUserId":"pu_1","entitlementId":"sp_a","reason":"x","appId":"a"}`
	serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant", body, "tok", "")

	if l.grantCalls[0].ActorLogin != backofficeSA {
		t.Errorf("actorLogin = %q, want 서비스 계정", l.grantCalls[0].ActorLogin)
	}
}

// 불변식 8. 권한을 결정하는 필드를 요청에 주입할 수 없다.
func TestRejectsUnknownFields(t *testing.T) {
	l := &fakeLedger{}
	h := newHandler(t, l, &fakeValidator{email: backofficeSA}, &fakeAuditor{})

	// actorLogin을 직접 넣으려는 시도
	body := `{"requestId":"r","platformUserId":"pu_1","entitlementId":"sp_a","reason":"x","actorLogin":"위조"}`
	w := serve(t, h, http.MethodPost, "/v1/admin/entitlements/grant", body, "tok", "")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if len(l.grantCalls) != 0 {
		t.Error("주입된 요청이 원장에 도달했다")
	}
}

// 조작은 성패와 무관하게 감사에 남아야 한다.
func TestOperationsAreAudited(t *testing.T) {
	t.Run("성공", func(t *testing.T) {
		a := &fakeAuditor{}
		l := &fakeLedger{applied: true}
		h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

		body := `{"requestId":"r","platformUserId":"pu_1","entitlementId":"sp_a","reason":"보상","appId":"a"}`
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
		if rec.detail["reason"] != "보상" {
			t.Errorf("reason이 감사에 없다: %v", rec.detail)
		}
	})

	t.Run("실패도 남는다", func(t *testing.T) {
		a := &fakeAuditor{}
		l := &fakeLedger{err: platformerr.New(platformerr.CodeRequestInvalid, "사유 없음")}
		h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

		body := `{"requestId":"r","platformUserId":"pu_1","entitlementId":"sp_a","reason":"x","appId":"a"}`
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

	// 이미 처리된 요청은 실패가 아니다. 구분해서 남긴다.
	t.Run("멱등 재요청", func(t *testing.T) {
		a := &fakeAuditor{}
		l := &fakeLedger{applied: false}
		h := newHandler(t, l, &fakeValidator{email: backofficeSA}, a)

		body := `{"requestId":"r","platformUserId":"pu_1","entitlementId":"sp_a","reason":"x","appId":"a"}`
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

	body := `{"requestId":"r","platformUserId":"pu_1","entitlementId":"sp_a","reason":"오지급","appId":"a"}`
	serve(t, h, http.MethodPost, "/v1/admin/entitlements/revoke", body, "tok", "syous")

	if len(l.revokeCalls) != 1 {
		t.Errorf("회수 호출 %d회", len(l.revokeCalls))
	}
	if len(l.grantCalls) != 0 {
		t.Error("회수 요청이 지급으로 갔다")
	}
}

func TestNewValidation(t *testing.T) {
	auth, _ := NewAuthenticator(&fakeValidator{}, nil)

	if _, err := NewHandler(nil, &fakeConfig{}, auth, nil); err == nil {
		t.Error("원장 없이 통과시켰다")
	}
	if _, err := NewHandler(&fakeLedger{}, &fakeConfig{}, nil, nil); err == nil {
		t.Error("인증기 없이 통과시켰다")
	}
	if _, err := NewAuthenticator(nil, nil); err == nil {
		t.Error("검증기 없이 통과시켰다")
	}
}

// break-glass의 핵심 경로다. 백오피스가 죽어도 이건 돼야 한다.
func TestMaintenanceToggle(t *testing.T) {
	newWithConfig := func(t *testing.T, cfg *fakeConfig, a *fakeAuditor) *Handler {
		t.Helper()
		auth, err := NewAuthenticator(&fakeValidator{email: backofficeSA}, nil)
		if err != nil {
			t.Fatalf("인증기 생성 실패: %v", err)
		}
		h, err := NewHandler(&fakeLedger{}, cfg, auth, a)
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
