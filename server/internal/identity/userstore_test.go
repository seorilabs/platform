package identity

import (
	"context"
	"strings"
	"testing"
)

func TestIdentityEventAttributesOmitEmptyReferrer(t *testing.T) {
	ait := identityEventAttributes(NewIdentity{
		UID: "ait:user", AuthType: "apps_in_toss", Referrer: "SANDBOX",
	})
	if ait["referrer"] != "SANDBOX" {
		t.Fatalf("AppsInToss referrer가 빠졌다: %v", ait)
	}
	for _, authType := range []string{"firebase", "anonymous", "firebase_bridge"} {
		attributes := identityEventAttributes(NewIdentity{UID: "user", AuthType: authType})
		if _, ok := attributes["referrer"]; ok {
			t.Fatalf("%s 경로에 빈 referrer가 실렸다: %v", authType, attributes)
		}
		if attributes["authType"] != authType || attributes["anonymous"] != false {
			t.Fatalf("%s 속성이 어긋났다: %v", authType, attributes)
		}
	}
}

func TestIdentityEventAttributesCarrySignInProvider(t *testing.T) {
	// authType은 계정이 만들어진 경로라 google.com인지 anonymous인지를 가린다.
	linked := identityEventAttributes(NewIdentity{
		UID: "firebase-user", AuthType: "firebase_bridge", SignInProvider: "google.com",
	})
	if linked["signInProvider"] != "google.com" {
		t.Fatalf("로그인 공급자가 빠졌다: %v", linked)
	}

	// platform이 uid를 만든 게스트 계정에는 아직 로그인이 없다.
	guest := identityEventAttributes(NewIdentity{UID: "firebase-user", AuthType: "firebase_bridge"})
	if _, ok := guest["signInProvider"]; ok {
		t.Fatalf("게스트 경로에 빈 공급자가 실렸다: %v", guest)
	}

	// 계약 상한을 넘는 값은 이벤트를 통째로 막는다. 관측 속성 때문에 가입이
	// 되돌아가면 안 되므로 싣지 않는다.
	long := identityEventAttributes(NewIdentity{
		UID:            "firebase-user",
		AuthType:       "firebase",
		SignInProvider: strings.Repeat("o", maxSignInProviderLen+1),
	})
	if _, ok := long["signInProvider"]; ok {
		t.Fatalf("상한을 넘는 공급자가 실렸다: %v", long)
	}
}

func TestFirebaseSessionCarriesSignInProvider(t *testing.T) {
	repo := newMemRepo()
	svc := newTestService(t, fakeVerifier{}, repo)
	if _, err := svc.CreateSession(context.Background(), testApp().AppID, Credential{
		Kind: KindFirebaseIDToken, Value: "id-token",
	}); err != nil {
		t.Fatal(err)
	}
	if repo.lastIdentity.SignInProvider != "anonymous" {
		t.Fatalf("Firebase 공급자가 전달되지 않았다: %q", repo.lastIdentity.SignInProvider)
	}
}

func TestAnonymousCredentialCarriesNoSignInProvider(t *testing.T) {
	// 클라이언트가 값을 고르는 경로다. Firebase가 확인한 공급자가 아니다.
	repo := newMemRepo()
	svc := newTestService(t, fakeVerifier{}, repo)
	if _, err := svc.CreateSession(context.Background(), testApp().AppID, Credential{
		Kind: KindAnonymous, Value: "device-hash",
	}); err != nil {
		t.Fatal(err)
	}
	if repo.lastIdentity.SignInProvider != "" {
		t.Fatalf("익명 자격증명이 공급자를 지어냈다: %q", repo.lastIdentity.SignInProvider)
	}
}

func TestFirebaseSessionPassesNoReferrer(t *testing.T) {
	repo := newMemRepo()
	svc := newTestService(t, fakeVerifier{}, repo)
	if _, err := svc.CreateSession(context.Background(), testApp().AppID, Credential{
		Kind: KindFirebaseIDToken, Value: "id-token",
	}); err != nil {
		t.Fatal(err)
	}
	if repo.lastReferrer != "" {
		t.Fatalf("Firebase 경로가 referrer를 넘겼다: %q", repo.lastReferrer)
	}
}
