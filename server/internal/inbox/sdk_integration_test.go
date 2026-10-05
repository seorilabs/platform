//go:build integration

package inbox

import (
	"context"
	"fmt"
	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/identity"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type sdkFixtureSessions struct{}

func (sdkFixtureSessions) Authenticate(r *http.Request) (identity.Session, error) {
	if r.Header.Get("Authorization") != "Bearer fixture-session" {
		return identity.Session{}, platformerr.New(platformerr.CodeAuthRequired, "fixture session required")
	}
	return identity.Session{AppID: "sdk-fixture", PlatformUserID: "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV"}, nil
}
func TestInboxSDKIntegration(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local emulator required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, e := store.New(ctx, "demo-outgame", "qa_")
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	l := ledger.NewForApp(st, domain.EnvSandbox, fmt.Sprintf("sdk-%d", time.Now().UnixNano()))
	_, e = l.IssueInbox(ctx, ledger.InboxIssue{RequestID: "welcome", PlatformUserID: "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV", Title: "Welcome", Body: "SDK fixture", Rewards: []ledger.InboxReward{{Kind: "entitlement", EntitlementID: "starter", Quantity: 1}}, Actor: "test", Reason: ledger.AdminReasonInternalValidation})
	if e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	NewHandler(sdkFixtureSessions{}, appsStub{true}, func(registry.App) Mailbox { return l }).Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	binary := os.Getenv("GODOT_BIN")
	if binary == "" {
		binary = "godot"
	}
	command := exec.CommandContext(ctx, binary, "--headless", "--path", "../../../sdk-gdscript", "--script", "addons/seorilabs_platform/tools/inbox_probe.gd", "--", server.URL)
	output, e := command.CombinedOutput()
	if e != nil || strings.Contains(string(output), "ERROR:") || !strings.Contains(string(output), "integration passed") {
		t.Fatalf("SDK integration: %v\n%s", e, output)
	}
	t.Log(string(output))
	ent, e := l.IsActive(ctx, "pu_01ARZ3NDEKTSV4RRFFQ69G5FAV", "starter")
	if e != nil || !ent {
		t.Fatalf("grant projection: %v %v", ent, e)
	}
}
