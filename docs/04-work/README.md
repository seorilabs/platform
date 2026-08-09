# Work

작업 로그와 backlog.

## 현재 상태

**P0~P9 완료. 공개된 lizard-tycoon은 production IAP 원장을 사용한다.**

Apple·Play 두 마켓 실기기 검증과 레거시 Firebase Functions 셧다운까지 끝났다.
백오피스 운영자 지급(선물)도 개통했다. AIT만 자격증명을 기다린다.

| 서비스 | 역할 |
|---|---|
| `platform-api` | identity + RemoteConfig |
| `platform-iap` | 결제 검증 + 마켓 웹훅. **마켓 자격증명은 여기에만** (R3) |
| `platform-ingest` | 이벤트 수집 |
| `platform-admin` | 운영 조회·조작. DRS로 비공개 |
| `platform-worker` (Job) | 완료 outbox 재시도. Scheduler 5분 |

전부 같은 이미지에 `PLATFORM_ROLE`로 갈린다. 배포는 `.github/workflows/deploy.yml`이
다섯 대상에 같은 태그로 올리고 실제로 같은 태그가 됐는지 다시 확인한다.

테스트 함수 325개, 패키지 24개(22개에 테스트). `go test -race ./...` 통과.

### 등록된 앱

| app_id | config | events | iap | 비고 |
|---|---|---|---|---|
| `lizard-tycoon` | ✅ | ✅ | ✅ | **production 원장**, entitlement 2종 |
| `cycle-pair` | — | — | — | Firebase 게스트 인증 |
| `babycare` | ✖ | ✔ | ✖ | Firebase custom token bridge + 핵심 퍼널·광고 이벤트 |

**레지스트리는 파일을 고치는 것만으로 반영되지 않는다.** `cmd/regsync`를 사람이
돌린다 — [registry/apps/README.md](../../registry/apps/README.md).

#### 원장 환경이 어긋나면 admin만 죽는다

레지스트리의 `iap.ledger_environment`와 서비스의 `IAP_LEDGER_ENVIRONMENT`가
같아야 한다. 다르면 admin 경로가 전부 422 `environment_mismatch`로 막힌다.

**결제 경로는 영향받지 않는다.** `LedgerEnvironment` 검사는
`internal/admin/handler.go`에만 있고 verify 경로에는 없다. 그래서 어긋나도
유저 결제는 계속 되고 운영자만 아무것도 못 하는 상태가 된다 — 알아채기 어렵다.

2026-08-03에 실제로 겪었다. 서비스는 production으로 전환됐는데 Firestore
레지스트리가 `sandbox`로 남아 admin이 전부 422였다. repo 파일은 이미
production이었고 regsync를 돌리지 않은 것이 원인이다.

두 원장은 경로가 다르고 서로 보이지 않는다. 불변식 9다.

```
production   processed_orders/...            (루트)
sandbox      iap_environments/sandbox/...
```

환경을 전환하면 이전 환경의 구매는 앱에서 사라진 것처럼 보인다. 데이터가
지워진 것이 아니라 다른 공간에 있다.

#### App Store 심사 기간의 sandbox와 공개 이후 production 운영

Apple App Review의 인앱결제는 sandbox 거래다. 플랫폼은 자동 fallback을 금지하고
원장 환경을 서비스 시작 시 하나로 고정하므로, 심사 접수 전부터 승인 완료까지
`lizard-tycoon` registry와 `platform-iap`·`platform-admin`·`platform-worker`를
모두 sandbox로 유지한다.

당시 registry에서 `features.iap=true`인 앱은 `lizard-tycoon` 하나였으므로 심사
기간에 공용 원장을 sandbox로 전환할 수 있었다. 이제 `happy-farm`도 IAP가 활성화돼
있으므로 모든 활성 IAP 앱의 환경을 production으로 맞춰야 한다. 배포 workflow가
이 일치를 fail-closed로 검사한다.

