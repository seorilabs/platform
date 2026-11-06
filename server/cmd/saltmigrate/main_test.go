package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
)

func TestSaltMigrationRejectsUnfrozenOrMismatchedSourceBeforeWrites(t *testing.T) {
	snapshot := []byte(`{"documents":[]}`)
	digest := sha256.Sum256(snapshot)
	base := manifest{SchemaVersion: 1, AppID: "salt-compass", Environment: domain.EnvProduction, ScanComplete: true, SourceSnapshotSHA256: hex.EncodeToString(digest[:])}
	for _, tc := range []struct {
		name   string
		change func(*manifest)
		ok     bool
	}{
		{"zero-positive-balances", func(*manifest) {}, true},
		{"incomplete-scan", func(m *manifest) { m.ScanComplete = false }, false},
		{"foreign-app", func(m *manifest) { m.AppID = "other" }, false},
		{"source-mismatch", func(m *manifest) { m.SourceSnapshotSHA256 = hex.EncodeToString(make([]byte, 32)) }, false},
		{"invalid-account", func(m *manifest) {
			m.Records = []ledger.SaltLegacyBalance{{PlatformUserID: "foreign", SourceDigest: m.SourceSnapshotSHA256, SourceKind: "pre-iap-cloud-snapshot", CapturedAt: time.Now(), SealCap: 40}}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			m := base
			tc.change(&m)
			raw, _ := json.Marshal(m)
			input, source := filepath.Join(dir, "manifest.json"), filepath.Join(dir, "source.json")
			if err := os.WriteFile(input, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, snapshot, 0600); err != nil {
				t.Fatal(err)
			}
			// --apply와 클라우드 인증 없이 dry-run으로 검증한다.
			err := run([]string{"--input", input, "--source", source})
			if (err == nil) != tc.ok {
				t.Fatalf("validation success=%v want=%v", err == nil, tc.ok)
			}
		})
	}
}
