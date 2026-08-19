package identity

import (
	"context"
	"testing"
)

func TestIdentityEventAttributesOmitEmptyReferrer(t *testing.T) {
	ait := identityEventAttributes("apps_in_toss", false, "SANDBOX")
	if ait["referrer"] != "SANDBOX" {
		t.Fatalf("AppsInToss referrer가 빠졌다: %v", ait)
	}
	for _, authType := range []string{"firebase", "anonymous", "firebase_bridge"} {
		attributes := identityEventAttributes(authType, false, "")
		if _, ok := attributes["referrer"]; ok {
			t.Fatalf("%s 경로에 빈 referrer가 실렸다: %v", authType, attributes)
		}
		if attributes["authType"] != authType || attributes["anonymous"] != false {
			t.Fatalf("%s 속성이 어긋났다: %v", authType, attributes)
		}
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
