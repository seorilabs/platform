package webhook

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/richzw/appstore"
	"github.com/seorilabs/platform/server/internal/iap/domain"
)

type environmentParser struct {
	payload *appstore.NotificationPayload
	tx      *appstore.JWSTransaction
	err     error
}

func (p *environmentParser) ParseNotification(string) (*appstore.NotificationPayload, error) {
	return p.payload, p.err
}
func (p *environmentParser) ParseTransaction(string) (*appstore.JWSTransaction, error) {
	return p.tx, p.err
}

func TestAppleNotificationEnvironmentRouting(t *testing.T) {
	for _, tc := range []struct {
		name, outer, transaction, bundle, want string
		allowed, badSignature                  bool
	}{
		{"production", "Production", "Production", "test.bundle", "production", true, false},
		{"sandbox", "Sandbox", "Sandbox", "test.bundle", "sandbox", true, false},
		{"sandbox not allowed", "Sandbox", "Sandbox", "test.bundle", "error", false, false},
		{"unknown", "Xcode", "Sandbox", "test.bundle", "error", true, false},
		{"missing", "", "Sandbox", "test.bundle", "error", true, false},
		{"transaction mismatch", "Sandbox", "Production", "test.bundle", "error", true, false},
		{"wrong app", "Sandbox", "Sandbox", "other.bundle", "error", true, false},
		{"signature invalid", "Sandbox", "Sandbox", "test.bundle", "error", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parser := &environmentParser{payload: &appstore.NotificationPayload{
				NotificationUUID: "test-event", NotificationType: "REFUND",
				Data: appstore.NotificationData{Environment: tc.outer, BundleID: tc.bundle, SignedTransactionInfo: "signed-test-transaction"},
			}, tx: &appstore.JWSTransaction{TransactionID: "test-transaction", ProductID: "test.sku", Environment: appstore.Environment(tc.transaction)}}
			if tc.badSignature {
				parser.err = errors.New("invalid test signature")
			}
			prodEvents, sandboxEvents := &fakeEvents{}, &fakeEvents{}
			prodReconciler, sandboxReconciler := &fakeReconciler{}, &fakeReconciler{}
			prodVerifier, sandboxVerifier := &fakeVerifier{out: revokedPurchase()}, &fakeVerifier{out: revokedPurchase()}
			sandbox, err := NewAppleHandler(AppleConfig{Parser: parser, Verifier: sandboxVerifier, Events: sandboxEvents, Reconciler: sandboxReconciler, BundleID: "test.bundle", AppID: "test-app", Environment: domain.EnvSandbox})
			if err != nil {
				t.Fatal(err)
			}
			additional := map[domain.Environment]*AppleHandler{}
			if tc.allowed {
				additional[domain.EnvSandbox] = sandbox
			}
			prod, err := NewAppleHandler(AppleConfig{Parser: parser, Verifier: prodVerifier, Events: prodEvents, Reconciler: prodReconciler, BundleID: "test.bundle", AppID: "test-app", Environment: domain.EnvProduction, Environments: additional})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			prod.Register(mux)
			r := httptest.NewRequest("POST", "/v1/iap/webhooks/apple", strings.NewReader(`{"signedPayload":"signed-test-payload"}`))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if tc.want == "error" {
				if w.Code < 400 || prodVerifier.call+sandboxVerifier.call+prodReconciler.call+sandboxReconciler.call != 0 || len(prodEvents.completed)+len(sandboxEvents.completed) != 0 {
					t.Fatalf("rejected notification changed state: status=%d", w.Code)
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			if tc.want == "production" && (prodVerifier.call != 1 || prodReconciler.call != 1 || sandboxVerifier.call+sandboxReconciler.call != 0 || len(sandboxEvents.completed) != 0) {
				t.Fatal("production crossed environments")
			}
			if tc.want == "sandbox" && (sandboxVerifier.call != 1 || sandboxReconciler.call != 1 || prodVerifier.call+prodReconciler.call != 0 || len(prodEvents.completed) != 0) {
				t.Fatal("sandbox crossed environments")
			}
		})
	}
}
