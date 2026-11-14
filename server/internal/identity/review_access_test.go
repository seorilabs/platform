package identity

import (
	"context"
	"errors"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReviewAccessDisabledByDefault(t *testing.T) {
	svc := newBridgeTestService(t, fakeVerifier{}, newMemRepo(), &fakeCustomTokenIssuer{token: "review-token"})
	mux := http.NewServeMux()
	NewHandler(svc).Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/review-access", strings.NewReader(`{"appId":"lizard-tycoon","code":"NOT-A-REAL-REVIEW-CODE"}`))
	request.Header.Set(AppHeader, "lizard-tycoon")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("disabled review access: status=%d, want403", response.Code)
	}
}

type fakeReviewRepository struct {
	allowed bool
	uid     string
	calls   int
	err     error
}

func (f *fakeReviewRepository) AllowReviewAttempt(context.Context, string, time.Time) (bool, error) {
	f.calls++
	return f.allowed, f.err
}
func (f *fakeReviewRepository) ResolveReviewUID(_ context.Context, _ string, candidate string) (string, error) {
	if f.uid == "" {
		f.uid = candidate
	}
	return f.uid, f.err
}
func reviewTestService(t *testing.T) (*Service, *fakeReviewRepository, *fakeCustomTokenIssuer) {
	t.Helper()
	svc := newBridgeTestService(t, fakeVerifier{}, newMemRepo(), nil)
	app := testApp()
	app.Features = map[string]bool{"firebase_custom_token_bridge": true, "account_deletion": true}
	app.FirebaseCustomTokenServiceAccount = "platform-auth@lizard-tycoon.iam.gserviceaccount.com"
	app.GA4.PropertyID = "123456789"
	app.Auth.ReviewAccessCodeSHA256 = hashHex("TESTONLYDEMOACCESSCODE")
	app.Auth.RequireAccountLinkAppCheck = true
	app.Auth.AccountProviders = map[string]registry.AuthProviderConfig{"google": {Audience: "test-client"}}
	svc.registry = registry.New(fakeSource{apps: []registry.App{app}})
	repo := &fakeReviewRepository{allowed: true}
	tokens := &fakeCustomTokenIssuer{token: "not-a-real-firebase-token"}
	svc.WithCustomTokenIssuer(tokens).WithReviewAccessRepository(repo).WithAppCheckVerifier(&fakeAppCheckVerifier{})
	return svc, repo, tokens
}
func TestReviewAccessCodeAndBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, code, attestation string
		configure               func(*Service, *fakeReviewRepository)
		want                    platformerr.Code
	}{
		{"valid normalized", " test-only demo access code ", "test-attestation", nil, ""},
		{"wrong code", "WRONG", "test-attestation", nil, platformerr.CodeAuthInvalid},
		{"empty code", "", "test-attestation", nil, platformerr.CodeAuthInvalid},
		{"oversized code", strings.Repeat("X", 129), "test-attestation", nil, platformerr.CodeAuthInvalid},
		{"missing attestation", "TESTONLYDEMOACCESSCODE", "", nil, platformerr.CodeAppCheckRequired},
		{"rate limit", "TESTONLYDEMOACCESSCODE", "test-attestation", func(_ *Service, r *fakeReviewRepository) { r.allowed = false }, platformerr.CodeRateLimited},
		{"storage failure", "TESTONLYDEMOACCESSCODE", "test-attestation", func(_ *Service, r *fakeReviewRepository) { r.err = errors.New("offline") }, platformerr.CodeInternal},
		{"wrong UID from storage", "TESTONLYDEMOACCESSCODE", "test-attestation", func(_ *Service, r *fakeReviewRepository) { r.uid = "personal-user" }, platformerr.CodeLedgerStateInvalid},
		{"missing repository", "TESTONLYDEMOACCESSCODE", "test-attestation", func(s *Service, _ *fakeReviewRepository) { s.reviewAccess = nil }, platformerr.CodePlatformUnavailable},
		{"missing signer", "TESTONLYDEMOACCESSCODE", "test-attestation", func(s *Service, _ *fakeReviewRepository) { s.customTokens = nil }, platformerr.CodePlatformUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, tokens := reviewTestService(t)
			if tc.configure != nil {
				tc.configure(s, r)
			}
			result, err := s.CreateReviewAccess(t.Context(), "lizard-tycoon", tc.code, tc.attestation)
			if tc.name == "storage failure" {
				if err == nil {
					t.Fatal("storage failure allowed")
				}
				return
			}
			if platformerr.CodeOf(err) != tc.want {
				t.Fatalf("code=%s want=%s err=%v", platformerr.CodeOf(err), tc.want, err)
			}
			if err == nil {
				if !isReviewUID("lizard-tycoon", result.AppUserID) || tokens.uid != result.AppUserID || tokens.guest {
					t.Fatal("not isolated non-guest review identity")
				}
				again, e := s.CreateReviewAccess(t.Context(), "lizard-tycoon", tc.code, tc.attestation)
				if e != nil || again.AppUserID != result.AppUserID {
					t.Fatal("reusable code changed identity")
				}
			} else if tokens.uid != "" {
				t.Fatal("rejected request reached IAM signer")
			}
		})
	}
}
func TestReviewSessionRequiresSignedAppClaims(t *testing.T) {
	guest := false
	uid := reviewPrefix("lizard-tycoon") + strings.Repeat("a", 64)
	good := Claims{UID: uid, SignInProvider: "custom", PlatformAppID: "lizard-tycoon", PlatformGuest: &guest}
	for _, tc := range []struct {
		name    string
		change  func(*Claims)
		allowed bool
	}{
		{"signed review", func(*Claims) {}, true},
		{"prefix only", func(c *Claims) { c.PlatformGuest = nil }, false},
		{"wrong app", func(c *Claims) { c.PlatformAppID = "other-app" }, false},
		{"guest custom token", func(c *Claims) { v := true; c.PlatformGuest = &v }, false},
		{"Google provider prefix", func(c *Claims) { c.SignInProvider = "google.com" }, false},
		{"wrong UID namespace", func(c *Claims) { c.UID = reviewPrefix("other-app") + strings.Repeat("a", 64) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := reviewTestService(t)
			claims := good
			tc.change(&claims)
			s.verifier = sessionBridgeVerifier{claims}
			res, err := s.CreateSession(t.Context(), "lizard-tycoon", Credential{Kind: KindFirebaseIDToken, Value: "test-id-token"}, ClientInfo{})
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
			if !tc.allowed {
				return
			}
			if !res.IsLinkedAccount || res.IsAnonymous {
				t.Fatal("review session cannot use its own cloud account")
			}
			for i := 0; i < 2; i++ {
				next, e := s.Refresh(t.Context(), "lizard-tycoon", res.RefreshToken, ClientInfo{})
				if e != nil || !next.IsLinkedAccount {
					t.Fatalf("review refresh: %v", e)
				}
				res = next
			}
			sess, e := s.Authenticate(t.Context(), "lizard-tycoon", res.PlatformToken)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.BeginAccountLink(t.Context(), sess, "google"); platformerr.CodeOf(e) != platformerr.CodeAuthForbidden {
				t.Fatal("review linked to personal provider")
			}
			if _, e = s.BeginAccountLinkRecovery(t.Context(), sess, "google", "personal-user"); platformerr.CodeOf(e) != platformerr.CodeAuthForbidden {
				t.Fatal("review recovered personal account")
			}
		})
	}
}
func TestReviewHTTPRejectsChosenUIDAndAppMismatch(t *testing.T) {
	for _, body := range []string{`{"code":"TESTONLYDEMOACCESSCODE"}`, `{"appId":"lizard-tycoon","code":"TESTONLYDEMOACCESSCODE","uid":"personal-user"}`, `{"appId":"other-app","code":"TESTONLYDEMOACCESSCODE"}`} {
		s, _, tokens := reviewTestService(t)
		mux := http.NewServeMux()
		NewHandler(s).Register(mux)
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/review-access", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(AppHeader, "lizard-tycoon")
		req.Header.Set("X-Firebase-AppCheck", "test-attestation")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != http.StatusBadRequest || tokens.uid != "" {
			t.Fatalf("invalid body reached signer, status%d", response.Code)
		}
	}
}

func TestWrongReviewCodeDoesNotConsumeValidAccessBudget(t *testing.T) {
	s, repo, _ := reviewTestService(t)
	repo.allowed = false
	_, err := s.CreateReviewAccess(t.Context(), "lizard-tycoon", "WRONG", "test-attestation")
	if platformerr.CodeOf(err) != platformerr.CodeAuthInvalid || repo.calls != 0 {
		t.Fatalf("wrong code consumed valid access budget: code=%s calls=%d", platformerr.CodeOf(err), repo.calls)
	}
}
