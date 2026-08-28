package presence

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

func TestTokenRoundTripAndExpiry(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	issuer, err := NewIssuer(privateKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	issuer.WithClock(func() time.Time { return now })
	verifier, err := NewVerifier(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	verifier.WithClock(func() time.Time { return now.Add(30 * time.Minute) })

	raw, expiresAt, err := issuer.Issue("happy-farm", "session_0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifier.Verify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.AppID != "happy-farm" || len(got.SessionHash) != 64 {
		t.Fatalf("unexpected token: %+v", got)
	}
	if !expiresAt.Equal(now.Add(time.Hour)) || !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expiry mismatch: issue=%s verify=%s", expiresAt, got.ExpiresAt)
	}

	verifier.WithClock(func() time.Time { return now.Add(2 * time.Hour) })
	if _, err := verifier.Verify(raw); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func TestParsePublicKey(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw := base64.StdEncoding.EncodeToString(publicKey)
	got, err := ParsePublicKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(publicKey) {
		t.Fatal("public key changed")
	}
}
