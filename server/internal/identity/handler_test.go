package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/seorilabs/platform/server/internal/registry"
)

func TestFirebaseCustomTokenHandler(t *testing.T) {
	customTokens := &fakeCustomTokenIssuer{token: "signed-custom-token"}
	service := newBridgeTestService(t, fakeVerifier{}, newMemRepo(), customTokens)
	mux := http.NewServeMux()
	NewHandler(service).Register(mux)

	t.Run("기존 uid를 보존한 token 응답", func(t *testing.T) {
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/auth/firebase-custom-token",
			bytes.NewBufferString(`{"appId":"lizard-tycoon","existingFirebaseIdToken":"legacy-uid"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(AppHeader, "lizard-tycoon")
		response := httptest.NewRecorder()

		mux.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var envelope struct {
			OK     bool `json:"ok"`
			Result struct {
				FirebaseCustomToken string `json:"firebaseCustomToken"`
				AppUserID           string `json:"appUserId"`
			} `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("응답 decode 실패: %v", err)
		}
		if !envelope.OK || envelope.Result.FirebaseCustomToken != "signed-custom-token" ||
			envelope.Result.AppUserID != "legacy-uid" {
			t.Fatalf("response = %#v", envelope)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q", got)
		}
	})

	t.Run("본문과 헤더의 앱 불일치 거부", func(t *testing.T) {
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/auth/firebase-custom-token",
			bytes.NewBufferString(`{"appId":"other-app"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(AppHeader, "lizard-tycoon")
		response := httptest.NewRecorder()

		mux.ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("알 수 없는 authority 필드 거부", func(t *testing.T) {
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/auth/firebase-custom-token",
			bytes.NewBufferString(`{"appId":"lizard-tycoon","uid":"injected"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(AppHeader, "lizard-tycoon")
		response := httptest.NewRecorder()

		mux.ServeHTTP(response, request)

		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})
}

func TestFirebaseCustomTokenHandlerRequiresAppCheck(t *testing.T) {
	customTokens := &fakeCustomTokenIssuer{token: "signed-custom-token"}
	appCheck := &fakeAppCheckVerifier{}
	service := newBridgeTestService(t, fakeVerifier{}, newMemRepo(), customTokens)
	app := testApp()
	app.Features = map[string]bool{"firebase_custom_token_bridge": true}
	app.FirebaseCustomTokenServiceAccount =
		"platform-auth@lizard-tycoon.iam.gserviceaccount.com"
	app.RequireAppCheck = true
	service.registry = registry.New(fakeSource{apps: []registry.App{app}})
	service.WithAppCheckVerifier(appCheck)
	mux := http.NewServeMux()
	NewHandler(service).Register(mux)

	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/auth/firebase-custom-token",
		bytes.NewBufferString(`{"appId":"lizard-tycoon"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(AppHeader, "lizard-tycoon")
	request.Header.Set("X-Firebase-AppCheck", "attested-token")
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if appCheck.token != "attested-token" {
		t.Fatalf("App Check token = %q", appCheck.token)
	}
}

func TestDeleteFirebaseAccountHandler(t *testing.T) {
	service := newBridgeTestService(
		t,
		fakeVerifier{},
		newMemRepo(),
		&fakeCustomTokenIssuer{token: "signed-custom-token"},
	)
	mux := http.NewServeMux()
	NewHandler(service).Register(mux)

	request := httptest.NewRequest(
		http.MethodDelete,
		"/v1/auth/firebase-account",
		bytes.NewBufferString(
			`{"appId":"lizard-tycoon","firebaseIdToken":"firebase-user"}`,
		),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(AppHeader, "lizard-tycoon")
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Deleted bool `json:"deleted"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("응답 decode 실패: %v", err)
	}
	if !envelope.OK || !envelope.Result.Deleted {
		t.Fatalf("response = %#v", envelope)
	}
}

func TestAccountLinkHandlers(t *testing.T) {
	accounts := newMemoryAccountRepo()
	provider := &fakeAccountProvider{name: "kakao", subject: "provider-subject"}
	customTokens := &fakeCustomTokenIssuer{token: "firebase-custom-token"}
	service := newAccountTestService(t, accounts, provider, customTokens)
	appCheck := &fakeAppCheckVerifier{}
	service.WithAppCheckVerifier(appCheck)

	guest, err := service.CreateSession(context.Background(), "lizard-tycoon", Credential{
		Kind: KindFirebaseIDToken, Value: "firebase-anonymous-uid",
	}, ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	accounts.mu.Lock()
	accounts.users[guest.PlatformUserID] = guest.AppUserID
	accounts.mu.Unlock()

	mux := http.NewServeMux()
	NewHandler(service).Register(mux)
	challengeRequest := httptest.NewRequest(
		http.MethodPost, "/v1/auth/account-link-challenges",
		bytes.NewBufferString(`{"provider":"kakao"}`),
	)
	setAccountLinkHeaders(challengeRequest, guest.PlatformToken)
	challengeResponse := httptest.NewRecorder()
	mux.ServeHTTP(challengeResponse, challengeRequest)
	if challengeResponse.Code != http.StatusCreated {
		t.Fatalf("challenge status = %d, body = %s",
			challengeResponse.Code, challengeResponse.Body.String())
	}
	var challengeEnvelope struct {
		Result struct {
			Nonce string `json:"nonce"`
		} `json:"result"`
	}
	if err := json.Unmarshal(challengeResponse.Body.Bytes(), &challengeEnvelope); err != nil {
		t.Fatal(err)
	}
	if challengeEnvelope.Result.Nonce == "" {
		t.Fatal("challenge nonce is empty")
	}

	body, err := json.Marshal(map[string]string{
		"provider": "kakao", "idToken": "provider-id-token", "nonce": challengeEnvelope.Result.Nonce,
	})
	if err != nil {
		t.Fatal(err)
	}
	linkRequest := httptest.NewRequest(
		http.MethodPost, "/v1/auth/account-links", bytes.NewReader(body),
	)
	setAccountLinkHeaders(linkRequest, guest.PlatformToken)
	linkResponse := httptest.NewRecorder()
	mux.ServeHTTP(linkResponse, linkRequest)
	if linkResponse.Code != http.StatusOK {
		t.Fatalf("link status = %d, body = %s", linkResponse.Code, linkResponse.Body.String())
	}
	var linkEnvelope struct {
		Result struct {
			FirebaseCustomToken string `json:"firebaseCustomToken"`
			Provider            string `json:"provider"`
			Session             struct {
				IsLinkedAccount bool `json:"isLinkedAccount"`
			} `json:"session"`
		} `json:"result"`
	}
	if err := json.Unmarshal(linkResponse.Body.Bytes(), &linkEnvelope); err != nil {
		t.Fatal(err)
	}
	if !linkEnvelope.Result.Session.IsLinkedAccount ||
		linkEnvelope.Result.FirebaseCustomToken != "firebase-custom-token" ||
		linkEnvelope.Result.Provider != "kakao" {
		t.Fatalf("response = %#v", linkEnvelope)
	}
	if appCheck.calls != 2 || appCheck.token != "attested-token" {
		t.Fatalf("App Check calls = %d, token = %q", appCheck.calls, appCheck.token)
	}
}

func TestKakaoUnlinkWebhook(t *testing.T) {
	newHandler := func(t *testing.T, accounts *memoryAccountRepo) *http.ServeMux {
		t.Helper()
		service := newAccountTestService(t, accounts,
			&fakeAccountProvider{name: "kakao", subject: "provider-subject"},
			&fakeCustomTokenIssuer{token: "firebase-custom-token"},
		)
		mux := http.NewServeMux()
		NewHandler(service).WithKakaoUnlinkWebhook(KakaoUnlinkWebhookConfig{
			PlatformAppID: "ungeul",
			KakaoAppID:    "1559177",
			AdminKey:      []byte("primary-admin-key"),
		}).Register(mux)
		return mux
	}
	request := func(body url.Values, adminKey string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/webhooks/kakao/unlink",
			bytes.NewBufferString(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Authorization", "KakaoAK "+adminKey)
		return r
	}
	validBody := url.Values{
		"app_id":        {"1559177"},
		"user_id":       {"123456789"},
		"referrer_type": {"UNLINK_FROM_APPS"},
	}

	t.Run("인증된 알림은 빈 200으로 멱등 처리", func(t *testing.T) {
		accounts := newMemoryAccountRepo()
		accounts.mappings["ungeul\x00kakao\x00123456789"] = ConnectedAccount{
			PlatformUserID: "pu-linked", AppUserID: "firebase-user", Provider: "kakao",
		}
		accounts.linked["pu-linked"] = true
		response := httptest.NewRecorder()
		newHandler(t, accounts).ServeHTTP(response, request(validBody, "primary-admin-key"))
		if response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
		}
		if accounts.disconnectedApp != "ungeul" || accounts.disconnectedProvider != "kakao" ||
			accounts.disconnectedSubject != "123456789" || accounts.linked["pu-linked"] {
			t.Fatalf("disconnect = %#v", accounts)
		}
	})

	t.Run("잘못된 Admin Key는 거부", func(t *testing.T) {
		accounts := newMemoryAccountRepo()
		response := httptest.NewRecorder()
		newHandler(t, accounts).ServeHTTP(response, request(validBody, "wrong-key"))
		if response.Code != http.StatusUnauthorized || accounts.disconnectedSubject != "" {
			t.Fatalf("status = %d, disconnected subject = %q",
				response.Code, accounts.disconnectedSubject)
		}
	})

	t.Run("Kakao app ID 불일치는 거부", func(t *testing.T) {
		accounts := newMemoryAccountRepo()
		body := url.Values{
			"app_id": {"9999999"}, "user_id": {"123456789"},
			"referrer_type": {"UNLINK_FROM_APPS"},
		}
		response := httptest.NewRecorder()
		newHandler(t, accounts).ServeHTTP(response, request(body, "primary-admin-key"))
		if response.Code != http.StatusBadRequest || accounts.disconnectedSubject != "" {
			t.Fatalf("status = %d, disconnected subject = %q",
				response.Code, accounts.disconnectedSubject)
		}
	})

	t.Run("인증 후 내부 오류도 Kakao 계약대로 빈 200", func(t *testing.T) {
		accounts := newMemoryAccountRepo()
		accounts.disconnectErr = errors.New("store unavailable")
		response := httptest.NewRecorder()
		newHandler(t, accounts).ServeHTTP(response, request(validBody, "primary-admin-key"))
		if response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
		}
	})
}

func setAccountLinkHeaders(request *http.Request, platformToken string) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(AppHeader, "lizard-tycoon")
	request.Header.Set("Authorization", "Bearer "+platformToken)
	request.Header.Set("X-Firebase-AppCheck", "attested-token")
}
