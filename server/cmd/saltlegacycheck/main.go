// saltlegacycheck는 동결한 기존 저장이 모두0인지 읽기만 한다. 이관·지급·클라우드 쓰기는 없다.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type manifest struct {
	SchemaVersion        int               `json:"schemaVersion"`
	AppID                string            `json:"appId"`
	Environment          string            `json:"environment"`
	ScanComplete         bool              `json:"scanComplete"`
	SourceSnapshotSHA256 string            `json:"sourceSnapshotSha256"`
	Records              []json.RawMessage `json:"records"`
}

type sourceRow struct {
	ReadTime string `json:"readTime"`
	Document *struct {
		Name   string `json:"name"`
		Fields map[string]struct {
			StringValue string `json:"stringValue"`
		} `json:"fields"`
	} `json:"document"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	f := flag.NewFlagSet("saltlegacycheck", flag.ContinueOnError)
	input, source := f.String("input", "", "동결 manifest"), f.String("source", "", "동결 Firestore query 원본")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *input == "" || *source == "" {
		return errors.New("--input과 --source가 필요합니다")
	}
	raw, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	var m manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return errors.New("동결 manifest 형식을 확인해 주세요")
	}
	if m.SchemaVersion != 1 || m.AppID != "salt-compass" || (m.Environment != "production" && m.Environment != "sandbox") || !m.ScanComplete || len(m.Records) != 0 {
		return errors.New("대상·전체 scan 완료를 확인해 주세요. 계정별 양수 이관은 별도 작업입니다")
	}
	snapshot, err := os.ReadFile(*source)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(snapshot)
	if hex.EncodeToString(digest[:]) != m.SourceSnapshotSHA256 {
		return errors.New("동결 원본 SHA-256이 다릅니다")
	}
	var rows []sourceRow
	if err := json.Unmarshal(snapshot, &rows); err != nil || len(rows) == 0 || len(rows) >= 10000 {
		return errors.New("전체 query 원본과 scan 한도를 확인해 주세요")
	}
	seen, readTime, count := map[string]bool{}, "", 0
	for _, row := range rows {
		if _, err := time.Parse(time.RFC3339Nano, row.ReadTime); err != nil || (readTime != "" && readTime != row.ReadTime) {
			return errors.New("동일 snapshot readTime을 확인해 주세요")
		}
		readTime = row.ReadTime
		if row.Document == nil {
			if len(rows) != 1 {
				return errors.New("저장 문서가 누락됐습니다")
			}
			continue
		}
		d := row.Document
		const prefix = "projects/salt-compass/databases/(default)/documents/"
		parts := strings.Split(strings.TrimPrefix(d.Name, prefix), "/")
		if !strings.HasPrefix(d.Name, prefix) || len(parts) != 4 || parts[0] != "players" || parts[2] != "saves" || parts[1] == "" || parts[3] == "" || seen[d.Name] {
			return errors.New("저장 소유 경로 또는 중복을 확인해 주세요")
		}
		seen[d.Name] = true
		checksum, body, ok := strings.Cut(d.Fields["blob"].StringValue, "\n")
		sum := sha256.Sum256([]byte(body))
		if !ok || checksum != hex.EncodeToString(sum[:]) {
			return errors.New("저장 원본 checksum이 다릅니다")
		}
		var save struct {
			Seal        *float64          `json:"seal"`
			SaveVersion *int              `json:"save_version"`
			Receipts    []json.RawMessage `json:"receipts"`
			Finance     map[string]any    `json:"iap_finance"`
		}
		if err := json.Unmarshal([]byte(body), &save); err != nil || save.Seal == nil || save.SaveVersion == nil || *save.Seal != 0 || len(save.Receipts) != 0 || len(save.Finance) != 0 {
			return errors.New("양수·기연결·미확인 저장은 보존하고 별도 이관 검토가 필요합니다")
		}
		count++
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"dryRun": true, "cloudWrites": false, "documents": count, "zeroBalanceSaves": count, "readTime": readTime, "sourceSnapshotSha256": m.SourceSnapshotSHA256})
}
