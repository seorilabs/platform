package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type rotatingJWKS struct {
	mu        sync.Mutex
	responses [][]byte
	calls     int
}

func (r *rotatingJWKS) roundTrip(*http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := r.calls
	if index >= len(r.responses) {
		index = len(r.responses) - 1
	}
	r.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Cache-Control": []string{"public, max-age=3600"}},
		Body:       io.NopCloser(strings.NewReader(string(r.responses[index]))),
	}, nil
}

func TestVerifierVerifyAndRefreshRotatedKey(t *testing.T) {
	now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
	first := newTestKey(t)
	second := newTestKey(t)
	remote := &rotatingJWKS{responses: [][]byte{
		jwkSetJSON(t, "first", &first.PublicKey),
		jwkSetJSON(t, "second", &second.PublicKey),
	}}
	verifier, err := New(Config{
		Provider: "kakao", Issuer: "https://issuer.example", JWKSURL: "https://issuer.example/keys",
		Client: &http.Client{Transport: roundTripperFunc(remote.roundTrip)}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	app := testApp("client-id")

	for index, tc := range []struct {
		kid string
		key *rsa.PrivateKey
	}{
		{kid: "first", key: first},
		{kid: "second", key: second},
	} {
		if index > 0 {
			now = now.Add(2 * time.Minute)
		}
		token := signToken(t, tc.key, tc.kid, claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer: "https://issuer.example", Subject: "provider-user",
				Audience: jwt.ClaimStrings{"client-id"}, IssuedAt: jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			},
			Nonce: "challenge",
		})
		subject, err := verifier.Verify(context.Background(), token, "challenge", app)
		if err != nil {
			t.Fatalf("kid %s verify: %v", tc.kid, err)
		}
		if subject != "provider-user" {
			t.Fatalf("subject = %q", subject)
		}
	}
	if remote.calls != 2 {
		t.Fatalf("JWKS calls = %d, want 2", remote.calls)
	}
}

func TestVerifierRejectsClaimsOutsideBoundary(t *testing.T) {
	now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
	key := newTestKey(t)
	client := staticJWKSClient(t, "key", &key.PublicKey)
	verifier, err := New(Config{
		Provider: "kakao", Issuer: "https://issuer.example", JWKSURL: "https://issuer.example/keys",
		Client: client, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		audience string
		nonce    string
		issuedAt *jwt.NumericDate
	}{
		{name: "audience", audience: "other-client", nonce: "challenge", issuedAt: jwt.NewNumericDate(now)},
		{name: "nonce", audience: "client-id", nonce: "other", issuedAt: jwt.NewNumericDate(now)},
		{name: "issued at required", audience: "client-id", nonce: "challenge"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			token := signToken(t, key, "key", claims{
				RegisteredClaims: jwt.RegisteredClaims{
					Issuer: "https://issuer.example", Subject: "provider-user",
					Audience: jwt.ClaimStrings{tc.audience}, IssuedAt: tc.issuedAt,
					ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
				},
				Nonce: tc.nonce,
			})
			_, err := verifier.Verify(context.Background(), token, "challenge", testApp("client-id"))
			if platformerr.CodeOf(err) != platformerr.CodeAuthInvalid {
				t.Fatalf("code = %q, err = %v", platformerr.CodeOf(err), err)
			}
		})
	}
}

func TestAppleVerifierUsesSHA256Nonce(t *testing.T) {
	now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
	key := newTestKey(t)
	verifier, err := NewApple(staticJWKSClient(t, "apple-key", &key.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	rawNonce := "challenge"
	sum := sha256.Sum256([]byte(rawNonce))
	token := signToken(t, key, "apple-key", claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: appleIssuer, Subject: "apple-user", Audience: jwt.ClaimStrings{"com.seorilabs.app"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		Nonce: hex.EncodeToString(sum[:]),
	})
	verifier.now = func() time.Time { return now }
	verifier.keys.now = verifier.now

	subject, err := verifier.Verify(context.Background(), token, rawNonce, testAppleApp("com.seorilabs.app"))
	if err != nil {
		t.Fatal(err)
	}
	if subject != "apple-user" {
		t.Fatalf("subject = %q", subject)
	}
}

func TestVerifierPreservesProviderUnavailable(t *testing.T) {
	verifier, err := New(Config{
		Provider: "kakao", Issuer: "https://issuer.example", JWKSURL: "https://issuer.example/keys",
		Client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network unavailable")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := newTestKey(t)
	now := time.Now().UTC()
	token := signToken(t, key, "key", claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "https://issuer.example", Subject: "provider-user", Audience: jwt.ClaimStrings{"client-id"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		}, Nonce: "challenge",
	})
	_, err = verifier.Verify(context.Background(), token, "challenge", testApp("client-id"))
	if platformerr.CodeOf(err) != platformerr.CodeProviderUnavailable {
		t.Fatalf("code = %q, err = %v", platformerr.CodeOf(err), err)
	}
}

func newTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func staticJWKSClient(t *testing.T, kid string, key *rsa.PublicKey) *http.Client {
	t.Helper()
	body := jwkSetJSON(t, kid, key)
	return &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Cache-Control": []string{"public, max-age=3600"}},
			Body:       io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})}
}

func jwkSetJSON(t *testing.T, kid string, key *rsa.PublicKey) []byte {
	t.Helper()
	exponent := big.NewInt(int64(key.E)).Bytes()
	body, err := json.Marshal(jwkSetResponse{Keys: []jwk{{
		KeyType: "RSA", KeyID: kid, Use: "sig", Algorithm: "RS256",
		Modulus:  base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		Exponent: base64.RawURLEncoding.EncodeToString(exponent),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signToken(t *testing.T, key *rsa.PrivateKey, kid string, value claims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, value)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func testApp(audience string) registry.App {
	return registry.App{Auth: registry.AuthConfig{AccountProviders: map[string]registry.AuthProviderConfig{
		"kakao": {Audience: audience},
	}}}
}

func testAppleApp(audience string) registry.App {
	return registry.App{Auth: registry.AuthConfig{AccountProviders: map[string]registry.AuthProviderConfig{
		"apple": {Audience: audience},
	}}}
}
