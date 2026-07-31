// Package binding은 마켓 계정 참조를 만들고 검증한다.
//
// Google과 Apple 신규 구매 전에 클라이언트가 이 값을 받아 마켓 SDK에 넘긴다.
// 마켓이 구매에 그대로 실어 돌려주므로, 검증 시 우리가 발급한 값인지
// 확인해 다른 사용자의 구매를 가로채지 못하게 한다.
//
// 불변식 11. keyring의 첫 항목이 현재 키이고 나머지는 회전 검증용이다.
// 비교는 상수시간으로 한다.
package binding

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"regexp"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/platformerr"
)

// MinKeyLen은 HMAC 키 최소 길이다.
// SHA-256 출력보다 짧으면 보안 강도가 떨어진다.
const MinKeyLen = 32

// 마켓별 유도 문자열.
//
// 같은 사용자라도 마켓마다 다른 값이 나와야 한다.
// 한쪽이 유출돼도 다른 마켓을 사칭할 수 없다.
const (
	googleContext = "google-play:v1:"
	appleContext  = "app-store:v1:"
)

var (
	googlePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	applePattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// Keyring은 HMAC 키 묶음이다.
//
// 첫 항목이 현재 키다. 새 값을 만들 때 이걸 쓰고, 검증할 때는 전부 시도한다.
// 키를 교체해도 이전 키로 발급된 값이 한동안 유효해야 하기 때문이다.
type Keyring struct {
	keys [][]byte
}

// NewKeyring은 키 묶음을 만든다.
func NewKeyring(keys ...[]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("binding: 키가 최소 하나 필요하다")
	}
	if len(keys) > 3 {
		// 회전 중이라도 3개면 충분하다. 많으면 검증 비용만 늘어난다.
		return nil, fmt.Errorf("binding: 키는 3개까지다 (현재 %d)", len(keys))
	}
	for i, k := range keys {
		if len(k) < MinKeyLen {
			return nil, fmt.Errorf("binding: %d번째 키가 %d바이트다. %d 이상이어야 한다",
				i, len(k), MinKeyLen)
		}
	}

	cp := make([][]byte, len(keys))
	for i, k := range keys {
		cp[i] = append([]byte(nil), k...)
	}
	return &Keyring{keys: cp}, nil
}

// GoogleAccountID는 Play용 obfuscatedExternalAccountId를 만든다.
//
// base64url 43자다. Play는 64자까지 받는다.
func (r *Keyring) GoogleAccountID(platformUserID string) string {
	return encodeGoogle(r.mac(r.keys[0], googleContext+platformUserID))
}

// AppleAccountToken은 App Store용 appAccountToken을 만든다.
//
// Apple은 UUID 형식을 요구한다. HMAC 앞 16바이트를 UUIDv5 형태로 맞춘다.
// 실제 UUIDv5는 아니지만 형식 검사를 통과하고 우리에겐 결정적 값이면 충분하다.
func (r *Keyring) AppleAccountToken(platformUserID string) string {
	return encodeApple(r.mac(r.keys[0], appleContext+platformUserID))
}

// VerifyGoogle은 Play 구매의 계정 참조가 이 사용자의 것인지 본다.
func (r *Keyring) VerifyGoogle(platformUserID, provided string) error {
	if provided == "" {
		return platformerr.New(platformerr.CodeAccountBindingMissing,
			"구매에 계정 정보가 없어요")
	}
	return r.verify(googleContext+platformUserID, provided, encodeGoogle)
}

// VerifyApple은 App Store 구매의 계정 참조가 이 사용자의 것인지 본다.
func (r *Keyring) VerifyApple(platformUserID, provided string) error {
	if provided == "" {
		return platformerr.New(platformerr.CodeAccountBindingMissing,
			"구매에 계정 정보가 없어요")
	}
	return r.verify(appleContext+platformUserID, provided, encodeApple)
}

// verify는 모든 키로 시도한다.
//
// 키 회전 중에는 이전 키로 발급된 값이 들어온다.
// 전부 실패해야 거부한다.
func (r *Keyring) verify(context, provided string, encode func([]byte) string) error {
	for _, k := range r.keys {
		expected := encode(r.mac(k, context))
		// 상수시간 비교. 일반 비교는 앞부분이 얼마나 맞는지 시간으로 새어
		// 값을 한 글자씩 알아낼 수 있다.
		if subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1 {
			return nil
		}
	}
	return platformerr.New(platformerr.CodeAccountBindingMismatch,
		"다른 계정에서 시작한 구매예요")
}

func (r *Keyring) mac(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

func encodeGoogle(sum []byte) string {
	return base64.RawURLEncoding.EncodeToString(sum)
}

// encodeApple은 HMAC 앞 16바이트를 UUID 형식으로 만든다.
//
// version을 5로, variant를 RFC 4122로 맞춰 Apple의 형식 검사를 통과시킨다.
func encodeApple(sum []byte) string {
	var b [16]byte
	copy(b[:], sum[:16])

	b[6] = (b[6] & 0x0f) | 0x50 // version 5
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ValidGoogleFormat은 형식만 확인한다. 클라이언트 쪽 사전 검사용이다.
func ValidGoogleFormat(s string) bool { return googlePattern.MatchString(s) }

// ValidAppleFormat은 형식만 확인한다.
func ValidAppleFormat(s string) bool { return applePattern.MatchString(s) }

// RequiresBinding은 이 마켓이 계정 바인딩을 요구하는지 본다.
//
// AIT는 면제다. aitUserKey custom claim 자체가 신뢰 경로이기 때문이다.
// 클라이언트가 body로 보내는 값이 아니라 검증된 토큰에서 나온다.
func RequiresBinding(p domain.Platform) bool {
	switch p {
	case domain.PlatformGooglePlay, domain.PlatformAppStore:
		return true
	default:
		return false
	}
}
