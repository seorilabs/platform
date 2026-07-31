# httptest.Server.Close 교착 — 타임아웃 테스트의 함정

P5에서 실제로 물렸다. 테스트가 통과도 실패도 하지 않고 45초 넘게 멈췄다.

## 증상

`go test`가 끝나지 않는다. 스택을 보면 두 goroutine이 서로를 기다린다.

```
goroutine 260 [chan receive]:
	toss_test.go:435            ← 핸들러가 <-r.Context().Done()에서 대기
goroutine 257 [sync.WaitGroup.Wait]:
	net/http/httptest/server.go:279  ← srv.Close()가 핸들러 종료를 대기
```

## 원인

타임아웃을 검증하려고 응답하지 않는 핸들러를 만든다.

```go
srv := httptest.NewServer(func(w http.ResponseWriter, r *http.Request) {
    <-r.Context().Done()   // 클라이언트가 끊으면 풀린다고 기대
})
t.Cleanup(srv.Close)
```

기대는 "클라이언트가 context 취소로 연결을 끊으면 `r.Context()`도 취소된다"이다.
**항상 그렇지는 않다.** 특히 요청 body가 있는 POST에서 핸들러가 body를 읽지
않으면, 서버가 연결 종료를 통보받지 못한다. 핸들러는 영원히 대기하고
`srv.Close()`는 그 핸들러를 기다린다.

같은 코드라도 GET에서는 우연히 통과한다. Play provider 테스트가 먼저
통과해서 이 함정을 못 보고 넘어갔고, AIT(POST)에서 드러났다.

## 해결

테스트가 끝날 때 확실히 풀리는 채널을 하나 더 건다.

```go
release := make(chan struct{})
defer close(release)

srv := httptest.NewServer(func(w http.ResponseWriter, r *http.Request) {
    select {
    case <-r.Context().Done():
    case <-release:
    }
})
t.Cleanup(srv.Close)
```

## 함정 안의 함정 — t.Cleanup은 LIFO다

처음에 `defer` 대신 `t.Cleanup`을 썼다가 여전히 멈췄다.

```go
release := make(chan struct{})
t.Cleanup(func() { close(release) })   // 1번째 등록 → 나중에 실행
v := newFakeVerifier(t, handler)       // 안에서 srv.Close 등록 → 먼저 실행
```

`t.Cleanup`은 **LIFO**다. 나중에 등록된 `srv.Close`가 먼저 돌아
같은 교착에 빠진다.

`defer`는 함수 리턴 시점에 실행되고 그 뒤에 `t.Cleanup`이 돈다.
그래서 **`defer close(release)`가 항상 안전하다.** 등록 순서를 따질 필요가 없다.

## macOS에는 timeout 명령이 없다

진단하다가 한 번 더 헛돌았다. `timeout 90 go test ...`가 `command not found`로
즉시 실패하는데 출력을 `grep`으로 걸러서 "결과 없음"으로 보였다.
Go 자체 플래그를 쓴다.

```bash
go test ./... -timeout 45s   # 초과 시 전체 goroutine 덤프를 찍는다
```

이 덤프가 범인을 정확히 짚어준다.

## 교훈

- 응답하지 않는 핸들러를 만들 때는 **탈출구를 반드시 둔다.**
- 정리 순서가 얽히면 `t.Cleanup`보다 `defer`가 안전하다.
- 한 provider에서 통과한 테스트 패턴이 다른 provider에서도 안전하다는 보장은
  없다. HTTP 메서드와 body 유무가 동작을 바꾼다.

## 관련

- [flag 파싱 함정](02-flag-parsing-trap.md) — 역시 실행해봐야 드러난 문제
