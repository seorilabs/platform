package ads

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

const defaultVerifierKeysURL = "https://www.gstatic.com/admob/reward/verifier-keys.json"

var (
	claimIDPattern = regexp.MustCompile(`^cl_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	numericPattern = regexp.MustCompile(`^[0-9]+$`)
)

type AdMobVerifier struct {
	client    *http.Client
	keysURL   string
	now       func() time.Time
	mu        sync.Mutex
	keys      map[string]*ecdsa.PublicKey
	expiresAt time.Time
}

func NewAdMobVerifier(client *http.Client, keysURL string) *AdMobVerifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if strings.TrimSpace(keysURL) == "" {
		keysURL = defaultVerifierKeysURL
	}
	return &AdMobVerifier{client: client, keysURL: keysURL, now: time.Now}
}

func (v *AdMobVerifier) Verify(ctx context.Context, rawQuery string) (SSVResult, error) {
	marker := "&signature="
	idx := strings.LastIndex(rawQuery, marker)
	if idx < 0 {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVInvalid, "AdMob SSV signature가 없어요")
	}
	// AdMob은 마지막 두 파라미터를 signature, key_id 순서로 보낸다.
	// key_id 뒤의 unsigned 파라미터를 허용하면 콜백 형식 드리프트나
	// 중복 파라미터 해석 차이를 놓칠 수 있으므로 정확한 suffix만 받는다.
	signatureValue, keyIDValue, ok := strings.Cut(rawQuery[idx+len(marker):], "&key_id=")
	if !ok || signatureValue == "" || !numericPattern.MatchString(keyIDValue) || strings.Contains(keyIDValue, "&") {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVInvalid, "AdMob SSV signature와 key ID 순서가 올바르지 않아요")
	}
	signedData := rawQuery[:idx]
	params, err := url.ParseQuery(rawQuery)
	if err != nil {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVInvalid, "AdMob SSV query가 올바르지 않아요")
	}
	// custom_data와 user_id는 SDK에서 값을 설정했을 때만 전송된다. AdMob
	// 콘솔의 URL 검증 probe도 두 값을 생략하므로 서명 검증 자체의 필수
	// 파라미터로 취급하지 않는다. 실제 보상 callback은 아래에서 두 값을
	// 다시 요구해 claim과 사용자의 결합을 느슨하게 만들지 않는다.
	required := []string{"ad_network", "ad_unit", "reward_amount", "reward_item", "signature", "timestamp", "transaction_id", "key_id"}
	for _, key := range required {
		if params.Get(key) == "" {
			return SSVResult{}, platformerr.New(platformerr.CodeSSVInvalid, "AdMob SSV 필수 값이 없어요")
		}
	}
	keyID := params.Get("key_id")
	if keyID != keyIDValue {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVInvalid, "AdMob SSV key ID가 올바르지 않아요")
	}
	key, err := v.key(ctx, keyID, false)
	if err != nil {
		return SSVResult{}, err
	}
	if key == nil {
		key, err = v.key(ctx, keyID, true)
		if err != nil {
			return SSVResult{}, err
		}
	}
	if key == nil {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVKeyUnavailable, "AdMob 검증 키를 찾을 수 없어요")
	}
	signature, err := decodeSignature(params.Get("signature"))
	if err != nil {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVSignatureInvalid, "AdMob SSV signature가 올바르지 않아요")
	}
	digest := sha256.Sum256([]byte(signedData))
	if !ecdsa.VerifyASN1(key, digest[:], signature) {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVSignatureInvalid, "AdMob SSV signature가 올바르지 않아요")
	}
	timestampMS, err := strconv.ParseInt(params.Get("timestamp"), 10, 64)
	if err != nil {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVInvalid, "AdMob SSV timestamp가 올바르지 않아요")
	}
	timestamp := time.UnixMilli(timestampMS)
	if delta := v.now().Sub(timestamp); delta > 24*time.Hour || delta < -24*time.Hour {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVInvalid, "AdMob SSV가 만료됐어요")
	}
	rewardAmount, err := strconv.Atoi(params.Get("reward_amount"))
	if err != nil || rewardAmount <= 0 {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVInvalid, "AdMob SSV reward가 올바르지 않아요")
	}
	result := SSVResult{AdNetworkID: params.Get("ad_network"), AdUnitID: params.Get("ad_unit"), ClaimID: params.Get("custom_data"), TransactionID: params.Get("transaction_id"), PlatformUserID: params.Get("user_id"), RewardItem: params.Get("reward_item"), RewardAmount: rewardAmount, Timestamp: timestamp}
	if !numericPattern.MatchString(result.AdUnitID) || !validSSVValue(result.TransactionID, 256) {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVInvalid, "AdMob SSV 식별자가 올바르지 않아요")
	}
	if isVerificationProbe(result) {
		return result, nil
	}
	if !validSSVValue(result.PlatformUserID, 128) || !claimIDPattern.MatchString(result.ClaimID) {
		return SSVResult{}, platformerr.New(platformerr.CodeSSVInvalid, "AdMob SSV claim ID가 올바르지 않아요")
	}
	return result, nil
}

func (v *AdMobVerifier) key(ctx context.Context, id string, force bool) (*ecdsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !force && v.now().Before(v.expiresAt) && len(v.keys) > 0 {
		return v.keys[id], nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.keysURL, nil)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeSSVKeyUnavailable, "AdMob 검증 키 요청을 만들지 못했어요")
	}
	res, err := v.client.Do(req)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeSSVKeyUnavailable, "AdMob 검증 키를 받지 못했어요")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, platformerr.New(platformerr.CodeSSVKeyUnavailable, "AdMob 검증 키 응답이 올바르지 않아요")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeSSVKeyUnavailable, "AdMob 검증 키를 읽지 못했어요")
	}
	var payload struct {
		Keys []struct {
			KeyID any    `json:"keyId"`
			PEM   string `json:"pem"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeSSVKeyUnavailable, "AdMob 검증 키 응답을 해석하지 못했어요")
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, item := range payload.Keys {
		block, _ := pem.Decode([]byte(item.PEM))
		if block == nil {
			continue
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			continue
		}
		publicKey, ok := parsed.(*ecdsa.PublicKey)
		if !ok {
			continue
		}
		keys[formatKeyID(item.KeyID)] = publicKey
	}
	if len(keys) == 0 {
		return nil, platformerr.New(platformerr.CodeSSVKeyUnavailable, "AdMob 검증 키가 비어 있어요")
	}
	v.keys = keys
	v.expiresAt = v.now().Add(24 * time.Hour)
	return keys[id], nil
}

func decodeSignature(value string) ([]byte, error) {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "-", "+"), "_", "/")
	if pad := len(value) % 4; pad != 0 {
		value += strings.Repeat("=", 4-pad)
	}
	return base64.StdEncoding.DecodeString(value)
}
func validSSVValue(value string, max int) bool {
	return value != "" && len(value) <= max && !strings.Contains(value, "/") && value != "." && value != ".."
}
func formatKeyID(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return ""
	}
}
func isVerificationProbe(result SSVResult) bool {
	return result.AdNetworkID == "5450213213286189855" && result.AdUnitID == "1234567890" && result.TransactionID == "123456789"
}
