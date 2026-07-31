# Work

작업 로그와 backlog.

## 현재 단계

**P4 진행 중.** P0~P3 완료, P4는 도메인·원장·카탈로그·바인딩까지.

| 서비스 | URL | 담당 |
|---|---|---|
| `platform-api` | `https://platform-api-306278488979.asia-northeast3.run.app` | identity + RemoteConfig |
| `platform-ingest` | `https://platform-ingest-306278488979.asia-northeast3.run.app` | 이벤트 수집 |

테스트 75개, 패키지 10개. `go test ./...` 통과.

---

## 완료

### D0 문서화

Obsidian 노트 3건, 저장소 골격, ADR 0001~0009, `spec/openapi.yaml`(lint 경고 0), conformance 벡터 3종.

### P0 실측과 부트스트랩

GCP 프로젝트 + **Billing budget 70,000 KRW**(40%/100%) · Firestore `(default)` `asia-northeast3` **`freeTier: true`** 삭제 보호 · BigQuery 2종 · SA 7개 · Artifact Registry · `cmd/fs` 조회 CLI.

| # | 실측 | 결과 |
|---|---|---|
| 1 | Apple JWS Go 방안 | ✅ `richzw/appstore` + OCSP 자체 추가 (ADR 0009) |
| 2 | Firebase 미등록 Firestore | ✅ 생성됨, `freeTier: true` |
| 6 | Cloud Run DRS | ✅ 막힘 확인, `--no-invoker-iam-check` 우회 |
| 7 | 콜드스타트 | ⚠️ **425ms** — 목표 300ms 초과 → **warm-up ping 도입 확정** |
| 3·4·5 | AIT 관련 | 미착수. 실제 `.ait` 빌드와 심사 필요 |

### P1 identity

`platformerr`(Code 60여 개, **AST 파싱으로 누락 자동 검출**) · `store`(Firestore 접근 독점) · `httpx` · `registry` · `identity` · `cmd/regsync`.

**필수 게이트 통과**: 골든 JWT 6종 거부 · 100회 동시 호출 → `platform_user_id` 1개.

배포 E2E: 세션 발급·갱신·삭제, 불변식 8(미지 필드 400), 미등록 앱 403.

### P2 이벤트 수집

conformance 벡터 28케이스 통과. 배포 E2E에서 `is_first: true` → `1`, `email` 제거, 중첩 객체 제거, allowlist 밖 제외 확인.

**남음**: TS SDK, GDScript SDK, 레퍼런스 앱 2개.

### P3 RemoteConfig

타겟팅 3축(플랫폼·앱버전·로케일), ETag 304, kill switch 3종. 배포 E2E 통과.

### P4 IAP (진행 중)

- `domain` — 불변식 1·2·3·6·9를 테스트로 고정
- `ledger` — **실제 Firestore 트랜잭션**에서 불변식 2·3·4·6·10 검증 (통합 테스트 7개)
- `catalog` — 마켓별 단계적 출시, placeholder·중복 거부
- `binding` — HMAC keyring 회전, 상수시간 비교 (불변식 11)

---

## 남은 작업과 제약

| 단계 | 상태 | 제약 |
|---|---|---|
| P4 나머지 | verify 유스케이스, 검증 핸들러 | 없음 |
| **P5 마켓 provider** | 미착수 | **Play SA 권한·Apple `.p8`·AIT mTLS 전부 미확보.** 코드는 쓸 수 있으나 **E2E 검증 불가** |
| P6 웹훅·워커 | 미착수 | 마켓 자격증명 필요. 워커 로직 자체는 검증 가능 |
| P2 SDK | 미착수 | 없음. TS·GDScript 2벌 + 레퍼런스 앱 |
| P7 백오피스 | 미착수 | **`seorilabs-backoffice` 저장소** 작업 |
| P8 lizard-tycoon | 미착수 | **`lizard-tycoon` 저장소** + 실기기 마켓 샌드박스 |
| P9 마감 | 미착수 | 장애 리허설 포함 |

## 미확정 항목

**해당 단계 전에 반드시 채워야 한다.**

| 항목 | 필요 시점 | 위치 |
|---|---|---|
| IAP rate limit·재시도 파라미터 | **P4** | `03-architecture/iap.md` |
| dead-letter 보존기간·alert 채널 | **P4** | `03-architecture/iap.md` |
| Play 런타임 SA + Console 권한 | **P5** | `05-markets/README.md` |
| Apple issuer ID·key ID·`.p8` | **P5** | `05-markets/README.md` |
| **AIT mTLS 인증서·상품 ID·claim 발급 경로** | **P5** | `05-markets/README.md` |
| 앱별 현행 이벤트 이름 매핑 | P2 SDK | `spec/events.md` |
| Firestore PITR 활성화 여부 | 실데이터 축적 시 | `06-release/gcp-bootstrap.md` |

AIT 항목 3건은 **lizard-tycoon에서도 미해결**이다. 확보하지 못하면 AIT provider는 스텁으로 두고 Play·App Store만 먼저 간다.
