package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaltLegacyCheckRejectsIncompletePositiveMissingAndCorruptSaves(t *testing.T) {
	for _, tc := range []struct {
		name, body            string
		complete, corrupt, ok bool
	}{
		{"valid-zero", `{"seal":0,"save_version":1}`, true, false, true},
		{"godot-zero-float", `{"seal":0.0,"save_version":1}`, true, false, true},
		{"positive", `{"seal":40,"save_version":1}`, true, false, false},
		{"missing-seal", `{"save_version":1}`, true, false, false},
		{"mock-paid", `{"seal":0,"save_version":1,"receipts":["debug"]}`, true, false, false},
		{"already-bound", `{"seal":0,"save_version":2,"iap_finance":{"uid":"fixture"}}`, true, false, false},
		{"corrupt", `{"seal":0,"save_version":1}`, true, true, false},
		{"incomplete-scan", `{"seal":0,"save_version":1}`, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sum := sha256.Sum256([]byte(tc.body))
			checksum := hex.EncodeToString(sum[:])
			if tc.corrupt {
				checksum = "bad"
			}
			snapshot, _ := json.Marshal([]any{map[string]any{"readTime": "2026-11-06T07:08:58.230201Z", "document": map[string]any{"name": "projects/salt-compass/databases/(default)/documents/players/fixture/saves/0", "fields": map[string]any{"blob": map[string]string{"stringValue": checksum + "\n" + tc.body}}}}})
			digest := sha256.Sum256(snapshot)
			m := manifest{SchemaVersion: 1, AppID: "salt-compass", Environment: "production", ScanComplete: tc.complete, SourceSnapshotSHA256: hex.EncodeToString(digest[:])}
			raw, _ := json.Marshal(m)
			input, source := filepath.Join(dir, "manifest.json"), filepath.Join(dir, "source.json")
			if err := os.WriteFile(input, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, snapshot, 0600); err != nil {
				t.Fatal(err)
			}
			if err := run([]string{"--input", input, "--source", source}); (err == nil) != tc.ok {
				t.Fatalf("accepted=%v expected=%v", err == nil, tc.ok)
			}
			if err := run([]string{"--input", input, "--source", source, "--apply"}); err == nil {
				t.Fatal("read-only checker accepted write flag")
			}
		})
	}
}
