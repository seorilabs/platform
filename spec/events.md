# 이벤트 사전

이벤트 **이름과 파라미터**의 계약. 정규화 규약은 [`conformance/param-normalization.json`](conformance/param-normalization.json)이 정본이다.

## 3층 구조

```
seori_*   플랫폼 예약. SDK가 자동 발생시키고 앱이 직접 보낼 수 없다
core_*    공통 사전. 앱이 보내되 이름과 파라미터가 조직 고정
<앱 고유>  기존 이름 그대로. SDK가 이름에 손대지 않는다
```

## `seori_*` — SDK 자동 발생

| 이름 | 언제 | 파라미터 |
|---|---|---|
| `seori_session_start` | SDK `start()` 시 | `sdk_version`, `runtime` |
| `seori_sdk_error` | SDK 내부 실패 — 전송·인증 실패 | `stage`, `code` |
| `seori_iap_verify` | 결제 검증 시도 | `market`, `product_id`, `result` |

`session_start`가 아니라 `seori_session_start`인 이유는 **GA4 예약 이벤트명을 Measurement Protocol로 보낼 수 없기** 때문이다. lizard-tycoon의 `ga4_mp_sender.gd`가 이미 11개를 스킵하고 있다.

## `core_*` — 공통 사전

앱이 보내는 이벤트 중 **조직 전체에서 의미가 같은 것**만 여기 둔다.

| 이름 | 파라미터 |
|---|---|
| `core_screen_view` | `screen_name`, `screen_class?` |
| `core_ad_request` | `placement`, `ad_format` |
| `core_ad_impression` | `placement`, `ad_format`, `network` |
| `core_ad_reward` | `placement`, `reward_code?`, `reward_amount?` |
| `core_purchase_start` | `product_id`, `market`, `price_krw?` |
| `core_purchase_result` | `product_id`, `market`, `status` |
| `core_tutorial_step` | `step_index`, `step_name` |

### 값 고정

| 파라미터 | 허용 값 |
|---|---|
| `market` | `google_play` / `app_store` / `apps_in_toss` / `web` |
| `ad_format` | `interstitial` / `rewarded` / `banner` |
| `status` (purchase) | `verified` / `pending` / `cancelled` / `failed` |

`market` 값은 기존 GDScript 포트가 쓰는 문자열과 **동일하게 맞췄다.** 새로 만들면 매핑 계층이 하나 더 생긴다.

## 앱 고유 이벤트 — 일괄 리네임하지 않는다

접두사가 앱마다 다르다.

| 앱 | 접두사 | 규모 |
|---|---|---|
| happy-farm | 없음 | 60개 이상 |
| crossword-puzzle | `game_` | — |
| moonmate | `cp_` | 7개 |
| babycare | `bc_` | 4개 |
| foam-party | 없음 | 9개 |

**통일하면 GA4 시계열이 단절된다.** 이벤트 이름을 바꾸면 과거 데이터가 이어지지 않고, 백오피스의 `AppMetricDaily`·`AppContentMetricDaily`·`content-registry.ts`가 전부 깨진다.

대신:

- **기존 앱은 이름 그대로.** SDK는 배관만 교체한다
- **신규 앱은 `core_*`를 기본 제공**하고 앱 고유 이벤트에 앱 슬러그 접두사를 쓴다
- **플랫폼으로 보낼 때만** 레지스트리의 `ga4.event_prefix`로 접두사를 strip한다

마지막 항목 덕분에 플랫폼 테이블에서 이런 횡단 쿼리가 처음으로 가능해진다.

```sql
SELECT app_id, event_name, COUNT(*)
FROM `seorilabs-platform.platform.events`
GROUP BY 1, 2
```

GA4는 앱별 속성이 분리되어 있어 절대 줄 수 없는 것이다.

## allowlist

**플랫폼으로 보내는 이벤트는 레지스트리 allowlist에 있는 것만이다.** 기본 20~30개.

기준: 결제, 인증, 핵심 퍼널, 크래시. 게임 루프 이벤트는 넣지 않는다.

이유는 비용이다. happy-farm이 **유저당 862 이벤트/일**을 보내고 있는데(다른 앱의 15~25배), 이런 걸 전부 받으면 BigQuery 비용과 ingest QPS가 규모에 비례해 늘어난다. allowlist가 이를 **상수로 묶는다.**

allowlist 밖 이벤트는 **조용히 버린다.** GA4로는 여전히 간다.

## PII

키 이름이 PII 목록에 해당하면 **SDK가 drop**한다. 목록은 `conformance/param-normalization.json`의 `pii_keys`가 정본이다.

개발자가 무심코 `log_event("login", {email: ...})` 하는 게 실제로 가장 흔한 사고다.

## 앱별 현행 이름 매핑표

`확정 필요` — P2에서 각 앱의 실제 이벤트를 조사해 채운다.

| 앱 | 현행 이름 | 표준 개념 | 플랫폼 전송 |
|---|---|---|---|
| lizard-tycoon | `확정 필요` | — | — |
| happy-farm | `확정 필요` | — | — |

이 표는 문서로만 유지한다. 자동 변환하지 않는다 — 자동 매핑은 틀렸을 때 조용히 틀린다.
