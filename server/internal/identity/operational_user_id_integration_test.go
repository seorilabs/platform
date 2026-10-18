//go:build integration

package identity

import (
	"github.com/google/uuid"
	"github.com/seorilabs/platform/server/internal/fspath"
	"github.com/seorilabs/platform/server/internal/operational"
	"github.com/seorilabs/platform/server/internal/store"
	"net"
	"os"
	"strings"
	"testing"
)

func TestServiceRuntimeOperationalUserID(t *testing.T) {
	host, _, err := net.SplitHostPort(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("loopback emulator required")
	}
	st, err := store.New(t.Context(), "demo-platform-service-e2e", "uid_"+strings.ReplaceAll(uuid.NewString(), "-", "")+"_")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	repo := NewStoreRepository(st).WithOperationalEvents(operational.NewRepository(st))
	identity := NewIdentity{UID: "synthetic-app-login-user", AuthType: "firebase", Anonymous: true}
	puid, err := repo.EnsureUser(t.Context(), "runtime-fixture", identity)
	if err != nil {
		t.Fatal(err)
	}
	path, err := fspath.Parse("operational_event_outbox/" + operational.StableEventID("identity", "runtime-fixture", puid))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := st.Get(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	var event operational.Event
	if err = snap.DataTo(&event); err != nil {
		t.Fatal(err)
	}
	if event.Attributes["platformUserId"] != puid {
		t.Fatal("server operational ID missing")
	}
	for key, value := range event.Attributes {
		if key == "uid" || key == "appUserId" || value == identity.UID {
			t.Fatal("app login identity leaked")
		}
	}
	repeated, err := repo.EnsureUser(t.Context(), "runtime-fixture", identity)
	if err != nil || repeated != puid {
		t.Fatal("duplicate account changed")
	}
	t.Log("isolated Firestore account and same-transaction outbox contain server operational ID only; existing account retry preserves ID")
}
