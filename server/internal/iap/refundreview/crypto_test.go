package refundreview

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

func testBinding() Binding {
	return Binding{
		ReviewID: strings.Repeat("a", 64), AppID: "lizard-tycoon",
		PackageName: "com.seorilabs.lizardtycoon", OrderIDHash: strings.Repeat("b", 64),
		Environment: "sandbox",
	}
}

func TestSealOpenAndKeyRotation(t *testing.T) {
	old := Key{ID: "2026-07", Material: []byte(strings.Repeat("o", 32))}
	current := Key{ID: "2026-08", Material: []byte(strings.Repeat("n", 32))}
	oldRing, err := NewKeyring(old)
	if err != nil {
		t.Fatal(err)
	}
	secret := Secret{OrderID: "GPA.1234", PendingRefundToken: "pending-token"}
	envelope, err := oldRing.Seal(secret, testBinding())
	if err != nil {
		t.Fatal(err)
	}

	rotated, err := NewKeyring(current, old)
	if err != nil {
		t.Fatal(err)
	}
	got, err := rotated.Open(envelope, testBinding())
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("Open() = %#v, want %#v", got, secret)
	}

	newEnvelope, err := rotated.Seal(secret, testBinding())
	if err != nil {
		t.Fatal(err)
	}
	if newEnvelope.KeyID != current.ID {
		t.Fatalf("신규 봉투 keyId = %q", newEnvelope.KeyID)
	}
}

func TestOpenRejectsMovedOrTamperedEnvelope(t *testing.T) {
	ring, err := NewKeyring(Key{ID: "k1", Material: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := ring.Seal(Secret{OrderID: "order", PendingRefundToken: "token"}, testBinding())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("다른 문서 binding", func(t *testing.T) {
		binding := testBinding()
		binding.AppID = "other-app"
		_, err := ring.Open(envelope, binding)
		if platformerr.CodeOf(err) != platformerr.CodeLedgerStateInvalid {
			t.Fatalf("code = %s, want ledger_state_invalid", platformerr.CodeOf(err))
		}
	})

	t.Run("ciphertext 변조", func(t *testing.T) {
		tampered := envelope
		raw, err := base64.RawURLEncoding.DecodeString(tampered.Ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		raw[0] ^= 0xff
		tampered.Ciphertext = base64.RawURLEncoding.EncodeToString(raw)
		_, err = ring.Open(tampered, testBinding())
		if platformerr.CodeOf(err) != platformerr.CodeLedgerStateInvalid {
			t.Fatalf("code = %s, want ledger_state_invalid", platformerr.CodeOf(err))
		}
	})
}

func TestKeyringRejectsWeakOrDuplicateKeys(t *testing.T) {
	tests := []struct {
		name string
		keys []Key
	}{
		{"없음", nil},
		{"짧은 키", []Key{{ID: "k", Material: []byte("short")}}},
		{"잘못된 ID", []Key{{ID: "bad id", Material: []byte(strings.Repeat("k", 32))}}},
		{"중복 ID", []Key{
			{ID: "k", Material: []byte(strings.Repeat("a", 32))},
			{ID: "k", Material: []byte(strings.Repeat("b", 32))},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewKeyring(tt.keys...); platformerr.CodeOf(err) != platformerr.CodeSecretConfigInvalid {
				t.Fatalf("code = %s, want secret_config_invalid", platformerr.CodeOf(err))
			}
		})
	}
}

func TestReviewHashesDoNotExposeRawValues(t *testing.T) {
	if got := ReviewID("token"); len(got) != 64 || strings.Contains(got, "token") {
		t.Fatalf("ReviewID = %q", got)
	}
	if ReviewID("token") == OrderIDHash("order") {
		t.Fatal("서로 다른 원문 해시가 같다")
	}
}
