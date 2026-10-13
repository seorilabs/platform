//go:build integration

package operational

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/store"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceRuntimeOperationalDeliveryRestart(t *testing.T) {
	host, _, err := net.SplitHostPort(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("loopback emulator required")
	}
	st, err := store.New(t.Context(), "demo-platform-service-e2e", "operations_"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	repo := NewRepository(st)
	repo.now = func() time.Time { return now }
	event := Event{EventID: StableEventID("identity", "runtime-fixture", "synthetic-user"), OccurredAt: now, Type: "identity.created", AppID: "runtime-fixture", Outcome: "created", Attributes: map[string]any{"authType": "firebase", "anonymous": true}}
	if err = st.RunTransaction(t.Context(), func(_ctx context.Context, tx *store.Tx) error { return repo.EnqueueTx(tx, event) }); err != nil {
		t.Fatal(err)
	}
	secret := []byte(strings.Repeat("x", 32))
	var attempts atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(io.LimitReader(r.Body, 4096))
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(r.Header.Get("X-Seori-Timestamp") + "."))
		mac.Write(body)
		want := "v1=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Seori-Signature"))) {
			t.Error("invalid signature")
			w.WriteHeader(401)
			return
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer sink.Close()
	sender, err := NewSender(sink.URL, secret, sink.Client())
	if err != nil {
		t.Fatal(err)
	}
	sent, failed, err := NewDispatcher(repo, sender).Drain(t.Context(), 1)
	if err != nil || sent != 0 || failed != 1 {
		t.Fatalf("first drain %d %d %v", sent, failed, err)
	}
	now = now.Add(5 * time.Minute)
	repo = NewRepository(st)
	repo.now = func() time.Time { return now }
	sent, failed, err = NewDispatcher(repo, sender).Drain(t.Context(), 1)
	if err != nil || sent != 1 || failed != 0 {
		t.Fatalf("restart drain %d %d %v", sent, failed, err)
	}
	sent, failed, err = NewDispatcher(NewRepository(st), sender).Drain(t.Context(), 1)
	if err != nil || sent != 0 || failed != 0 || attempts.Load() != 2 {
		t.Fatal("completed event resent")
	}
	t.Log("real Firestore enqueue → signed TCP delivery failure → persisted retry/recreated repository → delivery → no duplicate; Backoffice is a loopback fixture, retry clock advanced")
}
