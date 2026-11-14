//go:build integration

package identity

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/operational"
	"github.com/seorilabs/platform/server/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func reviewHTTPStore(t *testing.T) *store.Client {
	t.Helper()
	host, _, err := net.SplitHostPort(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("owned loopback emulator required")
	}
	st, err := store.New(t.Context(), "demo-review-independent", "ir_"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestReviewHTTPUsesFirestore(t *testing.T) {
	st := reviewHTTPStore(t)
	s, _, _ := reviewTestService(t)
	s.WithReviewAccessRepository(NewStoreRepository(st))
	mux := http.NewServeMux()
	NewHandler(s).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	var first string
	for i := 0; i < 2; i++ {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/auth/review-access", strings.NewReader(`{"appId":"lizard-tycoon","code":"TESTONLYDEMOACCESSCODE"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(AppHeader, "lizard-tycoon")
		req.Header.Set("X-Firebase-AppCheck", "test-attestation")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Result struct {
				AppUserID string `json:"appUserId"`
			} `json:"result"`
		}
		err = json.NewDecoder(response.Body).Decode(&payload)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !isReviewUID("lizard-tycoon", payload.Result.AppUserID) {
			t.Fatalf("actual HTTP/Firestore response invalid: status=%d decode=%v", response.StatusCode, err)
		}
		if i == 0 {
			first = payload.Result.AppUserID
		} else if payload.Result.AppUserID != first {
			t.Fatal("reusable code split review UID")
		}
	}
	path, _ := reviewPath("auth_review_limits", "lizard-tycoon")
	snap, err := st.Get(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	var limit reviewLimitDoc
	if err = snap.DataTo(&limit); err != nil {
		t.Fatal(err)
	}
	if limit.Count != 2 {
		t.Fatalf("HTTP requests did not reach real Firestore rate transaction: count=%d", limit.Count)
	}
	t.Log("actual loopback HTTP and Firestore identity/rate transactions; AppCheck/Firebase/IAM are fixtures")
}

func TestReviewBridgeMustNotEmitOperationalEvents(t *testing.T) {
	st := reviewHTTPStore(t)
	repo := NewStoreRepository(st).WithOperationalEvents(operational.NewRepository(st))
	s, _, _ := reviewTestService(t)
	guest := false
	uid := reviewPrefix("lizard-tycoon") + strings.Repeat("a", 64)
	s.verifier = sessionBridgeVerifier{Claims{UID: uid, SignInProvider: "custom", PlatformAppID: "lizard-tycoon", PlatformGuest: &guest}}
	s.users = repo
	s.WithAppVersionObserver(repo)
	mux := http.NewServeMux()
	NewHandler(s).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/auth/firebase-custom-token", strings.NewReader(`{"appId":"lizard-tycoon","existingFirebaseIdToken":"fixture-signed-review-id"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(AppHeader, "lizard-tycoon")
	req.Header.Set(appVersionHeader, "0.1.0")
	req.Header.Set(runtimeHeader, "review-fixture")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bridge status=%d", response.StatusCode)
	}
	idPath, _ := identityPath("lizard-tycoon", uid)
	snap, err := st.Get(t.Context(), idPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc identityDoc
	if err = snap.DataTo(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.AuthType != "review" {
		t.Errorf("review bridge stored authType=%s, want review", doc.AuthType)
	}
	for _, event := range []struct{ name, id string }{
		{"identity.created", operational.StableEventID("identity", "lizard-tycoon", doc.PlatformUserID)},
		{"app.version.first_seen", operational.StableEventID("app_version", "lizard-tycoon", "review-fixture", "0.1.0")},
	} {
		path, _ := fspath.Parse("operational_event_outbox/" + event.id)
		_, err = st.Get(t.Context(), path)
		if err == nil {
			t.Errorf("review bridge emitted real emulator outbox event %s", event.name)
		} else if status.Code(err) != codes.NotFound {
			t.Fatal(err)
		}
	}
}
