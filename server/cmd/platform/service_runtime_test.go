//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/blocklist"
	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/identity/providers/oidc"
	"github.com/seorilabs/platform/server/internal/inbox"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/remoteconfig"
	"github.com/seorilabs/platform/server/internal/store"
)

// 외부 IAM/App Check만 대체한다. JWT 검증, 세션, HTTP, 트랜잭션은 제품 구현이다.
// 실제 Google/Apple 로그인이나 실기기 attestation 통과 근거로 사용하지 않는다.
type runtimeKeys struct{ key *rsa.PublicKey }

func (k runtimeKeys) Key(_ context.Context, kid string) (*rsa.PublicKey, error) {
	if kid != "runtime" {
		return nil, fmt.Errorf("unknown fixture key")
	}
	return k.key, nil
}

type runtimeCustomTokens struct{}

func (runtimeCustomTokens) Mint(_ context.Context, _ registry.App, uid string, _ bool) (string, error) {
	return "fixture:" + uid, nil
}

type runtimeAppCheck struct{}

func (runtimeAppCheck) Verify(_ context.Context, token, project string) error {
	if token != "fixture-attestation" || project != "demo-platform-service-e2e" {
		return fmt.Errorf("fixture attestation rejected")
	}
	return nil
}

type runtimeFixture struct {
	t           *testing.T
	store       *store.Client
	app         registry.App
	key         *rsa.PrivateKey
	provider    *oidc.Verifier
	providerURL string
	secret      []byte
	server      *httptest.Server
	users       *identity.StoreRepository
	identity    *identity.Handler
	registry    *registry.Registry
	config      *remoteconfig.Service
	register    func(*http.ServeMux)
}

func newRuntimeFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	host, _, err := net.SplitHostPort(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("service runtime requires a loopback Firestore emulator; production fallback forbidden")
	}
	st, err := store.New(t.Context(), "demo-platform-service-e2e", "runtime_"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "runtime", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	t.Cleanup(jwks.Close)
	provider, err := oidc.New(oidc.Config{Provider: "google", Issuer: jwks.URL, JWKSURL: jwks.URL, Client: jwks.Client()})
	if err != nil {
		t.Fatal(err)
	}
	app := registry.App{AppID: "runtime-fixture", DisplayName: "Service runtime fixture", FirebaseProjectID: "demo-platform-service-e2e", FirebaseCustomTokenServiceAccount: "platform-auth@demo-platform-service-e2e.iam.gserviceaccount.com", Status: registry.StatusActive, Features: map[string]bool{"config": true, "firebase_custom_token_bridge": true, "account_deletion": true, "inbox": true}, GA4: registry.GA4Config{PropertyID: "123456"}, Auth: registry.AuthConfig{RequireAccountLinkAppCheck: true, AccountProviders: map[string]registry.AuthProviderConfig{"google": {Audience: "runtime-audience"}}}}
	if err = app.Validate(); err != nil {
		t.Fatal(err)
	}
	p, _ := fspath.Parse("apps/" + app.AppID)
	if err = st.Set(t.Context(), p, app); err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	f := &runtimeFixture{t: t, store: st, app: app, key: key, provider: provider, providerURL: jwks.URL, secret: secret}
	f.start()
	t.Cleanup(func() {
		if f.server != nil {
			f.server.Close()
		}
	})
	return f
}

func (f *runtimeFixture) start() {
	f.t.Helper()
	f.registry = registry.New(registry.NewStoreSource(f.store))
	f.config = remoteconfig.NewService(f.store)
	f.users = identity.NewStoreRepository(f.store)
	f.config.WithAppVersions(f.users)
	blocks := blocklist.NewService(blocklist.NewStoreSource(f.store))
	issuer, err := identity.NewSessionIssuer(f.secret, time.Hour)
	if err != nil {
		f.t.Fatal(err)
	}
	svc := identity.NewService(f.registry, identity.NewFirebaseVerifier(runtimeKeys{&f.key.PublicKey}, blocks), f.users, issuer, blocks).
		WithCustomTokenIssuer(runtimeCustomTokens{}).WithAppCheckVerifier(runtimeAppCheck{}).WithAccountDeletions(f.users).WithConfigOverlay(f.config)
	if err = svc.ConfigureAccountProviders(f.users, f.provider); err != nil {
		f.t.Fatal(err)
	}
	f.identity = identity.NewHandler(svc)
	mux := http.NewServeMux()
	f.identity.Register(mux)
	remoteconfig.NewHandler(f.config, f.registry).Register(mux)
	inbox.NewHandler(f.identity, f.registry, func(app registry.App) inbox.Mailbox { return ledger.NewForApp(f.store, domain.EnvSandbox, app.AppID) }).Register(mux)
	if f.register != nil {
		f.register(mux)
	}
	f.server = httptest.NewServer(mux)
	f.server.Client().Timeout = 15 * time.Second
}

func (f *runtimeFixture) restart() { f.server.Close(); f.start() }

func (f *runtimeFixture) signed(uid, nonce string, provider bool) string {
	f.t.Helper()
	now := time.Now()
	issuer, audience := f.app.Issuer(), f.app.Audience()
	if provider {
		issuer, audience = f.providerURL, "runtime-audience"
	}
	claims := jwt.MapClaims{"iss": issuer, "aud": audience, "sub": uid, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "auth_time": now.Unix(), "firebase": map[string]any{"sign_in_provider": "anonymous"}, "nonce": nonce}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "runtime"
	value, err := token.SignedString(f.key)
	if err != nil {
		f.t.Fatal(err)
	}
	return value
}

