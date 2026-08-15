package ads

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type ssvQueryParam struct{ key, value string }

func signSSVQuery(t *testing.T, privateKey *ecdsa.PrivateKey, params []ssvQueryParam) string {
	t.Helper()
	signedData := ""
	for i, item := range params {
		if i > 0 {
			signedData += "&"
		}
		signedData += url.QueryEscape(item.key) + "=" + url.QueryEscape(item.value)
	}
	digest := sha256.Sum256([]byte(signedData))
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signedData + "&signature=" + url.QueryEscape(base64.RawURLEncoding.EncodeToString(signature)) + "&key_id=7"
}

func TestAdMobVerifierChecksSignatureAndFields(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{"keyId": 7, "pem": publicPEM}}})
	}))
	defer keys.Close()

	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	verifier := NewAdMobVerifier(keys.Client(), keys.URL)
	verifier.now = func() time.Time { return now }
	params := []ssvQueryParam{
		{"ad_network", "5450213213286189855"},
		{"ad_unit", "1234567890"},
		{"custom_data", "cl_12345678-1234-1234-1234-123456789abc"},
		{"reward_amount", "1"},
		{"reward_item", "harvest_boost"},
		{"timestamp", strconv.FormatInt(now.UnixMilli(), 10)},
		{"transaction_id", "transaction-1"},
		{"user_id", "pu_123"},
	}
	raw := signSSVQuery(t, privateKey, params)

	result, err := verifier.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("valid SSV rejected: %v", err)
	}
	if result.ClaimID != "cl_12345678-1234-1234-1234-123456789abc" || result.PlatformUserID != "pu_123" {
		t.Fatalf("result = %#v", result)
	}

	t.Run("변조된 query", func(t *testing.T) {
		_, err := verifier.Verify(context.Background(), raw+"&extra=changed")
		if platformerr.CodeOf(err) != platformerr.CodeSSVInvalid {
			t.Fatalf("code = %q", platformerr.CodeOf(err))
		}
	})

	t.Run("만료", func(t *testing.T) {
		verifier.now = func() time.Time { return now.Add(25 * time.Hour) }
		_, err := verifier.Verify(context.Background(), raw)
		if platformerr.CodeOf(err) != platformerr.CodeSSVInvalid {
			t.Fatalf("code = %q", platformerr.CodeOf(err))
		}
	})
}

func TestAdMobVerifierAcceptsConsoleProbeWithoutOptionalFields(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{"keyId": 7, "pem": publicPEM}}})
	}))
	defer keys.Close()

	now := time.Date(2026, 8, 15, 4, 0, 0, 0, time.UTC)
	verifier := NewAdMobVerifier(keys.Client(), keys.URL)
	verifier.now = func() time.Time { return now }
	params := []ssvQueryParam{
		{"ad_network", "5450213213286189855"},
		{"ad_unit", "1234567890"},
		{"reward_amount", "1"},
		{"reward_item", "in_game_bonus"},
		{"timestamp", strconv.FormatInt(now.UnixMilli(), 10)},
		{"transaction_id", "123456789"},
	}
	raw := signSSVQuery(t, privateKey, params)

	result, err := verifier.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("AdMob console probe rejected: %v", err)
	}
	if !isVerificationProbe(result) || result.ClaimID != "" || result.PlatformUserID != "" {
		t.Fatalf("probe result = %#v", result)
	}

	missingClaimAndUser := signSSVQuery(t, privateKey, []ssvQueryParam{
		{"ad_network", "5450213213286189855"},
		{"ad_unit", "6693003024"},
		{"reward_amount", "1"},
		{"reward_item", "in_game_bonus"},
		{"timestamp", strconv.FormatInt(now.UnixMilli(), 10)},
		{"transaction_id", "real-transaction"},
	})
	_, err = verifier.Verify(context.Background(), missingClaimAndUser)
	if platformerr.CodeOf(err) != platformerr.CodeSSVInvalid {
		t.Fatalf("missing claim/user code = %q", platformerr.CodeOf(err))
	}
}

func TestAdMobClaimOwnershipAndUnitAreBound(t *testing.T) {
	app := rewardedApp()
	provider := app.Ads.Placements[0].Providers["admob"]
	provider.RewardItem = "harvest_boost"
	provider.RewardAmount = 1
	app.Ads.Placements[0].Providers["admob"] = provider
	repo := &fakeRepo{claims: map[string]Claim{
		"cl_1": {ClaimID: "cl_1", AppID: app.AppID, PlatformUserID: "pu_1", PlacementID: "harvest_boost", Provider: "admob", ClientPlatform: "android", State: StateAccepted},
	}}
	svc, err := NewService(repo, fakeApps{app.AppID: app}, fakeEntitlements{}, fakeUsers{})
	if err != nil {
		t.Fatal(err)
	}
	base := SSVResult{ClaimID: "cl_1", PlatformUserID: "pu_1", AdUnitID: "1234567890", TransactionID: "tx", RewardItem: "harvest_boost", RewardAmount: 1}

	for _, tc := range []struct {
		name   string
		mutate func(*SSVResult)
		code   platformerr.Code
	}{
		{name: "사용자 불일치", mutate: func(v *SSVResult) { v.PlatformUserID = "pu_other" }, code: platformerr.CodeClaimOwnershipMismatch},
		{name: "unit 불일치", mutate: func(v *SSVResult) { v.AdUnitID = "9999999999" }, code: platformerr.CodeAdUnitMismatch},
		{name: "보상 불일치", mutate: func(v *SSVResult) { v.RewardAmount = 2 }, code: platformerr.CodeAdRewardInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			tc.mutate(&input)
			_, err := svc.ConfirmAdMob(context.Background(), app.AppID, input)
			if platformerr.CodeOf(err) != tc.code {
				t.Fatalf("code = %q, want %q", platformerr.CodeOf(err), tc.code)
			}
		})
	}
}

