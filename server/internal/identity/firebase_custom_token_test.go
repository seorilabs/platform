package identity

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/registry"
)

type captureJWTSigner struct {
	serviceAccount string
	payload        string
}

func (s *captureJWTSigner) SignJWT(
	_ context.Context,
	serviceAccount, payload string,
) (string, error) {
	s.serviceAccount = serviceAccount
	s.payload = payload
	return "header.payload.signature", nil
}

func TestIAMCustomTokenIssuerBuildsFirebaseClaims(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	signer := &captureJWTSigner{}
	issuer := (&IAMCustomTokenIssuer{signer: signer, now: func() time.Time { return now }})
	app := registry.App{
		AppID:                             "babycare",
		FirebaseCustomTokenServiceAccount: "platform-auth@seorilabs-babycare.iam.gserviceaccount.com",
	}

	token, err := issuer.Mint(context.Background(), app, "legacy-firebase-uid", true)
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if token != "header.payload.signature" {
		t.Fatalf("token = %q", token)
	}
	if signer.serviceAccount != app.FirebaseCustomTokenServiceAccount {
		t.Fatalf("service account = %q", signer.serviceAccount)
	}

	var claims map[string]any
	if err := json.Unmarshal([]byte(signer.payload), &claims); err != nil {
		t.Fatalf("payload decode = %v", err)
	}
	if claims["iss"] != app.FirebaseCustomTokenServiceAccount ||
		claims["sub"] != app.FirebaseCustomTokenServiceAccount ||
		claims["aud"] != firebaseCustomTokenAudience ||
		claims["uid"] != "legacy-firebase-uid" {
		t.Fatalf("claims = %#v", claims)
	}
	if claims["iat"] != float64(now.Unix()) || claims["exp"] != float64(now.Add(time.Hour).Unix()) {
		t.Fatalf("token lifetime = %#v", claims)
	}
	developerClaims, ok := claims["claims"].(map[string]any)
	if !ok || developerClaims["seori_app_id"] != "babycare" || developerClaims["seori_guest"] != true {
		t.Fatalf("developer claims = %#v", claims["claims"])
	}
}

func TestIAMCustomTokenIssuerRejectsInvalidUID(t *testing.T) {
	issuer := &IAMCustomTokenIssuer{signer: &captureJWTSigner{}, now: time.Now}
	app := registry.App{
		FirebaseCustomTokenServiceAccount: "platform-auth@seorilabs-babycare.iam.gserviceaccount.com",
	}
	for _, uid := range []string{"", string(make([]byte, 129))} {
		if _, err := issuer.Mint(context.Background(), app, uid, true); err == nil {
			t.Fatalf("uid 길이 %d를 허용했다", len(uid))
		}
	}
}
