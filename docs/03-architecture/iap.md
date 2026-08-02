# IAP

## 이식이 아니라 재구현이다

lizard-tycoon의 기존 구현이 이미 3마켓을 커버하는 성숙한 형태다. **규모를 정확히 인식해야 한다.**

| 대상 | 규모 |
|---|---|
| `firebase/functions/src/` | **4,325 LOC** |
| `firebase/functions/test/` | **2,626 LOC** — 에뮬레이터 테스트 738 포함 |
| `game/infra/iap/` GDScript | **2,371 LOC** |
| 백오피스 어댑터 | **1,124 LOC** |

**좋은 소식**: `domain.ts` 148줄에 포트 인터페이스가 이미 정의되어 있어 Go 인터페이스로 거의 1대1 대응한다. **도메인 결정이 전부 내려져 있다.**

```go
type PurchaseVerifier interface {
    Platform() Platform
    Verify(ctx context.Context, proof PurchaseProof) (VerifiedPurchase, error)
    CompleteGrant(ctx context.Context, p VerifiedPurchase) error
}

// VerifiedPurchase{ Platform, ProductID, CanonicalID, ProviderOrderID,
//                   PlatformAccountID, PurchasedAt, ObservedAt, State, Completion }
// State:      active | pending | revoked | invalid
// Completion: none | google_acknowledge | apple_finish | apps_in_toss_client_complete
```

---

## 불변식 12개 — 절대 훼손 금지

언어와 저장소가 바뀌어도 그대로다. **이를 바꾸는 변경은 ADR 없이 하지 않는다.**

### 1. orderKey

```
orderKey = sha256("{platform}:{canonicalId}")
```

| 마켓 | canonicalId |
|---|---|
| Google Play | `purchaseToken` |
| App Store | **`originalTransactionId`** — `transactionId`가 아니다 |
| AppsInToss | `orderId` |

**클라이언트 생성 ID를 쓰지 않는다.** 마켓이 준 값만이 신뢰 가능한 멱등키다.

### 2. granted와 alreadyGranted는 배타적

```
alreadyGranted = 소유자가 같고 && state가 active && sources[orderKey].state가 active
granted        = !alreadyGranted
```

**둘 다 false인 조합은 존재하지 않는다.** 클라이언트가 이 전제로 분기한다.

### 3. stale 억제

랭크: `revoked(3) > active(2) > pending(1)`.

`observedAt`이 더 이른 갱신은 무시하고, 같은 시각이면 랭크가 낮은 전이를 무시한다.

> **늦게 끝난 grant가 환불을 되돌리지 못한다.** 이게 이 규칙의 존재 이유다.

단, cross-`platform_user_id`의 `active → active` 복원은 소유권 이동까지
무시하지 않는다. 소유권만 새 사용자로 옮기고 order/source의 기존 최신
`state`, `purchasedAt`, `observedAt`은 보존한다.

### 4. 검증된 마켓 토큰은 단일 소유자로 원자적 이전

현재 로그인한 마켓 계정이 반환하고 서버가 마켓 API로 다시 검증한 구매
토큰을 소유 근거로 삼는다. 기존 `platform_user_id`가 다르면 이전 소유자의
source 제거와 새 소유자 지급을 **한 트랜잭션**에서 처리하고
append-only `iap_ownership_transfers` 복구 증거를 같은 트랜잭션에 남긴다.
`iap.transferred` 감사 이벤트는 운영 검색용 projection이다. 계정 바인딩
불일치는 부정 신호로 기록하되 복원을 막지 않는다. → ADR 0010

### 5. 원장 문서 삭제 금지

`processed_orders`, `processed_iap_events`, `pending_refund_reviews`, 운영자
지급·회수·sandbox reset intent·completion·미시작 closure, sandbox reset barrier,
소유권 이전 증거 원장은 **영구 보존**한다.
`iap_completion_outbox`만 완료·회수 시 삭제한다.

미지의 환불은 소유자 없는 tombstone 문서를 먼저 만든다.

### 6. entitlement active 계산

```
active = sources 중 하나라도 state == "active"
```

트랜잭션 안에서 재계산해 내부 원장과 공개 projection에 동시에 쓴다.

