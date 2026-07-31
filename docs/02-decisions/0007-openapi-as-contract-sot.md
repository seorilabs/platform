# ADR 0007: 계약의 SoT는 OpenAPI다

## Status

Accepted

## Context

플랫폼 API를 소비하는 클라이언트가 셋이다.

- Go 서버 — 핸들러와 타입
- TypeScript SDK — RN, Web, AIT
- **GDScript SDK** — Godot. 코드 생성 도구가 없어 손으로 쓴다

계약을 어디에 둘 것인가.

## Decision

**`spec/openapi.yaml`이 계약의 유일한 source of truth다.**

Go 타입도 TS 타입도 여기서 생성된 **산출물**이지 계약 자체가 아니다.

| 소비자 | 방식 |
|---|---|
| Go | `oapi-codegen` → `server/gen/` 에 생성. **커밋한다** |
| TypeScript | `openapi-typescript` |
| GDScript | 문서 참조 — 손으로 맞추되 conformance 벡터로 행동을 고정 |

### 왜 언어 중립 계약인가

계약을 zod나 Go struct에 두면 **한 언어만 특권적 위치**가 된다. GDScript SDK는 어차피 손으로 맞춰야 하므로, TypeScript를 원본으로 두면 셋 중 둘이 종속되는 비대칭 구조가 된다.

OpenAPI를 원본으로 두면 **셋이 동등하게 참조**한다.

부수 효과로 **서버 언어를 바꿔도 클라이언트가 영향받지 않는다.** 지금은 Go지만 나중에 일부를 다른 언어로 옮겨도 계약은 그대로다. 이건 지금 무료로 확보되는 옵션이다.

## Consequences

- **계약을 바꿀 때는 `spec/openapi.yaml`을 먼저 고친다.** 서버 코드를 먼저 고치고 스펙을 나중에 맞추면 안 된다
- 생성 코드를 **커밋한다.** CI에서 재생성해 diff가 없는지 검사한다. 스펙과 코드가 갈라지는 걸 CI가 잡는다
- **`/v1`은 영구히 깨지지 않는다.** 마켓에 배포된 구버전 SDK가 2~3년 산다. 필드 추가만 허용하고 제거·의미 변경은 `/v2`를 새로 만든다 → R4
- 양방향 forward-compat을 강제한다. 서버는 요청의 미지 필드를 무시하고, SDK는 응답의 미지 필드를 무시한다
- **현재 lizard-tycoon GDScript 클라이언트가 응답 키 개수까지 일치를 요구해 이 규칙을 깬다.** P8에서 완화해야 R4가 성립한다
- OpenAPI로 표현하기 어려운 것(정규화 규칙, 백오프 스케줄)은 **`spec/conformance/*.json` 벡터**로 고정한다. 문서로 적어두면 6개월 뒤 갈라지지만 벡터는 CI가 지킨다

## Alternatives Considered

- **zod를 원본으로** — TypeScript 우선이 되어 GDScript가 2등 시민이 된다. 서버가 Go라 어차피 zod를 서버에서 못 쓴다
- **Go struct를 원본으로** — 반대 방향의 같은 문제. TS와 GDScript가 종속된다
- **Protobuf/gRPC** — 계약이 엄밀해지지만 Godot에 gRPC 클라이언트가 없고, 웹훅 수신은 어차피 HTTP여야 한다
- **계약 문서 없이 코드로** — 3벌이 6개월 안에 갈라진다. 조직에서 이미 실제로 일어난 일이다
