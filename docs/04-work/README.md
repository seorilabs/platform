# Work

작업 로그와 backlog.

## 현재 상태

**P0~P9 완료. lizard-tycoon이 플랫폼 IAP로 실서비스 중이다.**

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
| `lizard-tycoon` | ✅ | ✅ | ✅ | sandbox 원장. entitlement 2종 |
| `babycare` | ✖ | ✖ | ✖ | Firebase custom token bridge만 (ADR 0013) |

**레지스트리는 파일을 고치는 것만으로 반영되지 않는다.** `cmd/regsync`를 사람이
돌린다 — [registry/apps/README.md](../../registry/apps/README.md).

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

### 2. warm-up ping — 결정만 하고 미구현

P0에서 콜드 **425ms**(목표 300ms 초과)를 재고 도입을 확정했는데, Cloud Scheduler에는
`platform-worker-5m`뿐이다. 결제 검증만 유저가 대기하는 경로라 거기서만 체감된다.

**먼저 다시 재는 것이 순서다.** 425ms는 P0 시점이고 그 뒤 코드가 많이 바뀌었다.

### 3. App Check — 뼈대만 있다

에러 코드 4종(`platformerr/code.go`)과 registry `require_app_check` 필드가 있는데
**검증 코드가 없다.** 1단계에서 끄기로 한 의도된 상태다.

Go SDK에 `consume` replay 방지가 없어 자체 nonce 저장소가 필요하고, Godot에는
App Check SDK 자체가 없다.

### 4. 앱 확산

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
| 7 | 콜드스타트 | **425ms** — 재측정 필요 (위 "남은 것" 2번) |
| 3·4·5 | AIT 관련 | **미착수** — 실제 `.ait` 빌드와 심사 필요 |

### P1 identity

`platformerr`(Code 60여 개, **AST 파싱으로 누락 자동 검출**) · `store`(Firestore 접근
독점) · `httpx` · `registry` · `identity` · `cmd/regsync`.

**게이트 통과**: 골든 JWT 6종 거부 · 100회 동시 호출 → `platform_user_id` 1개 ·
불변식 8(미지 필드 400) · 미등록 앱 403.

babycare는 ADR 0013의 Firebase custom token bridge를 쓴다. 기존 ID token은 같은
uid로 교환하고 신규 uid는 서버가 만든다. SA·IAM 바인딩·registry sync까지 배선이
끝났고 기능 플래그는 전부 꺼져 있다.

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

---

## 미확정 항목

| 항목 | 필요 시점 | 위치 |
|---|---|---|
| **AIT mTLS 인증서·상품 ID·claim 발급 경로** | AIT 붙일 때 | `05-markets/README.md` |
| dead-letter 보존기간·alert 채널 | 실운영 축적 시 | `03-architecture/iap.md` |
| Firestore PITR 활성화 여부 | 실데이터 축적 시 | `06-release/gcp-bootstrap.md` |
