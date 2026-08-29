# Seorilabs Platform Agent Instructions

## 기본 원칙

- 한글을 주 사용언어로 한다.
- 항상 간결하고 실무적으로 답변한다.
- 애매한 부분은 상상해서 채우지 말고, 파일·로그·설정·실행 결과를 먼저 확인한다.
- 사용자의 말이 사실과 다르거나 기술적으로 부정확하면 바로잡는다.
- 복잡한 구조 설명은 가능하면 Mermaid로 도식화한다.
- 대화 중 장기 지식으로 남길 만한 확인 사실은 문서화한다. 이 레포의 실행 원장은 Obsidian vault의 `프로젝트/platform/`이다. 레포에는 코드와 계약만 둔다.

## Source Of Truth

- 기획·의사결정·작업 로그·운영 절차의 원장은 Obsidian `프로젝트/platform/`이다. 레포에 문서 디렉토리를 다시 만들지 않는다.
- **API 계약의 원장은 `spec/openapi.yaml`이다.** Go 타입도 TS 타입도 여기서 생성된 산출물이지 계약 자체가 아니다. 계약을 바꿀 때는 반드시 openapi.yaml을 먼저 고친다.
- **앱 레지스트리의 원장은 `registry/apps/*.json`이다.** 콘솔이나 Firestore를 직접 수정하지 않는다.
- 새 프로젝트에서 확정해야 하는 값은 `확정 필요`로 남기고 임의로 채우지 않는다.

## Local Overrides

- `AGENTS.local.md`가 있으면 먼저 읽고 개별 지침으로 적용한다. 커밋하지 않는다.

## 작업 방식 — 설계 합의 후 에이전트 구현, 사용자 리뷰

**에이전트가 코드를 작성하고 사용자가 리뷰한다.**

### 단, 코드보다 설계가 먼저다

**새 패키지·계층·경계를 도입할 때는 코드를 쓰기 전에 설계를 문서로 제시하고 합의한다.** 구조를 먼저 정하지 않으면 리뷰가 "이미 쓰인 코드를 되돌리는 일"이 되어 비용이 커진다.

- 패키지 배치와 의존성 규칙은 Obsidian `프로젝트/platform/03-architecture/server-layout.md`에 누적한다
- 되돌리기 어려운 결정은 ADR로 남긴다
- 기존 구조 안에서의 구현은 논의 없이 진행해도 된다

### 리뷰 가능한 코드를 만드는 규칙

사용자가 Go에 익숙해지는 중이므로, 리뷰가 실제로 가능해야 한다.

- **커밋을 작게 나눈다.** 한 커밋에 한 관심사
- **왜를 주석으로 남긴다.** 무엇을 하는지는 코드가 말하지만 왜 그렇게 했는지는 아니다
- **불변식과 연결한다.** IAP 관련 코드는 Obsidian `프로젝트/platform/03-architecture/iap.md`의 몇 번 불변식인지 주석에 적는다
- **관용구가 낯설 만한 곳은 설명을 붙인다.** 암묵적 인터페이스 만족, `errors.As`, `defer`와 `os.Exit`의 관계 등
- 새로 쓴 관용구나 밟은 함정은 Obsidian `프로젝트/platform/09-knowledge/go/`에 기록한다

### 여전히 에이전트가 하지 않는 것

- 사용자가 결정할 사안을 임의로 정하지 않는다. 특히 저장소 선택, 리전, 계약 형식, 원장 스키마
- 불변식을 바꾸는 변경을 ADR 없이 하지 않는다

## Go 규약

- Go 1.25. 라우팅은 **표준 `net/http`** — Go 1.22+ `ServeMux` 패턴으로 충분하므로 외부 라우터를 도입하지 않는다.
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

Obsidian `프로젝트/platform/03-architecture/iap.md`의 불변식 12개는 **언어·저장소와 무관하게 보존한다.** 이를 바꾸는 변경은 ADR 없이 하지 않는다.

## GitHub Actions

- 저장소는 public이다. CI와 배포는 GitHub-hosted `ubuntu-latest`에서 돈다. self-hosted ARC 러너는 `presence-edge`의 `deploy` job에만 남아 있다 — RPI k8s API가 클러스터 밖으로 열려 있지 않기 때문이다. 그 job을 다룰 때만 `seorilabs-arc-runners` 스킬을 쓴다.
- `pull_request`로 트리거되는 워크플로에 self-hosted 러너를 넣지 않는다. fork PR 코드가 클러스터에서 실행된다.
- 컨테이너 이미지는 러너에서 buildx로 만든다. Cloud Build 위임은 없다. arm64 대상은 QEMU가 아니라 `FROM --platform=$BUILDPLATFORM` + `GOARCH=$TARGETARCH` 크로스컴파일이다. QEMU로 Go 컴파일러를 돌리면 segfault가 난다.
- action 버전은 GitHub 공식 repo/API 기준 최신 stable major를 확인하고 SHA로 고정한다. `@latest`나 branch 참조를 쓰지 않는다.
- `main` 병합은 곧 production 배포 파이프라인 시작이다. `deploy` job은 environment `production`의 required reviewer 승인에서 멈춘다.
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
