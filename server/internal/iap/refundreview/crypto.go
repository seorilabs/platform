// Package refundreview는 Google Play 환불 검토의 비밀 봉투를 관리한다.
//
// orderId와 pendingRefundToken은 ReviewRefund 호출에만 필요하다. 브라우저,
// 로그, 평문 원장에 퍼지지 않도록 platform-iap에서 봉인하고 worker에서만 연다.
package refundreview

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// Key는 버전형 AES-256-GCM 키다. 첫 키만 신규 암호화에 쓰고 나머지는
// 회전 중인 이전 봉투를 여는 데만 쓴다.
type Key struct {
	ID       string
	Material []byte
}

// Secret은 Google ReviewRefund 외부 호출에 필요한 최소 비밀이다.
type Secret struct {
	OrderID            string `json:"orderId"`
	PendingRefundToken string `json:"pendingRefundToken"`
}

// Submission은 worker가 Google provider에 넘기는 immutable 요청이다.
// 선택적 사용량·PII 증거 필드는 구조상 표현할 수 없다.
type Submission struct {
	PackageName           string
	OrderID               string
	PendingRefundToken    string
	RefundPreference      string
	SampleContentProvided bool
}

// Binding은 ciphertext를 정확한 앱·원장 문서에 묶는 AAD다.
type Binding struct {
	ReviewID    string
	AppID       string
	PackageName string
	OrderIDHash string
	Environment string
}

// Envelope는 Firestore에 저장하는 비밀 봉투다.
type Envelope struct {
	KeyID      string `firestore:"keyId"`
	Nonce      string `firestore:"nonce"`
	Ciphertext string `firestore:"ciphertext"`
}

// Keyring은 현재 키와 회전 검증용 이전 키를 가진다.
type Keyring struct {
	current Key
	keys    map[string][]byte
	random  io.Reader
}

// NewKeyring은 정확히 32바이트인 AES-256 키만 받는다.
func NewKeyring(keys ...Key) (*Keyring, error) {
	if len(keys) == 0 || len(keys) > 3 {
		return nil, platformerr.New(platformerr.CodeSecretConfigInvalid,
			"환불 검토 암호화 키는 1~3개가 필요해요")
	}
	seen := make(map[string][]byte, len(keys))
	for _, key := range keys {
		if !keyIDPattern.MatchString(key.ID) || len(key.Material) != 32 {
			return nil, platformerr.New(platformerr.CodeSecretConfigInvalid,
				"환불 검토 암호화 키 형식이 올바르지 않아요")
		}
		if _, exists := seen[key.ID]; exists {
			return nil, platformerr.New(platformerr.CodeSecretConfigInvalid,
				"환불 검토 암호화 keyId가 중복됐어요")
		}
		seen[key.ID] = append([]byte(nil), key.Material...)
	}
	return &Keyring{
		current: Key{ID: keys[0].ID, Material: append([]byte(nil), keys[0].Material...)},
		keys:    seen,
		random:  rand.Reader,
	}, nil
}

// Seal은 비밀을 현재 키로 봉인한다.
func (k *Keyring) Seal(secret Secret, binding Binding) (Envelope, error) {
	if k == nil || secret.OrderID == "" || secret.PendingRefundToken == "" {
		return Envelope{}, platformerr.New(platformerr.CodeRequestInvalid,
			"환불 검토 비밀이 올바르지 않아요")
	}
	if err := validateBinding(binding); err != nil {
		return Envelope{}, err
	}

	raw, err := json.Marshal(secret)
	if err != nil {
		return Envelope{}, platformerr.Wrap(err, platformerr.CodeInternal,
			"환불 검토 비밀을 준비하지 못했어요")
	}
	defer clear(raw)
	aead, err := newAEAD(k.current.Material)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(k.random, nonce); err != nil {
		return Envelope{}, platformerr.Wrap(err, platformerr.CodeInternal,
			"환불 검토 nonce를 만들지 못했어요")
	}
	ciphertext := aead.Seal(nil, nonce, raw, additionalData(binding))
	return Envelope{
		KeyID:      k.current.ID,
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}, nil
}

// Open은 봉투의 keyId로 키를 고르고 AAD를 대조해 비밀을 연다.
func (k *Keyring) Open(envelope Envelope, binding Binding) (Secret, error) {
	if k == nil {
		return Secret{}, platformerr.New(platformerr.CodeSecretConfigInvalid,
			"환불 검토 복호화 키가 없어요")
	}
	if err := validateBinding(binding); err != nil {
		return Secret{}, err
	}
	material, ok := k.keys[envelope.KeyID]
	if !ok {
		return Secret{}, platformerr.New(platformerr.CodeSecretConfigInvalid,
			"환불 검토 봉투의 keyId를 찾을 수 없어요")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return Secret{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"환불 검토 nonce가 올바르지 않아요")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return Secret{}, platformerr.New(platformerr.CodeLedgerStateInvalid,
			"환불 검토 ciphertext가 올바르지 않아요")
	}
	aead, err := newAEAD(material)
	if err != nil {
		return Secret{}, err
	}
	raw, err := aead.Open(nil, nonce, ciphertext, additionalData(binding))
	if err != nil {
		return Secret{}, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
			"환불 검토 봉투를 검증하지 못했어요")
	}
	defer clear(raw)
	var secret Secret
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&secret); err != nil || secret.OrderID == "" || secret.PendingRefundToken == "" {
		if err == nil {
			err = errors.New("required field missing")
		}
		return Secret{}, platformerr.Wrap(err, platformerr.CodeLedgerStateInvalid,
			"환불 검토 비밀 형식이 올바르지 않아요")
	}
	return secret, nil
}

// ReviewID는 pending token 원문 대신 쓰는 영구 식별자다.
func ReviewID(token string) string { return hash(token) }

// OrderIDHash는 Google order ID 원문 대신 대조에 쓰는 값이다.
func OrderIDHash(orderID string) string { return hash(orderID) }

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func newAEAD(material []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeSecretConfigInvalid,
			"환불 검토 암호화 키가 올바르지 않아요")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, platformerr.Wrap(err, platformerr.CodeSecretConfigInvalid,
			"환불 검토 암호화 설정이 올바르지 않아요")
	}
	return aead, nil
}

func validateBinding(binding Binding) error {
	if binding.ReviewID == "" || binding.AppID == "" || binding.PackageName == "" ||
		binding.OrderIDHash == "" || binding.Environment == "" {
		return platformerr.New(platformerr.CodeRequestInvalid,
			"환불 검토 암호화 binding이 올바르지 않아요")
	}
	return nil
}

func additionalData(binding Binding) []byte {
	return []byte(fmt.Sprintf("v1\x00%s\x00%s\x00%s\x00%s\x00%s",
		binding.ReviewID, binding.AppID, binding.PackageName,
		binding.OrderIDHash, binding.Environment))
}
