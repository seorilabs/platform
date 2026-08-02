// Command regsync는 앱 레지스트리를 Firestore로 동기화한다.
//
// registry/apps/*.json 이 source of truth다. 이 도구가 Firestore로 밀어넣고
// 런타임은 Firestore에서 읽는다. 컨테이너에 repo가 없기 때문이다.
//
// CI가 registry/** 변경 시 자동으로 부른다. 로컬에서도 쓸 수 있다.
//
//	regsync --dir=../registry/apps --project=seorilabs-platform
//	regsync --dir=../registry/apps --project=seorilabs-platform --dry-run
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

	// 쓰기 전에 전부 검증한다.
	// 하나라도 잘못됐으면 아무것도 쓰지 않는다. 부분 적용이 더 나쁘다.
	for _, a := range apps {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("검증 실패: %w", err)
		}
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
