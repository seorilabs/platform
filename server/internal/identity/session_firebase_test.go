package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/registry"
)

func TestSessionFirebaseBridgeUsesOnlyAuthenticatedOwner(t *testing.T) {
	tokens := &fakeCustomTokenIssuer{token: "custom-for-owner"}
	svc := newBridgeTestService(t, fakeVerifier{}, newMemRepo(), tokens)
	other := testApp()
	other.AppID = "other-app"
	original := testApp()
	original.Features = map[string]bool{"firebase_custom_token_bridge": true}
	original.FirebaseCustomTokenServiceAccount = "platform-auth@lizard-tycoon.iam.gserviceaccount.com"
	svc.registry = registry.New(fakeSource{apps: []registry.App{original, other}})
	session, err := svc.CreateSession(t.Context(), "lizard-tycoon", Credential{Kind: KindFirebaseIDToken, Value: "verified-owner"}, ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(svc).Register(mux)
	for _, tc := range []struct {
		name, token, app, body string
		status                 int
	}{
		{"owner", session.PlatformToken, "lizard-tycoon", "{}", http.StatusOK},
		{"missing body", session.PlatformToken, "lizard-tycoon", "", http.StatusBadRequest},
		{"missing bearer", "", "lizard-tycoon", "", http.StatusUnauthorized},
		{"other app", session.PlatformToken, "other-app", "", http.StatusUnauthorized},
		{"chosen uid", session.PlatformToken, "lizard-tycoon", `{"uid":"other-owner"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/auth/session/firebase-custom-token", strings.NewReader(tc.body))
			r.Header.Set(AppHeader, tc.app)
			r.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	if tokens.uid != "verified-owner" {
		t.Fatalf("minted owner=%q", tokens.uid)
	}
}

type sessionBridgeVerifier struct{ claims Claims }

func (v sessionBridgeVerifier) Verify(context.Context, string, registry.App) (Claims, error) {
	return v.claims, nil
}

func TestFirebaseAITIdentityRequiresVerifiedPlatformClaims(t *testing.T) {
	guest := false
	good := Claims{UID: "ait:" + strings.Repeat("a", 64), SignInProvider: "custom", PlatformAppID: "lizard-tycoon", PlatformGuest: &guest}
	for _, tc := range []struct {
		name   string
		change func(*Claims)
		want   string
	}{
		{"signed AIT", func(*Claims) {}, "apps_in_toss"},
		{"app not opted in", func(*Claims) {}, "firebase"},
		{"prefix alone", func(c *Claims) { c.PlatformGuest = nil }, "firebase"},
		{"other app claim", func(c *Claims) { c.PlatformAppID = "other" }, "firebase"},
		{"password provider", func(c *Claims) { c.SignInProvider = "password" }, "firebase"},
		{"non AIT uid", func(c *Claims) { c.UID = "regular-firebase-owner" }, "firebase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := good
			tc.change(&claims)
			svc := newTestService(t, sessionBridgeVerifier{claims}, newMemRepo())
			app := aitFirebaseTestApp()
			if tc.name == "app not opted in" {
				app.Features["ait_firebase_identity"] = false
			}
			who, err := svc.resolveIdentity(t.Context(), app, Credential{Kind: KindFirebaseIDToken, Value: "firebase-signed-token"})
			if err != nil {
				t.Fatal(err)
			}
			if who.UID != claims.UID || who.AuthType != tc.want {
				t.Fatalf("identity=%+v", who)
			}
		})
	}
}

func TestSessionFirebaseBridgeRejectsClientChosenAnonymousIdentity(t *testing.T) {
	tokens := &fakeCustomTokenIssuer{token: "must-not-mint"}
	svc := newBridgeTestService(t, fakeVerifier{}, newMemRepo(), tokens)
	_, err := svc.CreateSessionFirebaseCustomToken(t.Context(), Session{AppID: "lizard-tycoon", AppUserID: "anon:chosen", IsAnonymous: true})
	if err == nil || tokens.uid != "" {
		t.Fatal("client chosen anonymous identity was minted")
	}
}

func TestAITSessionRefreshPreservesVerifiedLinkedIdentity(t *testing.T) {
	guest := false
	claims := Claims{UID: "ait:" + strings.Repeat("b", 64), SignInProvider: "custom", PlatformAppID: "lizard-tycoon", PlatformGuest: &guest}
	accounts := newMemoryAccountRepo()
	svc := newTestService(t, sessionBridgeVerifier{claims}, newMemRepo()).WithAccountLinkRepository(accounts)
	svc.registry = registry.New(fakeSource{apps: []registry.App{aitFirebaseTestApp()}})
	first, err := svc.CreateSession(t.Context(), "lizard-tycoon", Credential{Kind: KindFirebaseIDToken, Value: "verified-firebase-token"}, ClientInfo{})
	if err != nil || !first.IsLinkedAccount {
		t.Fatalf("initial linked=%v err=%v", first.IsLinkedAccount, err)
	}
	next, err := svc.Refresh(t.Context(), "lizard-tycoon", first.RefreshToken, ClientInfo{})
	if err != nil || !next.IsLinkedAccount || next.AppUserID != claims.UID || next.PlatformUserID != first.PlatformUserID {
		t.Fatalf("refreshed identity changed: err=%v linked=%v", err, next.IsLinkedAccount)
	}
}

func aitFirebaseTestApp() registry.App {
	app := testApp()
	app.Features = map[string]bool{"firebase_custom_token_bridge": true, "ait_firebase_identity": true}
	app.FirebaseCustomTokenServiceAccount = "platform-auth@" + app.FirebaseProjectID + ".iam.gserviceaccount.com"
	return app
}

func TestAITFirebaseDeletionUsesOnlyVerifiedCanonicalOwner(t *testing.T) {
	guest := false
	good := Claims{UID: "ait:" + strings.Repeat("c", 64), SignInProvider: "custom", PlatformAppID: "lizard-tycoon", PlatformGuest: &guest}
	for _, valid := range []bool{true, false} {
		claims := good
		if !valid {
			claims.PlatformAppID = "other-app"
		}
		repo := &deletionMemory{status: DeletionStatus{State: "processing"}}
		svc := deletionService(t, sessionBridgeVerifier{claims}, repo)
		app := deletionTestApp()
		app.Features["ait_firebase_identity"] = true
		svc.registry = registry.New(fakeSource{apps: []registry.App{app}})
		_, err := svc.RequestAccountDeletion(t.Context(), app.AppID, "firebase-signed-token", strings.Repeat("d", 64), nil)
		if valid && (err != nil || repo.uid != good.UID) {
			t.Fatalf("canonical owner not queued: %v", err)
		}
		if !valid && (err == nil || repo.begun) {
			t.Fatal("unverified AIT owner was queued for deletion")
		}
	}
}