func (f *runtimeFixture) request(method, path, token string, body any, want int) map[string]any {
	f.t.Helper()
	var data io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		data = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, f.server.URL+path, data)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Seori-App", f.app.AppID)
	req.Header.Set("X-Seori-Build", "debug")
	req.Header.Set("X-Firebase-AppCheck", "fixture-attestation")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := f.server.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var envelope map[string]any
	if err = json.NewDecoder(response.Body).Decode(&envelope); err != nil && err != io.EOF && strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		f.t.Fatalf("decode %s: %v", path, err)
	}
	if response.StatusCode != want {
		f.t.Fatalf("%s %s status=%d want=%d error=%v", method, path, response.StatusCode, want, envelope["error"])
	}
	result, _ := envelope["result"].(map[string]any)
	return result
}

func (f *runtimeFixture) session(uid string) map[string]any {
	return f.request("POST", "/v1/auth/session", "", map[string]any{"appId": f.app.AppID, "credential": map[string]string{"kind": "firebase-id-token", "value": f.signed(uid, "", false)}}, 200)
}

func TestServiceRuntimeAccountRecoveryAndInbox(t *testing.T) {
	f := newRuntimeFixture(t)
	bridge := f.request("POST", "/v1/auth/firebase-custom-token", "", map[string]string{"appId": f.app.AppID, "existingFirebaseIdToken": f.signed("legacy-user", "", false)}, 200)
	if bridge["appUserId"] != "legacy-user" {
		t.Fatal("bridge must preserve existing Firebase UID")
	}
	guest := f.request("POST", "/v1/auth/firebase-custom-token", "", map[string]string{"appId": f.app.AppID}, 200)
	if guest["appUserId"] == "" || guest["appUserId"] == "legacy-user" {
		t.Fatal("new bridge must create isolated guest")
	}
	target := f.session("target")
	targetToken := target["platformToken"].(string)
	challenge := f.request("POST", "/v1/auth/account-link-challenges", targetToken, map[string]string{"provider": "google"}, 201)
	nonce := challenge["nonce"].(string)
	linked := f.request("POST", "/v1/auth/account-links", targetToken, map[string]string{"provider": "google", "idToken": f.signed("provider-subject", nonce, true), "nonce": nonce}, 200)
	linkedSession := linked["session"].(map[string]any)
	if linkedSession["appUserId"] != "target" || linkedSession["isLinkedAccount"] != true {
		t.Fatal("link must preserve target UID")
	}
	source := f.session("interrupted-source")
	sourceToken := source["platformToken"].(string)
	recovery := f.request("POST", "/v1/auth/account-link-recovery-challenges", sourceToken, map[string]string{"provider": "google", "expectedAppUserId": "target"}, 201)
	proof := map[string]string{"provider": "google", "idToken": f.signed("provider-subject", recovery["nonce"].(string), true), "nonce": recovery["nonce"].(string), "expectedAppUserId": "target"}
	f.restart()
	// SDK14가 복구하는 중단 지점: challenge 저장 후 프로세스를 재시작한다.
	recovered := f.request("POST", "/v1/auth/account-link-recoveries", sourceToken, proof, 200)
	result := recovered["session"].(map[string]any)
	if result["appUserId"] != "target" || result["platformUserId"] != target["platformUserId"] {
		t.Fatal("recovery must adopt the established target")
	}
	f.request("POST", "/v1/auth/account-link-recoveries", sourceToken, proof, 200)
	wrong := map[string]string{"provider": "google", "idToken": proof["idToken"], "nonce": proof["nonce"], "expectedAppUserId": "other"}
	f.request("POST", "/v1/auth/account-link-recoveries", sourceToken, wrong, 401)
	f.request("POST", "/v1/auth/account-link-recoveries", "", proof, 401)
	f.request("POST", "/v1/auth/refresh", "", map[string]string{"refreshToken": result["refreshToken"].(string)}, 200)
	led := ledger.NewForApp(f.store, domain.EnvSandbox, f.app.AppID)
	message, err := led.IssueInbox(t.Context(), ledger.InboxIssue{RequestID: "runtime-welcome", PlatformUserID: target["platformUserId"].(string), Title: "Runtime", Body: "Runtime fixture", Reason: ledger.AdminReasonCustomerSupportCompensation, Actor: "runtime-review", Rewards: []ledger.InboxReward{{Kind: "entitlement", EntitlementID: "starter", Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	adoptedToken := result["platformToken"].(string)
	f.request("GET", "/v1/inbox", adoptedToken, nil, 200)
	f.request("POST", "/v1/inbox/"+message.ID+"/read", adoptedToken, struct{}{}, 200)
	f.request("POST", "/v1/inbox/"+message.ID+"/claim", adoptedToken, struct{}{}, 200)
	f.restart()
	f.request("POST", "/v1/inbox/"+message.ID+"/claim", adoptedToken, struct{}{}, 200)
	active, err := ledger.NewForApp(f.store, domain.EnvSandbox, f.app.AppID).ListActive(t.Context(), target["platformUserId"].(string))
	if err != nil || len(active) != 1 || active[0] != "starter" {
		t.Fatal("inbox reward must survive restart without duplicate grant")
	}
	t.Log("runtime journey: signed login → link → persisted recovery challenge → restart → adopt/replay/reject → refresh → inbox/read/claim → restart; external IAM/App Check are fixture boundaries")
}
