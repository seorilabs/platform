# Events

## 입장 — GA4를 대체하지 않는다

**기존 GA4 전송을 유지하면서 저빈도 핵심 이벤트만 플랫폼에 복제한다.**

```
앱 analytics tracker
   ├─ sink A: 기존 GA4 또는 AIT Measurement Protocol
   └─ sink B: 플랫폼 ingest - 앱별 allowlist만
```

| 빌드 | sink A | sink B | 결과 |
|---|---|---|---|
| Godot 네이티브 · RN | O | O | **GA4 geo 그대로. 회귀 0** |
| **AIT** | **O — 기존 직접 전송 유지** | O | **1차 연동은 dual sink** |

플랫폼의 AIT GA4 서버 릴레이는 아직 구현되지 않았다. 릴레이가 구현·검증되기
전에는 기존 Measurement Protocol과 비밀값 검증 게이트를 제거하지 않는다.

## 인증된 이벤트 경계

`platform-ingest`는 세션이 없는 이벤트도 기존 호환을 위해 수락하지만, 유효한
`Authorization: Bearer <platformToken>`이 있으면 `platform_user_id`를 반드시 붙인다.
이를 위해 세션 발급 role과 동일한 `platform-session-secret`을 ingest runtime에
resource-level로 마운트한다. secret이 없으면 인증 이벤트가 오류 없이 익명 적재되므로
ingest는 부팅 단계에서 실패하고 배포 workflow가 secret 참조를 readback한다.

앱은 세션 확립 전 이벤트를 보내지 않는다. 운영 스모크는 응답의 `accepted`만 보지 않고
BigQuery의 해당 `event_id` 행에서 `platform_user_id IS NOT NULL`까지 확인한다.

## 순수 릴레이를 채택하지 않은 이유

**GA4 Measurement Protocol은 요청 IP로 geo를 판정한다.**

플랫폼이 전부 릴레이하면 모든 트래픽의 국가가 `asia-northeast3`로 붙는다. 이는 백오피스 `AppMetricDaily.raw`의 국가·기기·OS top-N 분해를 **조용히 망가뜨린다.** 값이 틀렸다는 신호가 없어 한참 뒤에나 발견된다.

AIT만 릴레이하는 건 **AIT 트래픽이 어차피 100% 한국**이라 실질 손실이 없다.

`ip_override` 지원 여부는 P0 확인 항목이지만, **미지원을 가정하고 설계**해야 안전하다.

## 이 구조가 부수적으로 해결하는 문제 2개

1. **AIT의 `api_secret` 번들 인라인 문제.** happy-farm의 `mpSecret.generated.ts` + `check:ait-mp-secret` 게이트도, foam-party의 "심사 번들에 비밀값을 두지 않으려 아예 비활성"도 불필요해진다. 비밀값은 플랫폼 백엔드에만 존재한다
2. **lizard-tycoon AIT 빌드가 현재 GA4에 0건 보내는 문제.** `deploy-apps-in-toss.yml`에 `analytics.config.json` 복원 스텝이 없다. Play와 TestFlight 워크플로에는 있는데 AIT만 빠져 계측 사각지대다

## 플랫폼 수집이 필요한 이유 — GA4가 구조적으로 못 하는 것

| # | 항목 |
|---|---|
| 1 | **MP는 예약 이벤트명을 못 보낸다.** `session_start`, `in_app_purchase`, `ad_impression` 등 11개가 실제로 스킵되고 있다. 우회가 아니라 손실이다 |
| 2 | **GA4 데이터는 서버가 신뢰할 수 없다.** MP는 인증이 없어 누구나 위조 가능하므로 **결제 근거나 CS 증빙으로 쓸 수 없다** |
| 3 | **D-1 지연 + 앱별 데이터셋.** 실시간 조회도 횡단 조회도 불가 |
| 4 | **유저 단위 조회가 사실상 불가.** "이 유저가 뭘 했나"는 CS의 첫 질문이다 |

**이중화가 아니라 역할 분담이다.**

| | 역할 |
|---|---|
| GA4 | 마케팅·잔존율 분석 |
| 플랫폼 | **운영·CS·결제 근거의 인증된 원장** |

## 이중화 비용을 실제로 막는 장치 3개

1. **단일 진입점 SDK** — 앱 코드는 `track()` 한 번만 호출한다. 현재 "전송 경로 7가지, MP 클라이언트 3벌"을 죽이는 실제 수단이 이것이다
2. **정규화를 SDK 한 곳에서만** — TS와 GDScript가 동일 golden fixture로 계약 테스트를 통과해야 한다
3. **플랫폼 sink는 allowlist** — 기본 20~30개(구매·인증·핵심 퍼널)만. BigQuery 비용과 ingest QPS를 상수로 묶는다

## 직렬화 규약

**`boolean → 1/0`을 채택한다.** happy-farm의 `toScalar()` 방식이다.

```
boolean          → 1 / 0
number 유한값     → 그대로
number NaN/Inf   → 0
null / undefined → 파라미터 자체 drop
string           → 100자 truncate
파라미터 이름     → 40자 초과 시 drop
객체 / 배열       → drop
파라미터 개수     → 25개 상한
```

