package iap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/verify"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// fakeService는 유스케이스를 대신한다. 핸들러 경계만 검증한다.
type fakeService struct {
	gotProof domain.Proof
	gotAppID string
	gotPUID  string
	out      verify.Outcome
	err      error

	entitlements []string
}

func (f *fakeService) VerifyPurchase(
	_ context.Context, appID, puid string, proof domain.Proof,
) (verify.Outcome, error) {
	f.gotAppID, f.gotPUID, f.gotProof = appID, puid, proof
	return f.out, f.err
}

func (f *fakeService) ListEntitlements(_ context.Context, puid string) ([]string, error) {
	f.gotPUID = puid
	return f.entitlements, f.err
}

func (f *fakeService) AccountReferences(puid string) (string, string, error) {
	f.gotPUID = puid
	return "google-ref", "apple-ref", f.err
}

// fakeSessions는 인증을 대신한다.
type fakeSessions struct {
	sess identity.Session
	err  error
}

func (f *fakeSessions) Authenticate(*http.Request) (identity.Session, error) {
	return f.sess, f.err
}

func paidSession() identity.Session {
	return identity.Session{
		PlatformUserID: "pu_01J000000000000000000000",
		AppID:          "lizard-tycoon",
		AppUserID:      "firebase-uid-abc",
		IsAnonymous:    false,
	}
}

// serve는 요청 하나를 처리하고 응답을 돌려준다.
func serve(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
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

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// errorCode는 envelope에서 에러 코드를 꺼낸다.
func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()

	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("응답 해석 실패: %v (body=%s)", err, w.Body.String())
	}
	if env.OK {
		t.Fatalf("에러를 기대했는데 성공 응답이다: %s", w.Body.String())
	}
	return env.Error.Code
}

const validBody = `{"platform":"google_play","productId":"gecko_galaxy","token":"tok-1"}`

func TestVerifyPurchaseSuccess(t *testing.T) {
	svc := &fakeService{out: verify.Outcome{
		Status:        "verified",
		EntitlementID: "sp_galaxy_gecko",
		Entitlements:  []string{"sp_galaxy_gecko"},
	}}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})

	w := serve(t, h, http.MethodPost, "/v1/iap/verify", validBody)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	// 소유자는 세션에서만 온다. 요청 body가 정하지 않는다.
	if svc.gotPUID != "pu_01J000000000000000000000" {
		t.Errorf("puid = %q", svc.gotPUID)
	}
	if svc.gotAppID != "lizard-tycoon" {
		t.Errorf("appId = %q", svc.gotAppID)
	}
	if svc.gotProof.Platform != domain.PlatformGooglePlay {
		t.Errorf("platform = %q", svc.gotProof.Platform)
	}
	if svc.gotProof.Token != "tok-1" {
		t.Errorf("token = %q", svc.gotProof.Token)
	}

	// 불변식 12. 결제 응답은 캐시하지 않는다.
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// 익명 신원은 결제할 수 없다.
//
// getAnonymousKey 해시는 bearer 자격증명이 아니라 타인 사칭이 가능하다.
// 허용하면 남의 entitlement를 받아갈 수 있다.
func TestAnonymousCannotPay(t *testing.T) {
	anon := paidSession()
	anon.IsAnonymous = true
	h := NewHandler(&fakeService{}, &fakeSessions{sess: anon})

	routes := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/v1/iap/verify", validBody},
		{http.MethodGet, "/v1/iap/entitlements", ""},
		{http.MethodPost, "/v1/iap/account-references", ""},
	}

	for _, rt := range routes {
		t.Run(rt.path, func(t *testing.T) {
			w := serve(t, h, rt.method, rt.path, rt.body)

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", w.Code)
			}
			if code := errorCode(t, w); code != string(platformerr.CodeAnonymousNotAllowed) {
				t.Errorf("code = %q, want anonymous_not_allowed", code)
			}
		})
	}
}

// 불변식 8. 권한을 결정하는 필드를 요청에 주입할 수 없다.
func TestRejectsInjectedAuthorityFields(t *testing.T) {
	svc := &fakeService{}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})

	injections := []struct {
		name string
		body string
	}{
		{"platformUserId 주입",
			`{"platform":"google_play","productId":"p","token":"t","platformUserId":"pu_남의것"}`},
		{"entitlementId 주입",
			`{"platform":"google_play","productId":"p","token":"t","entitlementId":"sp_공짜"}`},
		{"granted 주입",
			`{"platform":"google_play","productId":"p","token":"t","granted":true}`},
		{"aitUserKey 주입",
			`{"platform":"apps_in_toss","productId":"p","token":"t","aitUserKey":"남의키"}`},
	}

	for _, tt := range injections {
		t.Run(tt.name, func(t *testing.T) {
			w := serve(t, h, http.MethodPost, "/v1/iap/verify", tt.body)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			// 서비스까지 도달하면 안 된다
			if svc.gotProof.Token != "" {
				t.Errorf("주입된 요청이 서비스에 도달했다: %+v", svc.gotProof)
			}
		})
	}
}

