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
	"flag"
	"fmt"
	"os"
)

const usage = `fs — Firestore 조회 CLI (읽기 전용)

사용법:
  fs get <path>              문서 1개를 조회한다
  fs ls  <path> [flags]      컬렉션 문서를 나열한다

플래그:
  --project string   GCP 프로젝트. 기본값은 GOOGLE_CLOUD_PROJECT 환경변수
  --prefix string    환경 prefix. staging은 stg_ 를 쓴다
  --limit int        ls 최대 건수 (기본 20)
  --json             원본 JSON으로 출력한다

예시:
  fs get users/pu_01JXYZ
  fs ls  processed_orders --limit=5 --json
  fs get iap_users/pu_01JXYZ/entitlements/sp_galaxy_gecko
`

func main() {
	// main은 얇게 두고 실제 로직은 run에 둔다.
	// os.Exit는 defer를 실행하지 않으므로 한 곳에서만 부른다. Go 관용구다.
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "오류: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("명령이 필요하다")
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

// runGet은 문서 1개를 조회해 출력한다.
//
// TODO(직접 구현):
//  1. flag.NewFlagSet("get", flag.ContinueOnError)로 플래그를 파싱한다
//  2. fs.Arg(0)로 경로를 받고 비어 있으면 에러
//  3. fspath.Parse로 검증하고 Kind가 KindDocument가 아니면 에러
//  4. WithEnvPrefix로 prefix를 적용한다
//  5. firestore.NewClient로 클라이언트를 만든다. defer client.Close()
//  6. client.Doc(path).Get(ctx)로 조회한다
//  7. 없으면 status.Code(err) == codes.NotFound를 판정해 친절한 메시지를 낸다
//  8. --json이면 snap.Data()를 json.MarshalIndent로, 아니면 키를 정렬해 표로 출력한다
//
// 주의: 토큰·영수증·purchaseToken 같은 값이 문서에 있을 수 있다.
// 출력은 사람이 보는 화면이므로 그대로 찍되, 이 도구의 출력을 로그로 남기지 않는다.
func runGet(ctx context.Context, args []string) error {
	return fmt.Errorf("get이 아직 구현되지 않았다")
}

// runList는 컬렉션 문서를 나열한다.
//
// TODO(직접 구현):
//  1. get과 같은 방식으로 플래그와 경로를 처리한다
//  2. Kind가 KindCollection이 아니면 에러
//  3. --limit 기본 20, 상한 100을 넘으면 에러. 실수로 전체를 훑지 않게 한다
//  4. client.Collection(path).Limit(n).Documents(ctx)로 순회한다
//  5. iterator.Done을 errors.Is로 판정해 루프를 끝낸다
//  6. 문서 ID와 주요 필드를 한 줄씩 출력한다
//
// 주의: Firestore는 인덱스 없는 복합 쿼리가 런타임에 실패한다.
// 이 도구는 정렬이나 필터를 붙이지 않고 단순 나열만 한다.
// 집계나 조건 조회가 필요하면 BigQuery platform.audit를 쓴다.
func runList(ctx context.Context, args []string) error {
	return fmt.Errorf("ls가 아직 구현되지 않았다")
}
