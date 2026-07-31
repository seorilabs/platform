# Conformance 벡터

**TS SDK와 GDScript SDK가 동일하게 통과해야 하는 테스트 벡터.**

OpenAPI로는 표현할 수 없는 **행동 규약**을 여기서 고정한다. 정규화 규칙, 재시도 스케줄, envelope 파싱이 그 대상이다.

## 왜 필요한가

조직에서 **실제로 일어난 일**이다.

| 구현 | boolean 처리 |
|---|---|
| happy-farm `measurementProtocol.ts` | `1/0` |
| vocab-swipe `ga4MeasurementProtocol.ts` | `"true"/"false"` |
| lizard-tycoon `ga4_mp_sender.gd` | **정규화 없음** — GDScript `true`가 JSON `true`로 나가 GA4가 조용히 거부 |

같은 목적의 코드 3벌이 **동작이 다른 상태로** 갈라졌다. 게다가 조용히 실패해서 한참 뒤에나 발견됐다.

> **문서로 적어두면 6개월 뒤 갈라진다. 벡터는 CI가 지킨다.**

## 파일

| 파일 | 대상 |
|---|---|
| `param-normalization.json` | 이벤트 파라미터 정규화 |
| `backoff.json` | 재시도 백오프 스케줄 |
| `envelope.json` | 응답 envelope 파싱 |

## 실행

**TypeScript**

```bash
node --test packages/sdk-ts/test/conformance.test.ts
```

**GDScript**

```bash
godot --headless --script sdk-gdscript/tools/conformance_probe.gd
```

`extends SceneTree` 패턴을 쓴다. lizard-tycoon의 `tools/core_probe.gd`, `tools/iap_client_probe.gd`가 참조 구현이다.

**두 언어 CI 모두에서 게이트로 건다.** 한쪽만 통과하는 상태를 허용하지 않는다.

## 벡터를 추가할 때

1. 먼저 여기에 케이스를 추가한다
2. 양쪽 SDK가 실패하는 것을 확인한다
3. 양쪽을 고친다

**구현을 먼저 고치고 벡터를 나중에 맞추지 않는다.** 그러면 벡터가 구현을 따라가는 문서가 되어 존재 의미가 사라진다.
