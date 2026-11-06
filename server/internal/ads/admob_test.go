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
	"strings"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
)

type ssvQueryParam struct{ key, value string }

func signSSVQuery(t *testing.T, privateKey *ecdsa.PrivateKey, params []ssvQueryParam) string {
	t.Helper()
	var uriContent, escaped []string
	for _, item := range params {
		uriContent = append(uriContent, item.key+"="+item.value)
		escaped = append(escaped, url.QueryEscape(item.key)+"="+strings.ReplaceAll(url.QueryEscape(item.value), "+", "%20"))
	}
	signedData := strings.Join(escaped, "&")
	digest := sha256.Sum256([]byte(strings.Join(uriContent, "&")))
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

// unit 교체는 서버와 설치된 바이너리가 동시에 바뀌지 않는다. 구버전은 계속 옛 unit으로
// 광고를 재생하고, SSV 콜백의 ad_unit도 그 값으로 온다. 은퇴 unit을 수용하지 않으면
// 교체 시점에 구버전 전체가 광고를 끝까지 보고도 보상을 못 받는다.
func TestAdMobAcceptsRetiredUnitDuringTransition(t *testing.T) {
	app := rewardedApp()
	provider := app.Ads.Placements[0].Providers["admob"]
	provider.AndroidAdUnitID = "ca-app-pub-1111111111111111/1111111111"
	provider.RetiredAndroidAdUnitIDs = []string{"ca-app-pub-0000000000000000/1234567890"}
	provider.IOSAdUnitID = "ca-app-pub-1111111111111111/2222222222"
	provider.RetiredIOSAdUnitIDs = []string{"ca-app-pub-0000000000000000/7777777777"}
	app.Ads.Placements[0].Providers["admob"] = provider

	for _, tc := range []struct {
		name     string
		platform string
		adUnitID string
		code     platformerr.Code
	}{
		{name: "android 신규", platform: "android", adUnitID: "1111111111"},
		{name: "android 은퇴", platform: "android", adUnitID: "1234567890"},
		{name: "ios 신규", platform: "ios", adUnitID: "2222222222"},
		{name: "ios 은퇴", platform: "ios", adUnitID: "7777777777"},
		// 플랫폼을 넘지 않는다. iOS claim이 Android 은퇴 unit으로 확정되면
		// 한쪽 스토어의 구버전이 다른 쪽 원장으로 보상을 받는다.
		{name: "ios claim에 android 은퇴 unit", platform: "ios", adUnitID: "1234567890", code: platformerr.CodeAdUnitMismatch},
		{name: "목록에 없는 unit", platform: "android", adUnitID: "9999999999", code: platformerr.CodeAdUnitMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{claims: map[string]Claim{
				"cl_1": {
					ClaimID: "cl_1", AppID: app.AppID, PlatformUserID: "pu_1",
					PlacementID: "harvest_boost", Provider: "admob",
					ClientPlatform: tc.platform, State: StateAccepted,
					ExpiresAt: time.Now().Add(time.Hour),
				},
			}}
			svc, err := NewService(repo, fakeApps{app.AppID: app}, fakeEntitlements{}, fakeUsers{})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := svc.ConfirmAdMob(context.Background(), app.AppID, SSVResult{
				ClaimID: "cl_1", PlatformUserID: "pu_1", AdUnitID: tc.adUnitID,
				TransactionID: "tx", RewardItem: "harvest_boost", RewardAmount: 1,
			})
			if platformerr.CodeOf(err) != tc.code {
				t.Fatalf("code = %q, want %q", platformerr.CodeOf(err), tc.code)
			}
			// 통과해야 하는 경우는 실제로 확정까지 가야 의미가 있다. unit 대조만
			// 지나고 뒤에서 막히면 사용자에게는 여전히 보상이 없다.
			if tc.code == "" && claim.State != StateConfirmed {
				t.Fatalf("state = %q, want %q", claim.State, StateConfirmed)
			}
		})
	}
}