심사 승인 뒤에도 앱은 수동 출시 상태로 둔다. 공개 출시 전에 별도 변경으로
registry를 production으로 되돌리고 `regsync`를 적용한 다음, Deploy workflow의
`iap_environment=production` 배포와 production 실거래 검증을 마쳐야 한다.

2026-08-09 App Store 공개 상태를 확인했으므로 `lizard-tycoon` registry를
production으로 복구한다. sandbox 심사·테스트 원장은 production으로 복사하지 않고
분리 보존한다.

---

## 남은 것

### 1. AIT (AppsInToss) — 유일한 기능 공백

**코드는 완성돼 있다.** `internal/iap/providers/toss`가 mTLS 검증기까지 구현돼
있고 테스트도 있다. 인증서가 없어 부팅 시 건너뛴다.

```
cmd/platform/iap.go        "AppsInToss 인증서가 없어 건너뛴다"
internal/identity/service.go  KindAITLogin → "아직 지원하지 않는 로그인 방식이에요"
```

막는 것은 전부 외부 자격증명·실측이라 코드로 풀 수 없다.

| # | 항목 |
|---|---|
| 1 | mTLS 클라이언트 인증서 (파트너 콘솔 발급) |
| 2 | AIT 상품 ID — 카탈로그 `apps_in_toss` 필드가 비어 있다 |
| 3 | `aitUserKey` claim 발급 경로 — **lizard-tycoon에서도 미해결** |
| 4 | `@apps-in-toss/web-framework`의 `Storage`/`getAnonymousKey`/`appLogin` 웹 노출 여부 |
| 5 | Godot HTML shell에 `<script>` 추가한 `.ait`의 심사 통과 여부 |

3번 때문에 `KindAITLogin`은 **fail-closed로 거부**한다. 검증 없이 받으면
클라이언트가 보낸 값을 그대로 신뢰하게 된다. 이 판단은 유지한다.

4·5가 실패하면 Godot Web은 신원 없는 이벤트 전용으로 축소한다.

### 2. App Check — 검증기 구현, 앱별 강제 전환 대기

Firebase Admin SDK for Go 검증기와 custom-token·계정 삭제 경계의
`X-Firebase-AppCheck` 처리를 구현했다. registry `require_app_check`가 false인 앱은
기존 클라이언트 호환을 유지하고, true인 앱은 token이 없거나 유효하지 않으면 거부한다.

Go SDK에 `consume` replay 방지가 없어 일회성 IAP 요청에는 여전히 자체 nonce 저장소가
필요하고, Godot에는 App Check SDK 자체가 없다. Babycare는 신규 RN 후보의 iOS App Attest와
Android Play Integrity 실기기 확인 후 registry sync로 강제 전환한다.

### 3. 앱 확산

`happy-farm`이 다음 후보다. `SELF_METRICS_ENDPOINT`가 이미 뚫려 있어 이벤트만이면
진입 비용이 낮다. 다만 **유저당 862 이벤트/일**로 다른 앱의 15~25배라
allowlist 설계를 먼저 본다.

---

## 하지 않기로 한 것

남은 일이 아니다. 2단계 또는 범위 밖이다.

공지 · 메시지함 · 푸시 · 구독형 IAP · cross-app entitlement · RC staged rollout ·
서버측 이벤트 dedup · GA4 대체

**계정 연동·병합**도 여기 있다. 익명 uid에서 소셜로 승격할 때 `linkWithCredential`을
쓰지 않으면 새 puid가 생기고 구매가 따라오지 않는다. 불변식 4(cross-uid 자동 이전
금지)가 409로 막는다. lizard-tycoon이 이미 안고 있던 제약을 승계한 것이다.

---

## 완료 기록

### D0 문서화

Obsidian 노트 3건, 저장소 골격, ADR 0001~0013, `spec/openapi.yaml`, conformance 벡터.

### P0 실측과 부트스트랩

GCP 프로젝트 + **Billing budget 70,000 KRW**(40%/100%) · Firestore `(default)`
`asia-northeast3` **`freeTier: true`** 삭제 보호 · BigQuery 2종 · SA 7개 ·
Artifact Registry · `cmd/fs` 조회 CLI.

