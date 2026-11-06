// Command saltmigrate는 비공개 동결 snapshot의 Salt 인장 기준값만 create-only 이관한다.
// 앱 registry 활성화 전에 원본 해시·대상 계정·전체 scan 완료를 별도로 검증한다.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/ledger"
	"github.com/seorilabs/platform/server/internal/store"
)

type manifest struct {
	SchemaVersion        int                        `json:"schemaVersion"`
	AppID                string                     `json:"appId"`
	Environment          domain.Environment         `json:"environment"`
	ScanComplete         bool                       `json:"scanComplete"`
	SourceSnapshotSHA256 string                     `json:"sourceSnapshotSha256"`
	Records              []ledger.SaltLegacyBalance `json:"records"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	f := flag.NewFlagSet("saltmigrate", flag.ContinueOnError)
	input := f.String("input", "", "비공개 동결 manifest")
	source := f.String("source", "", "동결 원본 파일")
	project := f.String("project", "seorilabs-platform", "기존 Platform 프로젝트")
	prefix := f.String("prefix", "", "격리 검수 prefix")
	apply := f.Bool("apply", false, "기준값 생성; 기본값은 dry-run")
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
	if err := json.Unmarshal(raw, &m); err != nil {
		return errors.New("이관 manifest JSON을 확인해 주세요")
	}
	if m.SchemaVersion != 1 || m.AppID != "salt-compass" || !m.Environment.Valid() || !m.ScanComplete || len(m.SourceSnapshotSHA256) != 64 {
		return errors.New("대상 앱·환경·동결 scan 완료 여부를 확인해 주세요")
	}
	snapshot, err := os.ReadFile(*source)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(snapshot)
	if hex.EncodeToString(digest[:]) != m.SourceSnapshotSHA256 {
		return errors.New("동결 원본 SHA-256이 다릅니다")
	}
	seen := map[string]bool{}
	for _, r := range m.Records {
		if seen[r.PlatformUserID] || r.Validate() != nil || r.SourceDigest != m.SourceSnapshotSHA256 || r.CapturedAt.After(time.Now().Add(time.Minute)) {
			return errors.New("이관 계정 중복 또는 기준값 형식을 확인해 주세요")
		}
		seen[r.PlatformUserID] = true
	}
	if *apply {
		ctx := context.Background()
		st, err := store.New(ctx, *project, *prefix)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		l := ledger.NewForApp(st, m.Environment, m.AppID)
		for _, r := range m.Records {
			if err := l.ImportSaltLegacy(ctx, r); err != nil {
				return err
			}
		}
	}
	// 개별 사용자 ID와 저장 내용은 로그에 출력하지 않는다.
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"dryRun": !*apply, "records": len(m.Records), "environment": m.Environment, "sourceSnapshotSha256": m.SourceSnapshotSHA256})
}
