package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func (m *memoryAccountRepo) CreateAccountLinkRecoveryChallenge(_ context.Context, appID, puid, provider, nonce, expectedUID string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.challenges[nonce] = memoryChallenge{appID: appID, puid: puid, provider: provider, expiresAt: expiresAt, purpose: "recovery", expectedUID: expectedUID}
	return nil
}

func TestAccountRecoveryRejectsWithoutMintOrMappingMutation(t *testing.T) {
	for _, name := range []string{"wrong target", "wrong proof", "missing mapping", "linked source", "target unlinked", "expired", "normal purpose", "target blocked", "source blocked", "anonymous", "bad uid", "multibyte uid"} {
		t.Run(name, func(t *testing.T) {
			accounts := newMemoryAccountRepo()
			provider := &fakeAccountProvider{name: "kakao", subject: "subject"}
			tokens := &fakeCustomTokenIssuer{token: "custom-token"}
			svc := newAccountTestService(t, accounts, provider, tokens)
			sess := Session{AppID: "lizard-tycoon", AppUserID: "source", PlatformUserID: "pu_source"}
			accounts.users[sess.PlatformUserID] = "source"
			accounts.linked["pu_target"] = true
			accounts.mappings["lizard-tycoon\x00kakao\x00subject"] = ConnectedAccount{PlatformUserID: "pu_target", AppUserID: "target", Provider: "kakao"}
			c, err := svc.BeginAccountLinkRecovery(context.Background(), sess, "kakao", "target")
			if err != nil {
				t.Fatal(err)
			}
			expected := "target"
			switch name {
			case "wrong target":
				expected = "other"
			case "wrong proof":
				provider.subject = "other"
			case "missing mapping":
				delete(accounts.mappings, "lizard-tycoon\x00kakao\x00subject")
			case "linked source":
				accounts.linked[sess.PlatformUserID] = true
			case "target unlinked":
				accounts.linked["pu_target"] = false
			case "expired":
				v := accounts.challenges[c.Nonce]
				v.expiresAt = svc.now()
				accounts.challenges[c.Nonce] = v
			case "normal purpose":
				normal, e := svc.BeginAccountLink(context.Background(), sess, "kakao")
				if e != nil {
					t.Fatal(e)
				}
				c = normal
			case "target blocked":
				svc.blocklist = blocking("target")
			case "source blocked":
				svc.blocklist = blocking("source")
			case "anonymous":
				sess.IsAnonymous = true
				sess.AppUserID = "anon:client-chosen"
			case "bad uid":
				expected = "../target"
			case "multibyte uid":
				expected = strings.Repeat("가", 43)
			}
			before := map[string]ConnectedAccount{}
			for k, v := range accounts.mappings {
				before[k] = v
			}
			if _, err = svc.RecoverAccountLink(context.Background(), sess, "kakao", "fresh-proof", c.Nonce, expected); err == nil {
				t.Fatal("AR-3/7: invalid recovery accepted")
			}
			if tokens.uid != "" || !reflect.DeepEqual(before, accounts.mappings) {
				t.Fatal("AR-3/7: rejection minted or moved identity")
			}
		})
	}
}

func TestAccountRecoverySameTargetAndReplay(t *testing.T) {
	a := newMemoryAccountRepo()
	p := &fakeAccountProvider{name: "kakao", subject: "subject"}
	tokens := &fakeCustomTokenIssuer{token: "custom-token"}
	svc := newAccountTestService(t, a, p, tokens)
	sess := Session{AppID: "lizard-tycoon", AppUserID: "target", PlatformUserID: "pu_target", IsLinkedAccount: true}
	a.users["pu_target"] = "target"
	a.linked["pu_target"] = true
	a.mappings["lizard-tycoon\x00kakao\x00subject"] = ConnectedAccount{PlatformUserID: "pu_target", AppUserID: "target", Provider: "kakao"}
	c, err := svc.BeginAccountLinkRecovery(context.Background(), sess, "kakao", "target")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r, e := svc.RecoverAccountLink(context.Background(), sess, "kakao", "proof", c.Nonce, "target")
		if e != nil || r.Restored || r.Session.AppUserID != "target" || !r.Session.IsLinkedAccount {
			t.Fatalf("AR-2/7: same target replay failed: %v", e)
		}
	}
	if _, e := svc.CompleteAccountLink(context.Background(), sess, "kakao", "proof", c.Nonce); e == nil {
		t.Fatal("AR-7: recovery proof accepted by normal link")
	}
	if err := a.DisconnectAccount(context.Background(), sess.AppID, "kakao", "subject"); err != nil {
		t.Fatal(err)
	}
	tokens.uid = ""
	if _, e := svc.RecoverAccountLink(context.Background(), sess, "kakao", "proof", c.Nonce, "target"); e == nil || tokens.uid != "" {
		t.Fatal("AR-7: consumed challenge resurrected unlinked account")
	}
}

func (m *memoryAccountRepo) RecoverAccount(_ context.Context, appID, currentPUID, provider, subject, nonce, expectedUID string, now time.Time) (ConnectedAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.challenges[nonce]
	if !ok || c.purpose != "recovery" || c.appID != appID || c.puid != currentPUID || c.provider != provider || c.expectedUID != expectedUID || !now.Before(c.expiresAt) {
		return ConnectedAccount{}, platformerr.New(platformerr.CodeAuthInvalid, "잘못된 복구 challenge")
	}
	target, ok := m.mappings[appID+"\x00"+provider+"\x00"+subject]
	if !ok || target.AppUserID != expectedUID || !m.linked[target.PlatformUserID] || (m.linked[currentPUID] && currentPUID != target.PlatformUserID) || (c.consumed != nil && (c.subject != subject || c.consumed.PlatformUserID != target.PlatformUserID)) {
		return ConnectedAccount{}, platformerr.New(platformerr.CodeAccountLinkConflict, "다른 계정")
	}
	target.Restored = target.PlatformUserID != currentPUID
	c.consumed = &target
	c.subject = subject
	m.challenges[nonce] = c
	return target, nil
}