| # | 실측 | 결과 |
|---|---|---|
| 1 | Apple JWS Go 방안 | `richzw/appstore` + OCSP 자체 추가 (ADR 0009) |
| 2 | Firebase 미등록 Firestore | 생성됨, `freeTier: true` |
| 6 | Cloud Run DRS | 막힘 확인, `--no-invoker-iam-check` 우회 |
| 7 | 콜드스타트 | **425ms** — 실서버 재측정 후 warm-up ping 도입 (아래 P9 참고) |
| 3·4·5 | AIT 관련 | **미착수** — 실제 `.ait` 빌드와 심사 필요 |

### P1 identity

`platformerr`(Code 60여 개, **AST 파싱으로 누락 자동 검출**) · `store`(Firestore 접근
독점) · `httpx` · `registry` · `identity` · `cmd/regsync`.

**게이트 통과**: 골든 JWT 6종 거부 · 100회 동시 호출 → `platform_user_id` 1개 ·
불변식 8(미지 필드 400) · 미등록 앱 403.

#### babycare custom token bridge (ADR 0013)

기존 ID token은 같은 uid로 교환하고 신규 uid는 서버가 만든다. 2026-08-02에 앱 SA,
resource-level `roles/iam.serviceAccountTokenCreator`, registry sync, platform-api
production 배포까지 완료했다.

