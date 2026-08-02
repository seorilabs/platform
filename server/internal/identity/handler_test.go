package identity

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