### 7. completeGrant 실패는 지급을 롤백하지 않는다

마켓 완료 호출(`acknowledge` / `finishTransaction`)이 실패해도 **지급은 이미 커밋된 상태로 둔다.** outbox에 넣고 `retry_server_completion`을 클라이언트에 알린다.

반대로 하면 "돈은 나갔는데 물건이 없다"가 된다.

### 8. 요청에 권한 결정 필드를 주입할 수 없다

허용 필드 외의 어떤 키도 400으로 거부한다. `uid`, `entitlementId` 주입 차단이 목적이다.

### 9. Apple 제약 둘

- **`NON_CONSUMABLE`이 아니면 422로 거부**한다
- **production과 sandbox 자동 fallback 금지.** 환경 설정이 원장 환경과 불일치하면 부팅을 실패시킨다

### 10. 알림은 기존 소유자만 재조정한다

**웹훅 알림만으로 신규 지급을 하지 않는다.** 이미 아는 주문의 상태를 조정할 뿐이다.
소유자는 상태 반영 transaction 안에서 다시 검증한다. 조회와 반영 사이에 앱 복원으로
소유자가 바뀌면 현재 소유자를 재조회하며, 웹훅이 사용자 간 소유권을 이전하지 않는다.

### 11. 계정참조 HMAC

keyring 1~3개, **첫 항목이 현재 키**, 나머지는 회전 검증용. 비교는 **상수시간**(`crypto/subtle`).

### 12. 응답 envelope

```
성공  HTTP 2xx     {"ok": true,  "result": {...}}
실패  HTTP 4xx/5xx {"ok": false, "error": {"code": "...", "message": "..."}}
헤더  cache-control: no-store
```

### ⚠️ 12번의 함정 — 론칭 전에 반드시 고친다

현재 GDScript 클라이언트는 **응답 키 개수까지 정확히 일치**할 것을 요구한다. 즉 **서버가 필드를 하나 추가하면 기존 클라이언트가 깨진다.**

이 상태로는 R4(`/v1`은 영구히 깨지지 않는다)가 성립하지 않는다.

> **P8에서 "필수 필드 존재 검증 + 미지 필드 무시"로 완화한다.** 론칭 후에는 구버전이 마켓에 2~3년 남아 손댈 수 없다.

IAP 쪽 기존 계약은 닫혀 있으므로 그대로 두고, **플랫폼 SDK만 열린 계약**으로 만든다.

---

## 데이터 모델

모든 경로에 환경 prefix가 적용된다. `sandbox`면 `iap_environments/sandbox/` 가 앞에 붙는다.

| 경로 | 문서 ID | 핵심 필드 |
|---|---|---|
| `users/{puid}/entitlements/{entId}` | entId | `active`, `updatedAt` — **읽기 전용 projection** |
| `iap_users/{puid}/entitlements/{entId}` | entId | `active`, `sources.{orderKey}` — **내부 원장** |
| `processed_orders/{orderKey}` | sha256 | `puid`, `entitlementId`, `platform`, `productId`, `providerOrderId`, `platformAccountIdHash`, `state`, `observedAt`, `tombstone`, `transferSequence` |
| `processed_iap_events/{eventKey}` | sha256 | `provider`, `status`, `attemptCount`, `leaseId`, `claimExpiresAt` |
| `iap_completion_outbox/{orderKey}` | orderKey | `platform`, `action`, `status`, `attemptCount`, `nextAttemptAt`, `leaseId` |
| `pending_refund_reviews/{hash}` | sha256 | `platform`, `orderId`, `dueAt` — 24시간 |
| `iap_rate_limits/{…}` | | `windowStartedAt`, `count` |
| `operator_entitlement_grants/{requestId}` | requestId | 운영자 지급 감사 원장. **영구** |
| `operator_entitlement_revocations/{requestId}` | requestId | 대상 `grantRequestId`를 포함한 운영자 회수 감사 원장. **영구** |
| `sandbox_reset_requests/{requestId}` | requestId | 고정 payload·`resetAt`·최초 barrier revision. create-only **영구 intent** |
| `sandbox_reset_completions/{requestId}` | requestId | 정렬된 대상 orderKey·`completedAt`. create-only **영구 completion** |
| `sandbox_reset_barriers/{puid}` | puid | `revision`, active request/cutoff, 마지막 완료 request/cutoff, `updatedAt`. sandbox App Store Grant/reset 직렬화용이며 **삭제 금지** |
| `iap_ownership_transfers/{orderKey}-{sequence}` | orderKey+sequence | 이전·신규 puid, entitlement, platform, state, observedAt. 토큰·provider order ID·마켓 계정 참조 없이 **append-only 영구 복구 증거** |
| `admin_mutation_limits/{oidcSha256}` | sha256 | Admin 조작 분·시·일 durable rate gate |