- 최초 활성화 workflow: [run 30750253253](https://github.com/seorilabs/platform/actions/runs/30750253253)
- 최초 활성화 revision/image: `platform-api-00015-xpx` / `platform:b57bfc82a6cf7cf5f5fb2b9c612adc4612d5754d`
- 후속 main 배포 호환성: [run 30750946141](https://github.com/seorilabs/platform/actions/runs/30750946141) 뒤 `platform-api-00016-cdv` / `platform:bdbd69428900d85ab7ae4e9a58b32eee09e48f20`에서 babycare config 200과 custom-token POST-only route 유지
- live smoke: UID 주입 거부, 신규 Firebase custom token 교환, 합성 legacy UID 보존,
  `Cache-Control: no-store`, 생성한 Firebase 사용자와 platform mapping cleanup
- 후속 구현: App Check 검증기와 `DELETE /v1/auth/firebase-account` 멱등 삭제 경계
- 2026-08-09 운영 gate: Google Play 설치 v1.1.2의 App Check token과 Firebase Functions
  강제 호출을 확인한 뒤 registry `require_app_check=true`로 전환
- 남은 운영 gate: 실제 기존 사용자·실기기 migration

registry의 `events`와 `firebase_custom_token_bridge`를 켠다. `config`/`iap`은
계속 끄고, 이벤트는 `bc_` 핵심 퍼널과 `core_screen_view`·`core_ad_*`만
allowlist로 수집한다. GA4는 타깃별 adapter로 보내고 Platform은 같은 이벤트의
운영·coverage sink로 사용한다.

### P2 이벤트 수집 + SDK

conformance 벡터 28케이스 통과. 배포 E2E에서 `is_first: true` → `1`, `email` 제거,
중첩 객체 제거, allowlist 밖 제외 확인. TS·GDScript SDK 2벌과 레퍼런스 앱 완료.

GDScript SDK는 vendoring + 체크섬 게이트로 드리프트를 막는다.

### P3 RemoteConfig

타겟팅 3축(플랫폼·앱버전·로케일), ETag 304, kill switch 3종.

### P4 IAP 도메인 + 원장

- `domain` — 불변식 1·2·3·6·9를 테스트로 고정
- `ledger` — **실제 Firestore 트랜잭션**에서 불변식 2·3·4·6·10 검증
- `catalog` — 마켓별 단계적 출시, placeholder·중복 거부
- `binding` — HMAC keyring 회전, 상수시간 비교 (불변식 11)

### P5 마켓 provider

Play(ADC + androidpublisher) · Apple(ADR 0009 방안 + OCSP) · AIT(mTLS, 자격증명 대기).
Secret Manager 배선과 `platform-iap` 분리까지.

### P6 웹훅 + 재시도 워커

Apple ASSN v2(JWS) · Play RTDN(push subscription + OIDC) · 이벤트 lease ·
Cloud Run Job 워커(claim → 완료 → backoff → dead-letter).

### P7 백오피스 connection

`seorilabs-backoffice`의 `/platform/iap` 화면. 조회는 read 전용 SA, 조작은 write SA를
가진 AppOps worker를 거친다. 운영자 지급·회수·App Store sandbox 초기화.

### P8 lizard-tycoon 통합 → 전환

Apple·Play 실기기 구매·복원·웹훅·완료 반영 전 경로 검증.
근거는 [market-verification.md](../07-qa/market-verification.md).

이 검증에서 **acknowledge 성공(204)을 실패로 처리하던 결함**을 찾았다. 환불된
구매로는 드러나지 않는 경로였다.

### P9 마감

템플릿 반영, 장애 리허설, 운영 문서. 레거시 Firebase Functions 셧다운은 두 마켓
웹훅이 실트래픽으로 이관된 것을 확인한 뒤 진행했다.

#### 콜드스타트 재측정과 warm-up ping (2026-08-03)

P0의 425ms는 표준 라이브러리만 쓴 최소 서버 수치였다. 의존성이 다 붙은 뒤를
다시 쟀다.

| 서비스 | p50 | p95 | 목표 300ms 대비 |
|---|---|---|---|
| `platform-api` | 842ms | 878ms | 2.1배 |
| `platform-iap` | 765ms | **798ms** | 1.9배 |
| `platform-ingest` | 575ms | 600ms | 1.4배 |

Cloud Run이 이미 기록한 `startup_latencies`를 읽었다. 리비전을 비워 콜드를 만들면
그 콜드를 실유저가 맞는다. warm은 반대로 좋다 — 서버 몫 47~60ms.

목표를 2배 가까이 넘겨 **warm-up ping을 도입했다.** 결제 검증은 유저가 화면에서
기다리는 유일한 경로다.

```
platform-iap-warmup-5m   */5 * * * *  GET /health/live
platform-api-warmup-5m   */5 * * * *  GET /health/live
```

`platform-ingest`는 fire-and-forget이라 콜드가 유저에게 보이지 않고,
`platform-admin`은 운영자용이라 감수한다. 붙이지 않았다.

측정 방법과 되돌리는 명령은
[06-release/README.md](../06-release/README.md#콜드스타트)에 있다.

#### 원장 환경 불일치 감지 (2026-08-03)

레지스트리와 서비스의 원장 환경이 어긋나면 그 앱의 운영 조작이 전부 422가 되는데
**유저 결제는 계속 된다.** 5xx도 트래픽 변화도 없어 대시보드로는 잡히지 않는다.
실제로 몇 시간 동안 그 상태였고 선물 한 건을 넣어보고 나서야 알았다.

`/v1/admin/health`가 어긋난 앱을 돌려주고 WARNING 로그를 남긴다. 백오피스 플랫폼
개요 화면도 `degraded`로 바뀌며 해소 방법(`regsync`)을 같이 띄운다.

배포 후 실제로 어긋내 확인했다.

```json
{"environment":"sandbox",
 "environmentMismatches":[
   {"appId":"lizard-tycoon","registry":"production","ledger":"sandbox"}]}
```

```json
{"level":"WARN","msg":"레지스트리와 원장 환경이 어긋나 조작이 막혔다",
 "apps":"lizard-tycoon","count":1,"ledger_environment":"sandbox"}
```

---

## 미확정 항목

| 항목 | 필요 시점 | 위치 |
|---|---|---|
| **AIT mTLS 인증서·상품 ID·claim 발급 경로** | AIT 붙일 때 | `05-markets/README.md` |
| dead-letter 보존기간·alert 채널 | 실운영 축적 시 | `03-architecture/iap.md` |
| Firestore PITR 활성화 여부 | 실데이터 축적 시 | `06-release/gcp-bootstrap.md` |
