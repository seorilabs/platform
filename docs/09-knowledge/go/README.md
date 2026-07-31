# Go 학습 기록

이 저장소는 **Go 학습이 목적 중 하나**다. 배운 관용구와 밟은 함정을 여기 기록한다.

작성 규칙: 파일 하나에 주제 하나. `context-propagation.md`처럼 이름 짓는다. **왜 그런지**를 남긴다 — 문법은 검색하면 나오지만 왜는 안 나온다.

## 이 저장소가 지키는 Go 규약

`../../../AGENTS.md`에 정본이 있다. 요약:

- 라우팅은 **표준 `net/http`**. Go 1.22+ `ServeMux`가 `POST /v1/inbox/{id}/claim` 패턴을 지원하므로 외부 라우터가 불필요하다
- 테스트는 **표준 `testing` + 테이블 드리븐**. assert 라이브러리 없이
- ORM 없음. Firestore·BigQuery 클라이언트를 repository 포트 뒤에 둔다
- `internal/`을 경계로 쓴다
- **인터페이스는 소비자 쪽에 정의한다.** 구현 패키지가 인터페이스를 export하지 않는다
- 에러는 `errors.Is`/`errors.As`로 판정한다. 문자열 비교 금지
- `context.Context`를 첫 인자로 전파한다

의존성을 최소로 두는 건 절약이자 **관용구 학습**을 위한 선택이다.

## 단계별로 만나는 것

| 단계 | 주제 |
|---|---|
| P0 | 플래그 파싱, 에러 처리 기본 — `cmd/fs` CLI |
| P1 | `net/http` 핸들러, struct 태그, error 래핑, 인터페이스, `context` |
| P2 | goroutine, channel, `sync.WaitGroup`, graceful shutdown |
| P3 | `sync.RWMutex` 캐시, `time` 처리, HTTP 캐시 헤더 |
| P4 | **Firestore 트랜잭션** — `RunTransaction` 재시도 semantics, 낙관적 동시성, `crypto/subtle` 상수시간 비교 |
| P5 | 포트 구현, 외부 HTTP 클라이언트, `tls.Certificate`, ES256/HMAC, `context.WithTimeout` |
| P6 | lease 패턴, 지수 백오프, `errgroup` |

## 기록

아직 없음. P0부터 채운다.
