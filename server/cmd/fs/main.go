// Command fs는 Firestore 문서를 조회하는 CLI다.
//
// gcloud에는 Firestore 문서 조회 명령이 없다. export/import/bulk-delete/indexes만 있다.
// 그래서 운영 중 상태를 확인하려면 이 도구가 필요하다.
//
// 조회 전용이다. 쓰기 명령을 만들지 않는다.
// 지급이나 회수는 감사 원장을 남기며 백오피스를 통해야 한다.
// docs/08-ops/BREAK-GLASS.md 참고.
//
// 사용법:
//
//	fs get users/pu_01JXYZ
//	fs ls  users/pu_01JXYZ/entitlements --limit=20
//	fs ls  processed_orders --json
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/seorilabs/platform/server/internal/fspath"
)

// maxListLimit은 실수로 컬렉션 전체를 훑는 걸 막는 상한이다.
// 많은 문서가 필요하면 BigQuery platform.audit를 쓴다.
const maxListLimit = 100

const usage = `fs — Firestore 조회 CLI (읽기 전용)

사용법:
  fs get <path>              문서 1개를 조회한다
  fs ls  <path> [flags]      컬렉션 문서를 나열한다

플래그:
  --project string   GCP 프로젝트. 기본값은 GOOGLE_CLOUD_PROJECT 환경변수
  --prefix string    환경 prefix. staging은 stg_ 를 쓴다. 기본값은 PLATFORM_FS_PREFIX
  --limit int        ls 최대 건수 (기본 20, 최대 100)
  --json             원본 JSON으로 출력한다

예시:
  fs get users/pu_01JXYZ
  fs ls  processed_orders --limit=5 --json
  fs get iap_users/pu_01JXYZ/entitlements/sp_galaxy_gecko
`

// errUsage는 이미 사용법을 출력했다는 표시다.
// main이 메시지를 또 찍지 않도록 구분한다.
var errUsage = errors.New("usage")

func main() {
	// main은 얇게 두고 실제 로직은 run에 둔다.
	// os.Exit는 defer를 실행하지 않으므로 한 곳에서만 부른다. Go 관용구다.
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, errUsage) {
			fmt.Fprintf(os.Stderr, "오류: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errUsage
	}

	cmd, rest := args[0], args[1:]

	switch cmd {
	case "get":
		return runGet(context.Background(), rest)
	case "ls":
		return runList(context.Background(), rest)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("알 수 없는 명령: %s", cmd)
	}
}

type commonFlags struct {
	project string
	prefix  string
	asJSON  bool
}

// bindCommon은 두 명령이 공유하는 플래그를 등록한다.
//
// flag.FlagSet을 명령마다 따로 만든다. 전역 flag.String을 쓰면
// 명령별로 다른 플래그를 둘 수 없고 테스트도 어려워진다.
func bindCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.project, "project", os.Getenv("GOOGLE_CLOUD_PROJECT"), "GCP 프로젝트")
	fs.StringVar(&c.prefix, "prefix", os.Getenv("PLATFORM_FS_PREFIX"), "환경 prefix")
	fs.BoolVar(&c.asJSON, "json", false, "원본 JSON 출력")
	return c
}

// newFlagSet은 flag 자체 출력을 끈 FlagSet을 만든다.
//
// ContinueOnError면 flag가 에러를 stderr에 찍고 에러도 반환한다.
// 그대로 두면 main이 또 찍어 중복되므로 출력을 버리고 우리가 낸다.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseArgs는 경로와 플래그가 섞인 인자를 처리한다.
//
// 표준 flag는 첫 비플래그 인자를 만나면 파싱을 멈춘다.
// 그래서 "ls users --limit=5" 처럼 경로가 앞에 오면 --limit이 무시된다.
// 두 번 파싱해 경로 뒤의 플래그도 읽는다. Go에서 흔히 쓰는 우회다.
func parseArgs(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return "", errors.New("경로가 필요하다")
	}
	path := rest[0]

	// 경로 뒤에 남은 인자를 다시 파싱한다.
	if err := fs.Parse(rest[1:]); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("인자가 너무 많다: %s", strings.Join(fs.Args(), " "))
	}
	return path, nil
}