// AIT 계정 해시는 검증된 Toss Login 세션에서만 온다.
func TestAITAccountHashComesFromSession(t *testing.T) {
	sess := paidSession()
	sess.AppUserID = "ait:toss-user-key-hash"

	svc := &fakeService{}
	h := NewHandler(svc, &fakeSessions{sess: sess})

	body := `{"platform":"apps_in_toss","productId":"ait_gecko","token":"order-1"}`
	if w := serve(t, h, http.MethodPost, "/v1/iap/verify", body); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	if svc.gotProof.AITAccountHash != "toss-user-key-hash" {
		t.Errorf("aitAccountHash = %q, want 세션 해시", svc.gotProof.AITAccountHash)
	}
}

func TestAITPurchaseRejectsNonTossLoginSession(t *testing.T) {
	h := NewHandler(&fakeService{}, &fakeSessions{sess: paidSession()})
	body := `{"platform":"apps_in_toss","productId":"ait_gecko","token":"order-1"}`
	w := serve(t, h, http.MethodPost, "/v1/iap/verify", body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", w.Code, w.Body.String())
	}
	if code := errorCode(t, w); code != string(platformerr.CodeAuthForbidden) {
		t.Errorf("code = %q, want %q", code, platformerr.CodeAuthForbidden)
	}
}

// 다른 마켓에는 AIT 키를 넘기지 않는다.
func TestAITAccountHashNotLeakedToOtherMarkets(t *testing.T) {
	svc := &fakeService{}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})

	if w := serve(t, h, http.MethodPost, "/v1/iap/verify", validBody); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if svc.gotProof.AITAccountHash != "" {
		t.Errorf("Play 요청에 AIT 계정 해시가 실렸다: %q", svc.gotProof.AITAccountHash)
	}
}

func TestVerifyRejectsBadRequest(t *testing.T) {
	h := NewHandler(&fakeService{}, &fakeSessions{sess: paidSession()})

	tests := []struct {
		name string
		body string
		code platformerr.Code
	}{
		{"모르는 마켓",
			`{"platform":"네이버","productId":"p","token":"t"}`,
			platformerr.CodePlatformMismatch},
		// 운영자 지급은 원장 source이지 클라이언트가 쓸 수 있는 경로가 아니다
		{"operator를 마켓으로 위장",
			`{"platform":"operator","productId":"p","token":"t"}`,
			platformerr.CodePlatformMismatch},
		{"빈 상품",
			`{"platform":"google_play","productId":"","token":"t"}`,
			platformerr.CodeProofInvalid},
		{"빈 토큰",
			`{"platform":"google_play","productId":"p","token":""}`,
			platformerr.CodeProofInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := serve(t, h, http.MethodPost, "/v1/iap/verify", tt.body)
			if code := errorCode(t, w); code != string(tt.code) {
				t.Errorf("code = %q, want %q", code, tt.code)
			}
		})
	}
}

// 인증 실패는 그대로 전달된다.
func TestUnauthenticatedIsRejected(t *testing.T) {
	authErr := platformerr.New(platformerr.CodeAuthRequired, "로그인이 필요해요")
	h := NewHandler(&fakeService{}, &fakeSessions{err: authErr})

	w := serve(t, h, http.MethodPost, "/v1/iap/verify", validBody)
	if code := errorCode(t, w); code != string(platformerr.CodeAuthRequired) {
		t.Errorf("code = %q, want auth_required", code)
	}
}

// 유스케이스 에러는 코드와 상태를 유지한 채 전달된다.
func TestServiceErrorPropagates(t *testing.T) {
	svc := &fakeService{err: platformerr.New(
		platformerr.CodePurchaseOwnedByAnotherUser, "다른 계정의 구매예요")}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})

	w := serve(t, h, http.MethodPost, "/v1/iap/verify", validBody)

	// 불변식 4는 409여야 한다
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
	if code := errorCode(t, w); code != string(platformerr.CodePurchaseOwnedByAnotherUser) {
		t.Errorf("code = %q", code)
	}
}

func TestListEntitlements(t *testing.T) {
	svc := &fakeService{entitlements: []string{"sp_galaxy_gecko"}}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})

	w := serve(t, h, http.MethodGet, "/v1/iap/entitlements", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Status       string   `json:"status"`
			Entitlements []string `json:"entitlements"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("응답 해석 실패: %v", err)
	}
	// status는 스펙에서 required다. 빼면 클라이언트가 응답을 통째로
	// 거부하고 복원이 매번 실패한다. 그러면 로컬 캐시가 서버 원장으로
	// 교정되지 않아 환불된 유저가 상품을 계속 갖고 있는 것처럼 보인다.
	if env.Result.Status != "verified" {
		t.Errorf("status = %q, want verified", env.Result.Status)
	}
	if len(env.Result.Entitlements) != 1 || env.Result.Entitlements[0] != "sp_galaxy_gecko" {
		t.Errorf("entitlements = %v", env.Result.Entitlements)
	}
	if svc.gotPUID != "pu_01J000000000000000000000" {
		t.Errorf("puid = %q", svc.gotPUID)
	}
}

func TestAccountReferences(t *testing.T) {
	svc := &fakeService{}
	h := NewHandler(svc, &fakeSessions{sess: paidSession()})

	w := serve(t, h, http.MethodPost, "/v1/iap/account-references", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var env struct {
		Result struct {
			GooglePlay string `json:"googlePlayObfuscatedAccountId"`
			AppStore   string `json:"appStoreAppAccountToken"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("응답 해석 실패: %v", err)
	}
	if env.Result.GooglePlay != "google-ref" || env.Result.AppStore != "apple-ref" {
		t.Errorf("참조 = %+v", env.Result)
	}
}