type accountRecoveryService interface {
	BeginAccountLinkRecovery(context.Context, Session, string, string) (AccountLinkChallenge, error)
	RecoverAccountLink(context.Context, Session, string, string, string, string) (AccountLinkResult, error)
}

func TestAccountRecoveryPublicContract(t *testing.T) {
	accounts := newMemoryAccountRepo()
	service := newAccountTestService(t, accounts,
		&fakeAccountProvider{name: "kakao", subject: "provider-subject"},
		&fakeCustomTokenIssuer{token: "firebase-custom-token"})
	if _, ok := any(service).(accountRecoveryService); !ok {
		t.Fatal("AR-1: interrupted adoption cannot obtain a verified recovery result")
	}
}

func TestAccountRecoveryHTTPContract(t *testing.T) {
	accounts := newMemoryAccountRepo()
	service := newAccountTestService(t, accounts,
		&fakeAccountProvider{name: "kakao", subject: "provider-subject"},
		&fakeCustomTokenIssuer{token: "firebase-custom-token"})
	service = service.WithAppCheckVerifier(&fakeAppCheckVerifier{})
	guest, err := service.CreateSession(context.Background(), "lizard-tycoon",
		Credential{Kind: KindFirebaseIDToken, Value: "source-uid"}, ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	accounts.users[guest.PlatformUserID] = guest.AppUserID
	accounts.users["pu_existing"] = "target-uid"
	accounts.linked["pu_existing"] = true
	accounts.mappings["lizard-tycoon\x00kakao\x00provider-subject"] = ConnectedAccount{
		PlatformUserID: "pu_existing", AppUserID: "target-uid", Provider: "kakao"}
	mux := http.NewServeMux()
	NewHandler(service).Register(mux)
	request := func(path string, body any) *httptest.ResponseRecorder {
		encoded, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
		setAccountLinkHeaders(req, guest.PlatformToken)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	challenge := request("/v1/auth/account-link-recovery-challenges", map[string]string{
		"provider": "kakao", "expectedAppUserId": "target-uid"})
	if challenge.Code != http.StatusCreated {
		t.Fatalf("AR-1: recovery challenge status=%d, want 201", challenge.Code)
	}
	var envelope struct {
		Result AccountLinkChallenge `json:"result"`
	}
	if err := json.Unmarshal(challenge.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	result := request("/v1/auth/account-link-recoveries", map[string]string{
		"provider": "kakao", "expectedAppUserId": "target-uid", "nonce": envelope.Result.Nonce, "idToken": "fresh-provider-proof"})
	if result.Code != http.StatusOK {
		t.Fatalf("recovery status=%d, want 200", result.Code)
	}
	var recovered struct {
		Result accountLinkResponse `json:"result"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Result.Session.AppUserID != "target-uid" || !recovered.Result.Session.IsLinkedAccount || recovered.Result.Session.IsAnonymous || !recovered.Result.Restored {
		t.Fatal("AR-1: response is not the verified canonical target")
	}
}

func TestAccountRecoveryHTTPAttestationAndAuthentication(t *testing.T) {
	accounts := newMemoryAccountRepo()
	provider := &fakeAccountProvider{name: "kakao", subject: "subject"}
	tokens := &fakeCustomTokenIssuer{token: "custom-token"}
	svc := newAccountTestService(t, accounts, provider, tokens)
	app := testApp()
	app.Auth.AccountProviders = map[string]registry.AuthProviderConfig{"kakao": {Audience: "kakao-client-id"}}
	app.Auth.RequireAccountLinkAppCheck = true
	app.Features = map[string]bool{"firebase_custom_token_bridge": true}
	app.FirebaseCustomTokenServiceAccount = "platform-auth@lizard-tycoon.iam.gserviceaccount.com"
	svc.registry = registry.New(fakeSource{apps: []registry.App{app}})
	svc.WithAppCheckVerifier(&fakeAppCheckVerifier{})
	guest, err := svc.CreateSession(context.Background(), app.AppID, Credential{Kind: KindFirebaseIDToken, Value: "source"}, ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(svc).Register(mux)
	for _, path := range []string{"/v1/auth/account-link-recovery-challenges", "/v1/auth/account-link-recoveries"} {
		for _, name := range []string{"missing token", "wrong app", "missing attestation", "unknown field"} {
			t.Run(path+"/"+name, func(t *testing.T) {
				body := map[string]string{"provider": "kakao", "expectedAppUserId": "target"}
				if path == "/v1/auth/account-link-recoveries" {
					body["nonce"] = "nonce"
					body["idToken"] = "proof"
				}
				if name == "unknown field" {
					body["recoveryOnly"] = "true"
				}
				encoded, _ := json.Marshal(body)
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
				setAccountLinkHeaders(req, guest.PlatformToken)
				switch name {
				case "missing token":
					req.Header.Del("Authorization")
				case "wrong app":
					req.Header.Set(AppHeader, "other-app")
				case "missing attestation":
					req.Header.Del("X-Firebase-AppCheck")
				}
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, req)
				if response.Code < 400 || tokens.uid != "" || provider.token != "" {
					t.Fatal("AR-3: unauthenticated request reached proof/mint")
				}
			})
		}
	}
}
