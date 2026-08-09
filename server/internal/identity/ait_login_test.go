package identity

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAITLoginExchangesCodeAndHashesUserKey(t *testing.T) {
	var generateBody, authorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api-partner/v1/apps-in-toss/user/oauth2/generate-token":
			raw, _ := io.ReadAll(r.Body)
			generateBody = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resultType":"SUCCESS","success":{"accessToken":"transient-access-token","refreshToken":"must-not-persist"}}`))
		case "/api-partner/v1/apps-in-toss/user/oauth2/login-me":
			authorization = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resultType":"SUCCESS","success":{"userKey":123456789}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewAITLoginClient(tls.Certificate{Certificate: [][]byte{{1}}}, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	client.baseURL = server.URL
	hashed, err := client.Verify(t.Context(), "one-time-code", "SANDBOX")
	if err != nil {
		t.Fatal(err)
	}
	if hashed != "15e2b0d3c33891ebb0f1ef609ec419420c20e320ce94c65fbc8c3312448eb225" {
		t.Fatalf("hash=%q", hashed)
	}
	if !strings.Contains(generateBody, `"authorizationCode":"one-time-code"`) || !strings.Contains(generateBody, `"referrer":"SANDBOX"`) {
		t.Fatalf("generate body=%s", generateBody)
	}
	if authorization != "Bearer transient-access-token" {
		t.Fatalf("authorization=%q", authorization)
	}
}