func TestAdMobCallbackCannotCrossAppBoundary(t *testing.T) {
	first := rewardedApp()
	second := rewardedApp()
	second.AppID = "other-game"
	second.Ads.Placements[0].Providers["admob"] = registry.AdsProviderConfig{
		AndroidAdUnitID: "ca-app-pub-0000000000000000/9999999999",
		RewardItem:      "harvest_boost", RewardAmount: 1,
	}
	repo := &fakeRepo{claims: map[string]Claim{
		"cl_1": {
			ClaimID: "cl_1", AppID: first.AppID, PlatformUserID: "pu_1",
			PlacementID: "harvest_boost", Provider: "admob", ClientPlatform: "android",
			State: StateAccepted,
		},
	}}
	svc, err := NewService(repo, fakeApps{first.AppID: first, second.AppID: second}, fakeEntitlements{}, fakeUsers{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ConfirmAdMob(context.Background(), second.AppID, SSVResult{
		ClaimID: "cl_1", PlatformUserID: "pu_1", AdUnitID: "9999999999",
		TransactionID: "tx", RewardItem: "harvest_boost", RewardAmount: 1,
	})
	if platformerr.CodeOf(err) != platformerr.CodeClaimOwnershipMismatch {
		t.Fatalf("code = %q, want %q", platformerr.CodeOf(err), platformerr.CodeClaimOwnershipMismatch)
	}
}

type fixedSSVVerifier struct {
	result SSVResult
	err    error
	calls  int
}

func (v *fixedSSVVerifier) Verify(context.Context, string) (SSVResult, error) {
	v.calls++
	return v.result, v.err
}

func TestAdMobHandlerSeparatesProbeAndCallbackPerApp(t *testing.T) {
	app := rewardedApp()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{claims: map[string]Claim{
		"cl_1": {
			ClaimID: "cl_1", AppID: app.AppID, PlatformUserID: "pu_1",
			PlacementID: "harvest_boost", Provider: "admob", ClientPlatform: "android",
			State: StateAccepted, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		},
	}}
	svc, err := NewService(repo, fakeApps{app.AppID: app}, fakeEntitlements{}, fakeUsers{})
	if err != nil {
		t.Fatal(err)
	}
	svc.WithClock(func() time.Time { return now })
	probeVerifier := &fixedSSVVerifier{result: SSVResult{
		AdNetworkID: "5450213213286189855", AdUnitID: "1234567890", TransactionID: "123456789",
	}}
	handler := NewHandler(svc, nil, probeVerifier)
	request := httptest.NewRequest(http.MethodGet, "/v1/ads/admob/ssv/happy-farm", nil)
	request.SetPathValue("appId", app.AppID)
	if err := handler.admobSSV(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}
	if len(repo.ssvEvents) != 1 || repo.ssvEvents[0] != (recordedSSVEvent{appID: app.AppID, event: SSVProbeSuccess}) {
		t.Fatalf("probe events = %#v", repo.ssvEvents)
	}

	callbackVerifier := &fixedSSVVerifier{result: SSVResult{
		ClaimID: "cl_1", PlatformUserID: "pu_1", AdUnitID: "1234567890",
		TransactionID: "tx-1", RewardItem: "harvest_boost", RewardAmount: 1,
	}}
	handler.verifier = callbackVerifier
	if err := handler.admobSSV(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}
	if len(repo.ssvEvents) != 2 || repo.ssvEvents[1] != (recordedSSVEvent{appID: app.AppID, event: SSVCallbackSuccess}) {
		t.Fatalf("callback events = %#v", repo.ssvEvents)
	}

	unknownRequest := httptest.NewRequest(http.MethodGet, "/v1/ads/admob/ssv/unknown", nil)
	unknownRequest.SetPathValue("appId", "unknown")
	if err := handler.admobSSV(httptest.NewRecorder(), unknownRequest); platformerr.CodeOf(err) != platformerr.CodeAppUnknown {
		t.Fatalf("unknown app code = %q", platformerr.CodeOf(err))
	}
	if callbackVerifier.calls != 1 {
		t.Fatalf("unknown app reached verifier: calls=%d", callbackVerifier.calls)
	}
}
