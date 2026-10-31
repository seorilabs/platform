package identity

import (
	"github.com/golang-jwt/jwt/v5"
	"strings"
	"testing"
)

func TestFirebaseVerifierReadsSignedBridgeProvenance(t *testing.T) {
	f := newFixture(t)
	guest := false
	token := f.sign(t, jwt.SigningMethodRS256, testKID, f.priv, func(c *firebaseClaims) {
		c.Subject = "ait:" + strings.Repeat("a", 64)
		c.Firebase.SignInProvider = "custom"
		c.PlatformAppID = testApp().AppID
		c.PlatformGuest = &guest
	})
	got, err := f.verifier.Verify(t.Context(), token, testApp())
	if err != nil {
		t.Fatal(err)
	}
	if got.PlatformAppID != testApp().AppID || got.PlatformGuest == nil || *got.PlatformGuest || got.SignInProvider != "custom" {
		t.Fatalf("signed provenance missing: %+v", got)
	}
}
