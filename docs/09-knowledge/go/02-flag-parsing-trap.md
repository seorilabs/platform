# 표준 flag는 첫 비플래그 인자에서 멈춘다

`cmd/fs`를 만들면서 실제로 밟은 함정. **테스트로 잡히지 않고 조용히 기본값을 쓴다.**

## 증상

```bash
fs ls users --limit=500
```

`--limit` 상한 검사(1~100)가 있는데도 통과했다. 500이 아니라 **기본값 20**이 쓰였기 때문이다.

```go
limit := fs.Int("limit", 20, "최대 건수")
fs.Parse(args)              // args = ["users", "--limit=500"]
// → "users"를 만나 멈춘다. --limit은 파싱되지 않는다.
if *limit > 100 { ... }     // *limit == 20 이라 통과
```

## 원인

Go 표준 `flag`는 **첫 비플래그 인자를 만나면 파싱을 중단**한다. 남은 것은 `fs.Args()`로 넘어간다.

`ls -l /tmp`처럼 플래그가 앞에 오는 유닉스 관행을 전제한 설계다. `git commit -m` 같은 서브커맨드 CLI도 대개 플래그를 먼저 받는다.

**조용한 실패라 위험하다.** 에러도 경고도 없고 기본값이 그대로 쓰인다. `--limit`처럼 안전 상한을 거는 플래그에서는 상한이 무력화된다.

## 해결 — 두 번 파싱

```go
func parseArgs(fs *flag.FlagSet, args []string) (string, error) {
    if err := fs.Parse(args); err != nil {
        return "", err
    }

    rest := fs.Args()          // 비플래그에서 멈춘 뒤 남은 것
    if len(rest) == 0 {
        return "", errors.New("경로가 필요하다")
    }
    path := rest[0]

    // 경로 뒤에 남은 인자를 다시 파싱한다
    if err := fs.Parse(rest[1:]); err != nil {
        return "", err
    }
    if fs.NArg() > 0 {
        return "", fmt.Errorf("인자가 너무 많다: %s", strings.Join(fs.Args(), " "))
    }
    return path, nil
}
```

`FlagSet`은 여러 번 `Parse`해도 된다. 두 번째 호출이 앞선 값을 덮어쓰지 않고 이어서 채운다.

덤으로 `fs.NArg() > 0` 검사가 오타를 잡는다. `fs get users/pu_1 extra`가 조용히 무시되지 않는다.

## 대안과 선택 이유

| 안 | 평가 |
|---|---|
| 사용법을 `fs ls --limit=5 users`로 강제 | 문서만 고치면 되지만 사용자가 틀린 순서로 쓰면 **여전히 조용히 실패**한다 |
| **두 번 파싱** | 두 순서를 다 지원하고 오타도 잡는다. **채택** |
| `cobra` 같은 라이브러리 도입 | 이 CLI에 과하다. 표준 라이브러리만 쓰는 원칙과도 어긋난다 |

## 교훈

**기본값이 있는 플래그로 안전 상한을 걸 때는 파싱이 실제로 됐는지 확인한다.** 상한 검사가 있다는 사실만으로 안심하면 안 된다.

이 버그는 단위 테스트로 잡히지 않았다. `parseArgs`가 없던 시점에는 검사할 함수 자체가 없었고, CLI 전체를 실행하는 스모크 테스트에서야 드러났다. **CLI는 실제로 실행해봐야 한다.**
