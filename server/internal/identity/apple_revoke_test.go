package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type fakeAppleRevoker struct {
	grant     AppleGrant
	err       error
	revoked   []string
	exchanged []string
}

func (f *fakeAppleRevoker) Configured(string) bool { return true }
func (f *fakeAppleRevoker) Exchange(_ context.Context, _ registry.App, code string) (AppleGrant, error) {
	f.exchanged = append(f.exchanged, code)
	return f.grant, f.err
}
func (f *fakeAppleRevoker) Revoke(_ context.Context, _ registry.App, token string) error {
	f.revoked = append(f.revoked, token)
	return nil
}

func TestAppleLinkedDeletionRequiresReauthorizationAndRevokesFirst(t *testing.T) {
	ctx := context.Background()
	receipt := strings.Repeat("b", 64)
	repo := &deletionMemory{linked: map[string]string{"apple": hashHex("apple-sub"), "google": hashHex("g")}}
	revoker := &fakeAppleRevoker{grant: AppleGrant{Subject: "apple-sub", RefreshToken: "rt"}}
	s := deletionService(t, fakeVerifier{}, repo).WithAppleRevoker(revoker)
	app := deletionTestApp().AppID

	if _, err := s.RequestAccountDeletion(ctx, app, "verified-uid", receipt, nil); platformerr.CodeOf(err) != platformerr.CodeAccountReauthRequired || repo.begun {
		t.Fatalf("Apple 연결 계정이 재승인 없이 삭제 접수됐다: %v", err)
	}
	auth := &ProviderAuthorization{Provider: "apple", AuthorizationCode: "code-1"}
	if _, err := s.RequestAccountDeletion(ctx, app, "verified-uid", receipt, auth); err != nil {
		t.Fatal(err)
	}
	if !repo.begun || len(revoker.revoked) != 1 || revoker.revoked[0] != "rt" {
		t.Fatalf("승인 철회 뒤 접수되지 않았다: begun=%v revoked=%v", repo.begun, revoker.revoked)
	}
	// 같은 접수증 재시도는 재승인 없이 현재 상태를 돌려준다.
	if _, err := s.RequestAccountDeletion(ctx, app, "verified-uid", receipt, nil); err != nil || len(revoker.exchanged) != 1 {
		t.Fatalf("접수 재시도가 다시 Apple 교환을 요구했다: %v %v", err, revoker.exchanged)
	}
}

func TestAppleDeletionRejectsAnotherAppleIDButRevokesItsNewGrant(t *testing.T) {
	repo := &deletionMemory{linked: map[string]string{"apple": hashHex("apple-sub")}}
	revoker := &fakeAppleRevoker{grant: AppleGrant{Subject: "someone-else", RefreshToken: "other"}}
	s := deletionService(t, fakeVerifier{}, repo).WithAppleRevoker(revoker)
	_, err := s.RequestAccountDeletion(context.Background(), deletionTestApp().AppID, "verified-uid", strings.Repeat("c", 64),
		&ProviderAuthorization{Provider: "apple", AuthorizationCode: "code"})
	if platformerr.CodeOf(err) != platformerr.CodeAuthForbidden || repo.begun {
		t.Fatalf("다른 Apple ID 로 삭제가 접수됐다: %v", err)
	}
	if len(revoker.revoked) != 1 || revoker.revoked[0] != "other" {
		t.Fatalf("방금 만든 승인을 남겼다: %v", revoker.revoked)
	}
}

func TestGoogleOnlyDeletionNeedsNoProviderStep(t *testing.T) {
	repo := &deletionMemory{linked: map[string]string{"google": hashHex("g")}}
	s := deletionService(t, fakeVerifier{}, repo)
	if _, err := s.RequestAccountDeletion(context.Background(), deletionTestApp().AppID, "verified-uid", strings.Repeat("d", 64), nil); err != nil || !repo.begun {
		t.Fatalf("Google 연결 계정 삭제가 접수되지 않았다: %v", err)
	}
}

func testAppleKeys(t *testing.T) (map[string]AppleSignInKey, *ecdsa.PrivateKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"test-app": map[string]string{
		"clientId": "com.seorilabs.example", "teamId": "TEAM123456", "keyId": "KEY1234567",
		"privateKey": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	}})
	keys, err := ParseAppleSignInKeys(raw)
	if err != nil {
		t.Fatal(err)
	}
	return keys, priv
}

func appleTestIDToken(sub, aud string) string {
	enc := base64.RawURLEncoding
	payload, _ := json.Marshal(map[string]any{"iss": appleAuthBaseURL, "sub": sub, "aud": aud})
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

func TestAppleTokenRevokerExchangesAndRevokesWithSignedClientSecret(t *testing.T) {
	keys, priv := testAppleKeys(t)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_id") != "com.seorilabs.example" {
			t.Fatalf("client_id = %q", r.Form.Get("client_id"))
		}
		secret, err := jwt.Parse(r.Form.Get("client_secret"), func(*jwt.Token) (any, error) { return &priv.PublicKey, nil },
			jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience(appleAuthBaseURL), jwt.WithIssuer("TEAM123456"), jwt.WithSubject("com.seorilabs.example"))
		if err != nil || secret.Header["kid"] != "KEY1234567" {
			t.Fatalf("client_secret 검증 실패: %v", err)
		}
		switch r.URL.Path {
		case "/auth/token":
			if r.Form.Get("code") == "used" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"refresh_token": "rt-1", "id_token": appleTestIDToken("apple-sub", "com.seorilabs.example")})
		case "/auth/revoke":
			if r.Form.Get("token") != "rt-1" || r.Form.Get("token_type_hint") != "refresh_token" {
				t.Fatalf("철회 요청이 다르다: %v", r.Form)
			}
		}
	}))
	defer srv.Close()
	revoker := NewAppleTokenRevoker(keys, srv.Client())
	revoker.baseURL = srv.URL
	app := registry.App{AppID: "test-app", Auth: registry.AuthConfig{AccountProviders: map[string]registry.AuthProviderConfig{"apple": {Audience: "com.seorilabs.example"}}}}
	grant, err := revoker.Exchange(context.Background(), app, "fresh")
	if err != nil || grant.Subject != "apple-sub" || grant.RefreshToken != "rt-1" {
		t.Fatalf("교환 결과가 다르다: %+v %v", grant, err)
	}
	if err := revoker.Revoke(context.Background(), app, grant.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := revoker.Exchange(context.Background(), app, "used"); platformerr.CodeOf(err) != platformerr.CodeAccountReauthRequired {
		t.Fatalf("만료 code 가 재승인 요구로 바뀌지 않았다: %v", err)
	}
	other := app
	other.Auth.AccountProviders = map[string]registry.AuthProviderConfig{"apple": {Audience: "com.other"}}
	if _, err := revoker.Exchange(context.Background(), other, "fresh"); platformerr.CodeOf(err) != platformerr.CodePlatformUnavailable {
		t.Fatalf("레지스트리 audience 와 다른 키를 썼다: %v", err)
	}
	if strings.Join(paths, ",") != "/auth/token,/auth/revoke,/auth/token" {
		t.Fatalf("호출 순서가 다르다: %v", paths)
	}
}