채택 근거: 이미 라이브 GA4 데이터가 이 형식으로 쌓여 있고, GA4에서 숫자는 SUM/AVG가 되지만 문자열은 안 된다.

**객체·배열을 조용히 `String()` 하지 않는다.** 조용한 stringify가 이 조직에서 실제로 문제를 냈다.

### 현재 3갈래로 갈라진 상태

| 구현 | boolean 처리 |
|---|---|
| happy-farm `measurementProtocol.ts` | `1/0` ← **채택** |
| vocab-swipe `ga4MeasurementProtocol.ts` | `"true"/"false"` |
| lizard-tycoon `ga4_mp_sender.gd` | **정규화 없음** — GDScript `true`가 JSON `true`로 나가 GA4가 조용히 거부 |

vocab-swipe만 파괴적 전환이 필요하므로 **마이그레이션 순번을 최후로** 둔다.

### conformance 벡터로 강제

`spec/conformance/param-normalization.json`을 TS `node:test`와 GDScript probe에 각각 넣어 **바이트 동일 JSON** 출력을 CI에서 검증한다.

문서로 적어두면 6개월 뒤 갈라지지만, 벡터는 CI가 지킨다.

## 기존 이벤트명은 일괄 리네임하지 않는다

접두사가 앱마다 다르다 — happy-farm 무접두사 60개 이상, crossword `game_`, moonmate `cp_`, babycare `bc_`.

통일하면 **GA4 시계열이 단절**되고 `AppMetricDaily`, `AppContentMetricDaily`, `content-registry.ts`가 전부 깨진다.

대신:

- 기존 앱은 이름 그대로. SDK는 배관만 교체한다
- 신규 앱은 템플릿이 `core_*` 공통 사전을 기본 제공한다
- **플랫폼으로 보낼 때만** 레지스트리의 `event_prefix`로 접두사를 strip한다 → 횡단 쿼리가 가능해진다
- `spec/events.md`에 앱별 현행 이름과 표준 개념의 매핑표를 문서로 유지한다

## PII blocklist

키 이름이 `email`, `phone`, `name`, `address`, `birth` 등에 해당하면 **SDK가 drop**한다.

개발자가 무심코 `log_event("login", {email: ...})` 하는 게 실제로 가장 흔한 사고다. conformance 벡터에 케이스를 넣어 CI로 강제한다.

## BigQuery 스키마

```sql
-- seorilabs-platform.platform.events
event_id          STRING NOT NULL   -- 클라이언트 ULID
received_at       TIMESTAMP NOT NULL
event_ts          TIMESTAMP NOT NULL -- 클라이언트 시각, ±48h로 clamp
app_id            STRING NOT NULL
platform_user_id  STRING
ga4_client_id     STRING            -- GA4 대조용
session_id        STRING
event_name        STRING NOT NULL   -- 정규화, 접두사 제거
platform          STRING            -- android|ios|web|ait
app_version       STRING
locale, country   STRING
params            JSON
sdk_version       STRING
-- PARTITION BY DATE(received_at)   ← 적재 시점 기준. 지각 이벤트도 최신 파티션으로
-- CLUSTER BY app_id, event_name
-- partition expiration 400 days
```

`received_at` 기준으로 파티션하는 이유는 **지각 이벤트가 과거 파티션을 건드리지 않게** 해서 프루닝을 안정시키기 위해서다.

적재는 **Storage Write API 동기 append**. Pub/Sub을 넣지 않는다. 클라이언트가 이미 비동기 fire-and-forget으로 배치를 보내므로 요청 내 동기 write가 문제되지 않고, 인메모리 버퍼가 없으니 인스턴스 종료 시 유실이 없다. 월 2TiB 무료.

## at-least-once

**서버 dedup을 하지 않는다.** 클라이언트가 `event_id`(ULID)를 넣고 중복 제거는 쿼리에서 한다. 서버 dedup은 write 비용 대비 가치가 없다.

> **정확한 카운트를 요구하는 지표를 플랫폼 이벤트로 계산하지 않는다.** 그건 GA4 BigQuery export의 몫이다.

## 플랫폼 DAU는 지표가 아니다

플랫폼도 "오늘 토큰을 검증한 유니크 유저"를 안다. **이걸 새 DAU 지표로 만들면 안 된다.** 두 DAU가 다르고 어느 게 맞는지 모르게 된다.

> **플랫폼 DAU는 헬스체크로만 쓴다: `coverage = 플랫폼 DAU / GA4 DAU`**

이러면 이중화가 아니라 **상호검증**이 된다. coverage가 갑자기 60%로 떨어지면 특정 버전이나 플랫폼에서 SDK 연동이 깨진 것이다.

기존 `/analytics` 화면은 **한 줄도 고치지 않는다.** 장기 통합은 백오피스 코드에 이미 주석으로 예고된 `ContentMetricsSource` 포트의 두 번째 구현으로 간다. 1단계 범위 밖이다.
