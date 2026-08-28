// Command regsync는 앱 레지스트리를 Firestore로 동기화한다.
//
// registry/apps/*.json 이 source of truth다. 이 도구가 Firestore로 밀어넣고
// 런타임은 Firestore에서 읽는다. 컨테이너에 repo가 없기 때문이다.
//
// CI는 --dry-run 검증만 한다. 실제 적용은 사람이 돌린다.
//
// 자동 적용을 하지 않는 이유는 자격증명 범위다. Firestore IAM은 컬렉션
// 단위로 못 쪼갠다. 레지스트리를 쓸 권한을 주면 같은 주체가 IAP 원장도
// 쓸 수 있다. GitHub Actions에서 닿는 신원에 그 권한을 두지 않는다.
// R3(자격증명 격리)와 같은 이유다.
//
// 대신 파일만 고치고 적용을 잊는 사고가 실제로 있었다. registry 파일은
// iap:false인데 앱은 결제가 되고 백오피스 관리만 403이었다.
// Obsidian 프로젝트/platform/06-release/go-live-checklist.md의 절차를 따른다.
//
//	regsync --dir=../registry/apps --project=seorilabs-platform --dry-run
//	regsync --dir=../registry/apps --project=seorilabs-platform
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "오류: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("regsync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	dir := fs.String("dir", "registry/apps", "레지스트리 JSON 디렉토리")
	project := fs.String("project", os.Getenv("GOOGLE_CLOUD_PROJECT"), "GCP 프로젝트")
	prefix := fs.String("prefix", os.Getenv("PLATFORM_FS_PREFIX"), "환경 prefix")
	dryRun := fs.Bool("dry-run", false, "검증만 하고 쓰지 않는다")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" && !*dryRun {
		return errors.New("--project 또는 GOOGLE_CLOUD_PROJECT 가 필요하다")
	}

	// 절대 경로로 바꿔 os.DirFS 기준을 명확히 한다.
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return fmt.Errorf("경로 해석 실패: %w", err)
	}
	root := filepath.Dir(abs)
	base := filepath.Base(abs)

	src := registry.NewFSSource(os.DirFS(root), base)
	apps, err := src.LoadApps(context.Background())
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return fmt.Errorf("%s 에 레지스트리 파일이 없다", abs)
	}

	// 쓰기 전에 개별 앱과 앱 사이의 전역 식별자를 함께 검증한다.
	// 하나라도 잘못됐으면 아무것도 쓰지 않는다. 부분 적용이 더 나쁘다.
	if err := registry.ValidateAppSet(apps); err != nil {
		return fmt.Errorf("검증 실패: %w", err)
	}
	fmt.Printf("검증 통과: %d개\n", len(apps))

	if *dryRun {
		for _, a := range apps {
			fmt.Printf("  [dry-run] %s (%s) status=%s firebase=%s\n",
				a.AppID, a.DisplayName, a.Status, a.FirebaseProjectID)
		}
		return nil
	}

	ctx := context.Background()
	st, err := store.New(ctx, *project, *prefix)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	sink := registry.NewStoreSource(st)
	for _, a := range apps {
		if err := sink.Upsert(ctx, a); err != nil {
			return fmt.Errorf("%s upsert 실패: %w", a.AppID, err)
		}
		fmt.Printf("  upsert %s\n", a.AppID)
	}

	fmt.Printf("동기화 완료: %d개 → %s%s\n", len(apps), *prefix, registry.AppsPath)
	return nil
}