`sources.{orderKey}` = `{platform, productId, state, purchasedAt, observedAt, updatedAt}`.

### Sandbox reset durable intent

App Store sandbox reset의 "시작"은 HTTP 요청 수신 시각이 아니라
`sandbox_reset_requests`와 active barrier가 **같은 prepare 트랜잭션에서
commit된 시점**이다. → ADR 0012

```text
prepare transaction  →  immutable intent + active barrier
apply transaction    →  order/source revoke + projection
                       + immutable completion + completed barrier
close transaction    →  intent 부재 requestId의 immutable closure
```

apply가 실패해도 intent와 active barrier를 지우지 않는다. 따라서 그 cutoff보다
오래된 App Store 거래는 계속 fail-closed하며, 호출자는 같은 `requestId`의 원 요청
또는 resume API로만 재개한다. completion까지 commit된 뒤에는 같은 requestId가
저장된 order key 결과를 멱등 반환한다.

cross-PUID 복원은 이전 소유자와 새 소유자의 barrier를 모두 읽고 touch한다.
active 또는 마지막 완료 cutoff에 걸린 pre-reset 거래는 소유권 이전도 막고,
cutoff 이후 구매만 신규 거래로 허용한다. 이 경계에서는 이전 증거도 만들지 않는다.

Admin 계약은 다음 상태를 구분한다.

| HTTP | error code / state | 의미와 후속 조치 |
|---|---|---|
| 200 | `prepared` | intent 확정, 효과 미완료. 수동 종결 금지, 같은 requestId로 resume |
| 200 | `completed` | 효과와 completion 확정. `applied`로 종결 가능 |
| 200 | `closed_not_started` | 미시작 closure 확정. 이때만 `not_applied`로 종결 가능 |
| 404 | `sandbox_reset_not_found` | intent와 closure 모두 없음. close를 먼저 commit하고 수동 종결은 보류 |
| 409 | `sandbox_reset_busy` | 같은 사용자의 다른 prepared reset이 진행 중 |
| 409 | `sandbox_reset_closed` | 미시작으로 종결한 requestId의 reset 또는 resume가 늦게 도착함 |
| 409 | `sandbox_reset_already_started` | intent 또는 completion이 먼저 확정되어 close할 수 없음 |
| 503 | `sandbox_reset_pending` | prepare 이후 apply 실패. 새 requestId 금지, 같은 요청 재개 |

상태 조회 응답에는 PUID와 order key를 노출하지 않는다. resume는
`RESUME RESET {appId} {requestId}` typed confirmation과 Admin write allowlist를
요구한다. 404 관찰만으로는 늦은 prepare를 막지 못하므로
`CLOSE RESET {appId} {requestId}` 확인 문자열로 closure를 먼저 commit해야 한다.

### 소유자 키는 platform_user_id

원본은 Firebase `uid`를 쓰지만 플랫폼은 `platform_user_id`로 바꾼다. **미론칭이라 마이그레이션 부담이 0**이다. → ADR 0008

### 환경 분리

`IAP_LEDGER_ENVIRONMENT` 하나가 모든 durable 경로의 prefix를 결정한다.

Go에서는 **repository 레이어에서 타입으로 강제**한다. 모든 경로 생성이 한 함수를 통과하게 만들어 실수로 prefix를 빠뜨릴 수 없게 한다.

Apple 환경 설정과 불일치하면 **부팅을 실패시킨다**(503). 자동 fallback은 없다.

### 인덱스 5종

`(platform, nextAttemptAt)` 복합 · `(puid)` · `(updatedAt)` · `(dueAt)` · `(puid, active)`.