// 은퇴 unit만 있고 현재 unit이 없는 플랫폼은 광고를 서비스하지 않는다는 뜻이다.
// 목록만 보고 통과시키면 지면을 내린 플랫폼에서 보상이 계속 나간다.
func TestAdMobRejectsRetiredUnitWithoutCurrentUnit(t *testing.T) {
	app := rewardedApp()
	provider := app.Ads.Placements[0].Providers["admob"]
	provider.IOSAdUnitID = ""
	provider.RetiredIOSAdUnitIDs = []string{"ca-app-pub-0000000000000000/7777777777"}
	app.Ads.Placements[0].Providers["admob"] = provider

	repo := &fakeRepo{claims: map[string]Claim{
		"cl_1": {
			ClaimID: "cl_1", AppID: app.AppID, PlatformUserID: "pu_1",
			PlacementID: "harvest_boost", Provider: "admob",
			ClientPlatform: "ios", State: StateAccepted,
		},
	}}
	svc, err := NewService(repo, fakeApps{app.AppID: app}, fakeEntitlements{}, fakeUsers{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ConfirmAdMob(context.Background(), app.AppID, SSVResult{
		ClaimID: "cl_1", PlatformUserID: "pu_1", AdUnitID: "7777777777",
		TransactionID: "tx", RewardItem: "harvest_boost", RewardAmount: 1,
	})
	if platformerr.CodeOf(err) != platformerr.CodeAdUnitMismatch {
		t.Fatalf("code = %q, want %q", platformerr.CodeOf(err), platformerr.CodeAdUnitMismatch)
	}
}

// Google 공식 Java verifier의 URI.getQuery()와 같은 계약이다.
// 콘솔 실제 콜백의 한국어 reward_item은 raw query로 검증하면 실패하고
// percent escapes를 한 번 푼 UTF-8 바이트로 검증해야 한다.
func TestAdMobVerifierUsesDecodedURIBytesForUnicodeRewards(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{"keyId": 7, "pem": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}}})
	}))
	defer keys.Close()
	now := time.Now().UTC()
	params := []ssvQueryParam{{"ad_network", "5450213213286189855"}, {"ad_unit", "1234567890"}, {"custom_data", "cl_12345678-1234-1234-1234-123456789abc"}, {"reward_amount", "1"}, {"reward_item", "남은 시간 50% 단축 + 돛"}, {"timestamp", strconv.FormatInt(now.UnixMilli(), 10)}, {"transaction_id", "unicode-transaction"}, {"user_id", "pu_unicode"}}
	var canonical, escaped []string
	for _, p := range params {
		canonical = append(canonical, p.key+"="+p.value)
		escaped = append(escaped, p.key+"="+strings.ReplaceAll(url.QueryEscape(p.value), "+", "%20"))
	}
	digest := sha256.Sum256([]byte(strings.Join(canonical, "&")))
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Join(escaped, "&") + "&signature=" + base64.RawURLEncoding.EncodeToString(signature) + "&key_id=7"
	verifier := NewAdMobVerifier(keys.Client(), keys.URL)
	result, err := verifier.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("공식 URI 서명 계약의 한국어 보상 거절: %v", err)
	}
	if result.RewardItem != "남은 시간 50% 단축 + 돛" {
		t.Fatalf("보상 문구가 달라짐: %q", result.RewardItem)
	}
	literalPlus := strings.Replace(raw, "%2B", "+", 1)
	same, err := verifier.Verify(context.Background(), literalPlus)
	if err != nil || same.RewardItem != result.RewardItem {
		t.Fatalf("URI 동치인 literal plus가 보상 값을 바꿈: %q %v", same.RewardItem, err)
	}
	// percent decode와 form decode는 다르다. 인코딩된 +를 한 번 푼 뒤 공백으로 바꾸거나
	// %25를 두 번 풀면 원본 서명이 성립하지 않아야 한다.
	tampered := strings.Replace(raw, "%2B", "%20", 1)
	if _, err := verifier.Verify(context.Background(), tampered); platformerr.CodeOf(err) != platformerr.CodeSSVSignatureInvalid {
		t.Fatalf("보상 변조 허용: %v", err)
	}
	doubleEncoded := strings.Replace(raw, "%25", "%2525", 1)
	if _, err := verifier.Verify(context.Background(), doubleEncoded); platformerr.CodeOf(err) != platformerr.CodeSSVSignatureInvalid {
		t.Fatalf("percent 중복 해석 허용: %v", err)
	}
}
