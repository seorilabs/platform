package identity

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type fakeAccountProvider struct {
	name    string
	subject string
	err     error
	token   string
	nonce   string
}

func (f *fakeAccountProvider) Name() string { return f.name }

func (f *fakeAccountProvider) Verify(
	_ context.Context,
	idToken, nonce string,
	_ registry.App,
) (string, error) {
	f.token = idToken
	f.nonce = nonce
	return f.subject, f.err
}

type memoryChallenge struct {
	appID     string
	puid      string
	provider  string
	expiresAt time.Time
	consumed  *ConnectedAccount
}

type memoryAccountRepo struct {
	mu                   sync.Mutex
	challenges           map[string]memoryChallenge
	users                map[string]string
	linked               map[string]bool
	mappings             map[string]ConnectedAccount
	disconnectErr        error
	disconnectedApp      string
	disconnectedProvider string
	disconnectedSubject  string
}

func (m *memoryAccountRepo) DisconnectAccount(
	_ context.Context,
	appID, provider, subject string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnectedApp = appID
	m.disconnectedProvider = provider
	m.disconnectedSubject = subject
	if m.disconnectErr != nil {
		return m.disconnectErr
	}
	key := appID + "\x00" + provider + "\x00" + subject
	mapping, exists := m.mappings[key]
	if !exists {
		return nil
	}
	delete(m.mappings, key)
	linked := false
	for _, remaining := range m.mappings {
		if remaining.PlatformUserID == mapping.PlatformUserID {
			linked = true
			break
		}
	}
	m.linked[mapping.PlatformUserID] = linked
	return nil
}

func newMemoryAccountRepo() *memoryAccountRepo {
	return &memoryAccountRepo{
		challenges: map[string]memoryChallenge{},
		users:      map[string]string{},
		linked:     map[string]bool{},
		mappings:   map[string]ConnectedAccount{},
	}
}

func (m *memoryAccountRepo) CreateAccountLinkChallenge(
	_ context.Context,
	appID, puid, provider, nonce string,
	expiresAt time.Time,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.challenges[nonce] = memoryChallenge{appID: appID, puid: puid, provider: provider, expiresAt: expiresAt}
	return nil
}

func (m *memoryAccountRepo) ConnectAccount(
	_ context.Context,
	appID, currentPUID, provider, subject, nonce string,
	now time.Time,
) (ConnectedAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	challenge, ok := m.challenges[nonce]
	if !ok || challenge.appID != appID || challenge.puid != currentPUID ||
		challenge.provider != provider || now.After(challenge.expiresAt) {
		return ConnectedAccount{}, platformerr.New(platformerr.CodeAuthInvalid, "잘못된 challenge")
	}
	if challenge.consumed != nil {
		return *challenge.consumed, nil
	}
	key := appID + "\x00" + provider + "\x00" + subject
	result, exists := m.mappings[key]
	if !exists {
		result = ConnectedAccount{
			PlatformUserID: currentPUID, AppUserID: m.users[currentPUID], Provider: provider,
		}
		m.mappings[key] = result
	}
	result.Restored = result.PlatformUserID != currentPUID
	m.linked[result.PlatformUserID] = true
	challenge.consumed = &result
	m.challenges[nonce] = challenge
	return result, nil
}

func (m *memoryAccountRepo) IsAccountLinked(
	_ context.Context,
	_, puid string,
) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.linked[puid], nil
}

func newAccountTestService(
	t *testing.T,
	accounts *memoryAccountRepo,
	provider AccountProvider,
	customTokens CustomTokenIssuer,
) *Service {
	t.Helper()
	app := testApp()
	app.Features = map[string]bool{"firebase_custom_token_bridge": true}
	app.FirebaseCustomTokenServiceAccount = "platform-auth@lizard-tycoon.iam.gserviceaccount.com"
	app.RequireAppCheck = true
	app.Auth.AccountProviders = map[string]registry.AuthProviderConfig{
		"kakao": {Audience: "kakao-client-id"},
	}
	reg := registry.New(fakeSource{apps: []registry.App{app}})
	issuer, err := NewSessionIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(reg, fakeVerifier{}, newMemRepo(), issuer, fakeBlocklist{}).WithCustomTokenIssuer(customTokens)
	if err := service.ConfigureAccountProviders(accounts, provider); err != nil {
		t.Fatal(err)
	}
	return service
}