`canonicalId`는 **단일필드 인덱스를 비활성화**한다. PII 최소화 목적이며 원본 규칙을 승계한다.

---

## 3마켓 provider

| | Google Play | App Store | AppsInToss |
|---|---|---|---|
| 검증 API | `purchases/productsv2/tokens/{token}` | `getTransactionInfo` | `order/get-order-status` |
| 완료 API | `:acknowledge` | `finishTransaction` | 클라이언트가 `completeProductGrant` |
| **자격증명** | **ADC** — SA + Console 권한. **JSON 키 없음** | Secret — issuer ID, key ID, `.p8` | **mTLS** 클라이언트 인증서 |
| 타임아웃 | 8초 | — | 10초, 응답 1MB 제한 |
| 계정 바인딩 | `obfuscatedExternalAccountId` — HMAC | `appAccountToken` — HMAC를 UUID 형태로 | **면제** — claim이 신뢰 경로 |
| 소비/비소비 | 명시적 구분 없음 | **`NON_CONSUMABLE` 강제** | — |
| 웹훅 | RTDN Pub/Sub | ASSN v2 JWS | **없음** |

### 상태 매핑

| 마켓 | active | pending | revoked |
|---|---|---|---|
| Play | `PURCHASED` | `PENDING` | `CANCELLED` |
| Apple | 기본 | — | `revocationDate` 등이 있으면 |
| AIT | `PURCHASED` / `PAYMENT_COMPLETED` | `ORDER_IN_PROGRESS` | `REFUNDED` |

### SKU 카탈로그

canonical JSON을 런타임에 주입한다. `entitlementId`는 `^[A-Za-z0-9._-]{1,128}$`, 최대 100개.

전역 `IAP_CATALOG_JSON`은 마켓 SKU→entitlement 매핑의 원장이다. 앱별 운영
경계는 `registry/apps/*.json`의 `iap.entitlement_ids`가 담당한다.
`features.iap=true`이면 앱 목록은 비어 있을 수 없고, Admin 조회와 조작은 두
목록의 교집합만 허용한다. 앱 목록에는 있지만 전역 카탈로그에 없는 값은 설정
불일치이므로 503으로 fail-closed한다.

`확정 필요` / `TBD` / `TODO` / 빈값 placeholder가 있으면 **503으로 부팅을 막는다.** 중복 SKU는 500.

---

## 웹훅

### Apple ASSN v2

HTTPS POST, body는 `{"signedPayload": "<JWS>"}` 단일 필드(64KB).

**인증이 곧 JWS 서명 검증이다.** 별도 토큰이 없다. 검증 실패는 401.

처리 대상: `ONE_TIME_CHARGE`, `REFUND`, `REFUND_DECLINED`, `REFUND_REVERSED`, `REVOKE`. 그 외는 무시.

eventId는 `notificationUUID`.

### Google RTDN

Pub/Sub topic `play-iap-rtdn` — **"goog" prefix가 금지되어 이 이름이어야 한다.**

Firebase Functions는 `onMessagePublished` 트리거였지만 Cloud Run에서는 **push subscription + OIDC 토큰 검증**으로 바꾼다.

eventId는 Pub/Sub `messageId`.

| 이벤트 | 처리 |
|---|---|
| `oneTimeProductNotification` | Play API 재조회 후 기존 주문 재조정 |
| `voidedPurchaseNotification` | `refundType=1` 전액환불이면 revoke. **부분환불은 `partial_refund_unsupported` 422** |
| `pendingRefundReviewNotification` | 24시간 `dueAt` 원장 기록만. **자동 응답하지 않는다** |
| `testNotification` | 무시 |

> **Pub/Sub 재전송 시맨틱 재설계 필요.** 현재 `event_busy` 503이 Pub/Sub 재시도를 유발하는 구조인데, push subscription에서 이 흐름이 그대로 성립하는지 P6에서 검증한다.

### 공통 이벤트 lease

```
claimEvent → handler → completeEvent
```

lease 5분. 실패 시 재시도 가능(5xx, `purchase_not_found`, `event_claim_lost`)이면 lease를 풀고 다시 던지고, 영구 실패면 `completeEvent`로 영구 무시한다.

---

## 재시도 워커

