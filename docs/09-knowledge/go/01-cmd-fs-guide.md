# cmd/fs 구현 가이드

P0의 Go 워밍업 과제. **실제로 계속 쓸 도구**를 만들면서 Go 기본기를 익힌다.

## 왜 이걸 먼저 만드는가

`gcloud`에는 **Firestore 문서 조회 명령이 없다.** export/import/bulk-delete/indexes만 있다. 운영 중 상태를 확인하려면 콘솔을 열거나 이 도구를 쓰는 수밖에 없다.

그리고 학습 대상이 적당하다 — 플래그 파싱, 에러 처리, `context`, 외부 클라이언트 lifecycle이 한 번에 나온다. 도메인 로직이 없어 Go 문법에 집중할 수 있다.

## 순서

### 1. `internal/fspath` 부터

`path_test.go`가 **완성된 상태**로 있다. `path.go`의 TODO를 채워 통과시킨다.

```bash
cd server
go test ./internal/fspath/
```

처음에는 전부 실패한다. 정상이다.

순수 함수라 Firestore 없이 테스트된다. **이걸 먼저 하는 이유가 그것**이다 — 네트워크나 자격증명 없이 Go 문법에만 집중할 수 있다.

### 2. `cmd/fs` 채우기

`main.go`의 `runGet`과 `runList` TODO를 채운다.

```bash
export GOOGLE_CLOUD_PROJECT=demo-platform
export FIRESTORE_EMULATOR_HOST=localhost:8080
go run ./cmd/fs get users/pu_test
```

에뮬레이터로 먼저 시험하고 실제 프로젝트는 나중에 붙인다.

## 알아둘 Go 관용구

### 에러는 sentinel + `errors.Is`

```go
var ErrEmptyPath = errors.New("fspath: 경로가 비어 있다")

// 판정
if errors.Is(err, ErrEmptyPath) { ... }
```

**문자열 비교로 에러를 판정하지 않는다.** 메시지가 바뀌면 조용히 깨진다.

에러를 감쌀 때는 `%w`를 쓴다. `%v`로 감싸면 `errors.Is`가 원인을 못 찾는다.

```go
return fmt.Errorf("문서 조회 실패 %s: %w", path, err)
```

### `main`은 얇게, 로직은 `run`에

```go
func main() {
    if err := run(os.Args[1:]); err != nil {
        fmt.Fprintf(os.Stderr, "오류: %v\n", err)
        os.Exit(1)
    }
}
```

**`os.Exit`는 `defer`를 실행하지 않는다.** 여기저기서 부르면 `client.Close()`가 안 돌고 자원이 샌다. 한 곳에서만 부른다.

덤으로 `run`은 인자를 받으므로 테스트할 수 있다.

### `context`는 첫 인자로 전파

```go
func runGet(ctx context.Context, args []string) error
```

Go 컨벤션이다. 구조체 필드에 넣지 않는다.

### `flag.FlagSet`을 명령마다 따로

전역 `flag.String`을 쓰면 명령별로 다른 플래그를 둘 수 없고 테스트도 어렵다.

```go
fs := flag.NewFlagSet("get", flag.ContinueOnError)
```

`flag.ExitOnError`가 아니라 `ContinueOnError`를 쓴다. 파싱 실패도 에러로 돌려받아 한 곳에서 처리하기 위해서다.

### `defer`로 자원 정리

```go
client, err := firestore.NewClient(ctx, project)
if err != nil {
    return fmt.Errorf("Firestore 클라이언트 생성 실패: %w", err)
}
defer client.Close()
```

### gRPC 에러 코드 판정

Firestore는 gRPC를 쓰므로 "없음"이 일반 에러로 온다.

```go
import (
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
)

if status.Code(err) == codes.NotFound {
    return fmt.Errorf("문서가 없다: %s", path)
}
```

### iterator 종료 판정

```go
import "google.golang.org/api/iterator"

for {
    doc, err := it.Next()
    if errors.Is(err, iterator.Done) {
        break
    }
    if err != nil {
        return fmt.Errorf("순회 실패: %w", err)
    }
    // ...
}
```

## 테스트 작성 규칙

`path_test.go`가 이 저장소의 표준 형태다.

- **테이블 드리븐** — 케이스를 구조체 슬라이스로 나열한다
- **`t.Run` 서브테스트** — 실패한 케이스 이름이 바로 보인다
- **`got` / `want` 명명** — Go 커뮤니티 관용구
- **`t.Helper()`** — 헬퍼에서 실패하면 호출한 쪽 줄 번호가 표시된다
- **assert 라이브러리를 쓰지 않는다** — 표준 `testing`만

`t.Fatalf`는 그 서브테스트를 즉시 끝내고, `t.Errorf`는 계속 진행한다. 후속 검사가 의미 없으면 `Fatalf`, 여러 필드를 한 번에 보고 싶으면 `Errorf`다.

## 이 도구가 하지 않는 것

- **쓰기.** 지급·회수는 감사 원장을 남기며 백오피스를 통한다
- **정렬·필터.** Firestore는 인덱스 없는 복합 쿼리가 런타임에 실패한다. 조건 조회는 BigQuery `platform.audit`로 간다
- **집계.** 같은 이유

## 다음

`cmd/fs`가 돌면 P1의 `net/http` 핸들러로 넘어간다. 거기서 인터페이스와 `context` 전파를 본격적으로 쓴다.
