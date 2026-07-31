# Seorilabs Platform Agent Instructions

## 기본 원칙

- 한글을 주 사용언어로 한다.
- 항상 간결하고 실무적으로 답변한다.
- 애매한 부분은 상상해서 채우지 말고, 파일·로그·설정·실행 결과를 먼저 확인한다.
- 사용자의 말이 사실과 다르거나 기술적으로 부정확하면 바로잡는다.
- 복잡한 구조 설명은 가능하면 Mermaid로 도식화한다.
- 대화 중 장기 지식으로 남길 만한 확인 사실은 문서화한다. 이 레포의 실행 원장은 `docs/`이고 Obsidian은 보조 지식베이스다.

## Source Of Truth

- 기획·의사결정·작업 로그·운영 절차의 원장은 `docs/`다.
- **API 계약의 원장은 `spec/openapi.yaml`이다.** Go 타입도 TS 타입도 여기서 생성된 산출물이지 계약 자체가 아니다. 계약을 바꿀 때는 반드시 openapi.yaml을 먼저 고친다.
- **앱 레지스트리의 원장은 `registry/apps/*.json`이다.** 콘솔이나 Firestore를 직접 수정하지 않는다.
- 새 프로젝트에서 확정해야 하는 값은 `확정 필요`로 남기고 임의로 채우지 않는다.

## Local Overrides

- `AGENTS.local.md`가 있으면 먼저 읽고 개별 지침으로 적용한다. 커밋하지 않는다.

## 작업 방식 — 설명 위주 + 직접 구현

이 저장소는 **Go 학습이 목적 중 하나**다. 에이전트가 전부 구현해버리면 목적이 사라진다.

| 에이전트가 하는 것 | 사용자가 하는 것 |
|---|---|
| 설계·구조·**Go 관용구 설명** | **핸들러·도메인 로직 직접 작성** |
| 패키지 스캐폴드, 인터페이스 정의, `go.mod` | 인터페이스 구현체 채우기 |
| **테이블 드리븐 테스트를 먼저 작성 — 실패 상태로** | 테스트를 통과시키는 구현 |
| 인프라, CI/CD, Dockerfile | — |
| SDK 2벌, 백오피스 확장 | — |
| 코드 리뷰, 관용구 교정, 대안 제시 | — |

- 사용자가 명시적으로 "구현해줘"라고 하지 않는 한, **서버 핸들러와 도메인 로직은 대신 작성하지 않는다.**
- 배운 관용구와 밟은 함정은 `docs/09-knowledge/go/`에 기록한다.

## Go 규약

- Go 1.24+. 라우팅은 **표준 `net/http`** — Go 1.22+ `ServeMux` 패턴으로 충분하므로 외부 라우터를 도입하지 않는다.
- 테스트는 **표준 `testing` + 테이블 드리븐**. assert 라이브러리를 도입하지 않는다.
- ORM을 도입하지 않는다. Firestore·BigQuery 클라이언트를 repository 포트 뒤에 둔다.
- `internal/`을 경계로 쓴다. 외부에서 import되면 안 되는 것은 전부 `internal/` 아래.
- 인터페이스는 **소비자 쪽에 정의**한다. 구현 패키지가 인터페이스를 export하지 않는다.
- 에러는 `errors.Is`/`errors.As`로 판정한다. 문자열 비교 금지.
- `context.Context`를 첫 인자로 전파한다.
- 로깅은 `log/slog` 구조화 JSON.

## 절대 금지

- **토큰·refresh token·영수증·purchaseToken·FCM 토큰 원문을 로그에 남기지 않는다.**
- **마켓 계정 식별자 원문을 저장하지 않는다.** sha256만 저장한다.
- **플랫폼에 PII를 저장하지 않는다.** 이메일·이름·전화번호는 각 앱의 Firebase Auth에만 둔다.
- **IAP 원장 문서를 삭제하지 않는다.** `iap_completion_outbox`만 예외.
- **`/v1` 응답에서 필드를 제거하거나 의미를 바꾸지 않는다.** 추가만 허용. breaking change는 `/v2`.
- Firebase 프로젝트를 이 저장소용으로 등록하지 않는다. → ADR 0002

## IAP 불변식

`docs/03-architecture/iap.md`의 불변식 12개는 **언어·저장소와 무관하게 보존한다.** 이를 바꾸는 변경은 ADR 없이 하지 않는다.

## GitHub Actions / ARC

- Seorilabs GitHub Actions 또는 ARC runner 라우팅을 작성·수정·진단할 때는 `seorilabs-arc-runners` 스킬을 사용한다.
- 먼저 `/Users/syous/Workspace/kubectl/github-actions-runners/global-versions.yaml`을 확인한다.
- action 버전은 GitHub 공식 repo/API 기준 최신 stable major를 확인한다. `@latest`나 branch 참조를 쓰지 않는다.
- **Go는 크로스컴파일이 네이티브**라 ARC arm64 러너에서 `GOOS=linux GOARCH=amd64`로 Cloud Run용 amd64 바이너리를 QEMU 없이 빌드한다. Cloud Build 위임이 불필요하다.
- 아티팩트는 `retention-days: 3`.

## PR

- PR은 Draft가 아닌 Ready 상태로 생성한다.
- 제목과 Description은 한글로 쓴다. 고유명사·명령어·코드·에러 메시지는 원문을 유지할 수 있다.
- 변경 구조나 흐름 이해에 도움이 되면 Mermaid 다이어그램을 포함한다.
- Seorilabs PR 운영은 `seori-pr-workflow` 스킬을 따른다.
- AI 도구가 생성했다는 서명이나 홍보 문구를 커밋·PR·문서에 넣지 않는다.

## 품질 게이트

```bash
golangci-lint run
go vet ./...
go test ./...
```

Firestore 에뮬레이터가 필요한 테스트는 별도 태그로 분리하고 기본 게이트에 넣지 않는다.