### 단일 실행 보장이 사라진다

Firebase는 `onSchedule` + `maxInstances:1, concurrency:1`로 **인프라가 단일 실행을 보장**했다. Cloud Run Job은 보장하지 않는다.

**다행히 lease 기반 claim이 이미 있어 다중 워커도 안전하다.** 다만 `nextAttemptAt` 밀어내기와 타임아웃 시 lease 유지 로직이 그 가정에서 여전히 맞는지 P6에서 재검토한다.

### 동작

5분 주기. 배치 최대 20건이되 **매 반복 1건씩 claim**한다 — 느린 앞 건이 뒤 건의 lease를 소진하지 않게 하기 위해서다.

Google은 **완료 전에 Play 상태를 재조회**한다. 이미 acknowledged면 호출하지 않고 종료하고, revoked면 회수로 전환한다.

Backoff는 `min(60초 × 2^(n-1), 6시간)`.

Dead-letter 조건은 시도 횟수 초과, age 초과, 레코드 파손.

**타임아웃은 lease를 해제하지 않고 만료까지 유지한다.** 외부 호출이 실제로 취소되었는지 알 수 없기 때문이다.

---

## 미결 항목 — P4에서 확정

원본에서도 `확정 필요` 상태인 것들이다.

| 항목 | 상태 |
|---|---|
| 검증 rate limit — uid당 / 앱당 분당 | `확정 필요` |
| 완료 최대 시도 횟수 | `확정 필요` |
| 완료 최대 age | `확정 필요` |
| **dead-letter 보존기간 · alert 채널 · TTL** | `확정 필요` |
| Google chargeback 자동 응답 정책 | 하지 않는다 |

---

## Go 이식의 난관

| # | 항목 | 난이도 | 대응 |
|---|---|---|---|
| **A** | **Apple JWS 검증 — 공식 Go 라이브러리 없음** | **최고** | ES256 + x5c 체인 + **OCSP online check** + 재시도 가능 실패 구분을 재현해야 한다. 커뮤니티 라이브러리 3종 또는 **자체 구현**(`crypto/x509` + `golang.org/x/crypto/ocsp`). P0 최우선. **최악엔 Apple만 기존 Functions 유지** |
| B | App Check replay 방지 | 높음 | `consume` 옵션이 Node Admin 전용이다. 1단계는 optional로 두고 자체 nonce 저장소는 후속 |
| C | 함수별 Secret 격리 상실 | 중간 | **`platform-iap` role 분리로 복원** → R3 |
| D | 런타임 SA 4개 분리 | 중간 | 단일 바이너리면 IAM이 합집합이 된다. `platform-iap` SA 하나에 Play·Apple·AIT 권한만 |
| E | Pub/Sub 트리거 → push subscription | 중간 | OIDC 검증 + cross-project 구독 |
| F | Firestore Security Rules 부재 | 낮음 | 클라이언트가 entitlement를 직접 read하던 경로가 사라진다. **API 경유만** 남긴다 |
| G | Cloud Run DRS 정책 | 낮음 | `allUsers` 바인딩이 막힐 수 있다. lizard-tycoon 선례 확인 |

---

## 백오피스 연동

기존 `commerce` 탭을 플랫폼 Admin API에 연결한다. **새 탭을 만들지 않는다.**

현재 백오피스 어댑터 1,124줄이 lizard-tycoon Firestore를 직접 조작하는데, **API 호출로 바뀌면서 오히려 단순해진다** — SA 키 관리, firebase-admin 초기화, 앱별 하드코딩이 전부 사라진다.

이관 대상 operation 8개: `recent-purchases`, `sandbox-testers`, `account-entitlements`, `refund-review-queue`, `reset-app-store-sandbox`, `production-grants`, `grant-production-entitlement`, `revoke-production-entitlement`.

**source 스키마를 통합한다.** 현재 백오피스가 운영자 source를 서버와 별도로 정의하고 있다.

**결제 관련 작업은 전부 서버가 앱·사용자·환경·카탈로그를 미리 검증하고,
요청 내용으로 계산한 typed confirmation과 일치해야만 실행**한다. OIDC write
allowlist와 durable rate gate도 함께 적용한다. → ADR 0011
