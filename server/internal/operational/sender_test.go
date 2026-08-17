package operational

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSenderSignsSafeEnvelope(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		timestamp := r.Header.Get("X-Seori-Timestamp")
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(timestamp + "."))
		_, _ = mac.Write(body)
		if got, want := r.Header.Get("X-Seori-Signature"), "v1="+hex.EncodeToString(mac.Sum(nil)); got != want {
			t.Fatalf("signature=%q want=%q", got, want)
		}
		var envelope map[string]any
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["version"] != float64(1) || envelope["appId"] != "happy-farm" {
			t.Fatalf("envelope=%v", envelope)
		}
		if _, found := envelope["platformUserId"]; found {
			t.Fatal("platformUserId must not be sent")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	sender, err := NewSender(server.URL, secret, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	sender.now = func() time.Time { return now }
	err = sender.Send(context.Background(), Event{
		EventID: "identity_1234567890", OccurredAt: now, Type: "identity.created",
		AppID: "happy-farm", Outcome: "ok", Attributes: map[string]any{"authType": "firebase"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStableEventIDDoesNotExposeInput(t *testing.T) {
	id := StableEventID("identity", "happy-farm", "pu_sensitive")
	if id == "" || id == "identity_happy-farm_pu_sensitive" {
		t.Fatalf("unsafe id=%q", id)
	}
	if got := StableEventID("identity", "happy-farm", "pu_sensitive"); got != id {
		t.Fatalf("unstable id=%q got=%q", id, got)
	}
}