// resolveTarget은 경로를 검증하고 환경 prefix를 적용한다.
// get과 ls가 기대하는 Kind가 달라 want로 받는다.
func resolveTarget(raw string, c *commonFlags, want fspath.Kind) (fspath.Path, error) {
	p, err := fspath.Parse(raw)
	if err != nil {
		return fspath.Path{}, err
	}
	if p.Kind() != want {
		return fspath.Path{}, fmt.Errorf(
			"%s 경로가 필요한데 %s 경로다: %s",
			want, p.Kind(), raw,
		)
	}

	prefixed, err := p.WithEnvPrefix(c.prefix)
	if err != nil {
		return fspath.Path{}, err
	}
	if c.project == "" {
		return fspath.Path{}, errors.New("--project 또는 GOOGLE_CLOUD_PROJECT 가 필요하다")
	}
	return prefixed, nil
}

func runGet(ctx context.Context, args []string) error {
	fs := newFlagSet("get")
	c := bindCommon(fs)

	raw, err := parseArgs(fs, args)
	if err != nil {
		return err
	}

	p, err := resolveTarget(raw, c, fspath.KindDocument)
	if err != nil {
		return err
	}

	client, err := firestore.NewClient(ctx, c.project)
	if err != nil {
		return fmt.Errorf("Firestore 클라이언트 생성 실패: %w", err)
	}
	defer client.Close()

	snap, err := client.Doc(p.String()).Get(ctx)
	if err != nil {
		// Firestore는 gRPC를 쓰므로 "없음"이 일반 에러로 온다.
		// status.Code로 판정해야 다른 실패와 구분된다.
		if status.Code(err) == codes.NotFound {
			return fmt.Errorf("문서가 없다: %s", p)
		}
		return fmt.Errorf("문서 조회 실패 %s: %w", p, err)
	}

	if c.asJSON {
		return printJSON(snap.Data())
	}
	fmt.Printf("# %s\n", p)
	printFields(snap.Data())
	return nil
}

func runList(ctx context.Context, args []string) error {
	fs := newFlagSet("ls")
	c := bindCommon(fs)
	limit := fs.Int("limit", 20, "최대 건수")

	raw, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if *limit < 1 || *limit > maxListLimit {
		return fmt.Errorf("--limit 은 1 이상 %d 이하여야 한다", maxListLimit)
	}

	p, err := resolveTarget(raw, c, fspath.KindCollection)
	if err != nil {
		return err
	}

	client, err := firestore.NewClient(ctx, c.project)
	if err != nil {
		return fmt.Errorf("Firestore 클라이언트 생성 실패: %w", err)
	}
	defer client.Close()

	// 정렬이나 필터를 붙이지 않는다.
	// Firestore는 인덱스 없는 복합 쿼리가 런타임에 실패하므로
	// 조건 조회는 BigQuery platform.audit로 간다.
	it := client.Collection(p.String()).Limit(*limit).Documents(ctx)
	defer it.Stop()

	count := 0
	for {
		snap, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return fmt.Errorf("컬렉션 순회 실패 %s: %w", p, err)
		}
		count++

		if c.asJSON {
			if err := printJSON(map[string]any{"id": snap.Ref.ID, "data": snap.Data()}); err != nil {
				return err
			}
			continue
		}
		fmt.Printf("# %s\n", snap.Ref.ID)
		printFields(snap.Data())
		fmt.Println()
	}

	if count == 0 {
		fmt.Fprintf(os.Stderr, "문서가 없다: %s\n", p)
		return nil
	}
	if count == *limit {
		// 상한에 걸렸는지 알려준다. 조용한 절단은 "전부 봤다"로 오해된다.
		fmt.Fprintf(os.Stderr, "\n(--limit %d 에 도달했다. 더 있을 수 있다)\n", *limit)
	}
	return nil
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Firestore 값에는 DocumentRef나 LatLng처럼 JSON으로 안 되는 타입이 섞일 수 있다.
		// 조회 도구가 그것 때문에 실패하면 곤란하므로 Go 표현으로 떨어뜨린다.
		fmt.Printf("%+v\n", v)
		return nil
	}
	fmt.Println(string(b))
	return nil
}

// printFields는 키를 정렬해 출력한다.
// Go의 map 순회 순서는 무작위라 정렬하지 않으면 실행할 때마다 순서가 바뀐다.
func printFields(data map[string]any) {
	keys := make([]string, 0, len(data))
	width := 0
	for k := range data {
		keys = append(keys, k)
		if len(k) > width {
			width = len(k)
		}
	}
	sort.Strings(keys)

	for _, k := range keys {
		fmt.Printf("  %-*s  %v\n", width, k, data[k])
	}
}