func TestAccountLinkIssuesLinkedSession(t *testing.T) {
	accounts := newMemoryAccountRepo()
	provider := &fakeAccountProvider{name: "kakao", subject: "provider-subject"}
	customTokens := &fakeCustomTokenIssuer{token: "firebase-custom-token"}
	service := newAccountTestService(t, accounts, provider, customTokens)

	guest, err := service.CreateSession(context.Background(), "lizard-tycoon", Credential{
		Kind: KindFirebaseIDToken, Value: "firebase-anonymous-uid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if guest.IsLinkedAccount {
		t.Fatal("guest session is unexpectedly linked")
	}
	accounts.users[guest.PlatformUserID] = guest.AppUserID
	sess, err := service.Authenticate(context.Background(), "lizard-tycoon", guest.PlatformToken)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.BeginAccountLink(context.Background(), sess, "kakao")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CompleteAccountLink(
		context.Background(), sess, "kakao", "provider-id-token", challenge.Nonce,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Session.IsLinkedAccount || result.Restored {
		t.Fatalf("result = %#v", result)
	}
	if result.FirebaseCustomToken != "firebase-custom-token" ||
		customTokens.uid != guest.AppUserID || provider.nonce != challenge.Nonce ||
		provider.token != "provider-id-token" {
		t.Fatalf("adapter boundary mismatch: result=%#v tokenIssuer=%#v provider=%#v",
			result, customTokens, provider)
	}
	linkedSession, err := service.Authenticate(
		context.Background(), "lizard-tycoon", result.Session.PlatformToken,
	)
	if err != nil || !linkedSession.IsLinkedAccount {
		t.Fatalf("linked session = %#v, err = %v", linkedSession, err)
	}
	refreshed, err := service.Refresh(
		context.Background(), "lizard-tycoon", result.Session.RefreshToken,
	)
	if err != nil || !refreshed.IsLinkedAccount {
		t.Fatalf("refreshed session = %#v, err = %v", refreshed, err)
	}
	if err := service.DisconnectExternalAccount(
		context.Background(), "lizard-tycoon", "kakao", "provider-subject",
	); err != nil {
		t.Fatal(err)
	}
	downgraded, err := service.Refresh(
		context.Background(), "lizard-tycoon", refreshed.RefreshToken,
	)
	if err != nil || downgraded.IsLinkedAccount {
		t.Fatalf("연결 해제 후 갱신 세션 = %#v, err = %v", downgraded, err)
	}
	if accounts.disconnectedSubject != "provider-subject" {
		t.Fatalf("연결 해제 subject = %q", accounts.disconnectedSubject)
	}
}

func TestAccountLinkRestoresExistingPlatformUser(t *testing.T) {
	accounts := newMemoryAccountRepo()
	provider := &fakeAccountProvider{name: "kakao", subject: "provider-subject"}
	customTokens := &fakeCustomTokenIssuer{token: "firebase-custom-token"}
	service := newAccountTestService(t, accounts, provider, customTokens)

	guest, err := service.CreateSession(context.Background(), "lizard-tycoon", Credential{
		Kind: KindFirebaseIDToken, Value: "new-install-uid",
	})
	if err != nil {
		t.Fatal(err)
	}
	accounts.users[guest.PlatformUserID] = guest.AppUserID
	accounts.linked["pu_existing"] = true
	accounts.mappings["lizard-tycoon\x00kakao\x00provider-subject"] = ConnectedAccount{
		PlatformUserID: "pu_existing", AppUserID: "existing-firebase-uid", Provider: "kakao",
	}
	sess, err := service.Authenticate(context.Background(), "lizard-tycoon", guest.PlatformToken)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.BeginAccountLink(context.Background(), sess, "kakao")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CompleteAccountLink(
		context.Background(), sess, "kakao", "provider-id-token", challenge.Nonce,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Restored || result.Session.PlatformUserID != "pu_existing" ||
		result.Session.AppUserID != "existing-firebase-uid" || customTokens.uid != "existing-firebase-uid" {
		t.Fatalf("result = %#v, minted uid = %q", result, customTokens.uid)
	}
}

func TestAccountLinkRejectsSpoofableAnonymousSession(t *testing.T) {
	accounts := newMemoryAccountRepo()
	service := newAccountTestService(t, accounts,
		&fakeAccountProvider{name: "kakao", subject: "provider-subject"},
		&fakeCustomTokenIssuer{token: "firebase-custom-token"},
	)
	_, err := service.BeginAccountLink(context.Background(), Session{
		PlatformUserID: "pu_spoofed", AppID: "lizard-tycoon",
		AppUserID: "anon:chosen-by-client", IsAnonymous: true,
	}, "kakao")
	if platformerr.CodeOf(err) != platformerr.CodeAnonymousNotAllowed {
		t.Fatalf("code = %q, err = %v", platformerr.CodeOf(err), err)
	}
}
