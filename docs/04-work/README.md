# Work

작업 로그와 backlog.

## 현재 단계

**P0 진행 중.** 인프라 부트스트랩 완료, 실측 7건 중 3건 확인.

### P0 진행

- [x] GCP 프로젝트 생성 + 과금 연결 + **Billing budget** (70,000 KRW, 40%/100%)
- [x] API 14종 활성화
- [x] **Firestore Native `(default)`** — `asia-northeast3`, `freeTier: true`, 삭제 보호 활성화
- [x] BigQuery `platform` / `platform_stg` — 동일 리전
- [x] 서비스 계정 7개 + IAM 최소권한
- [x] Artifact Registry `platform`
- [x] `cmd/platform` 최소 서버 + Dockerfile — distroless static
- [x] Cloud Run `platform-api` 배포 + 공개 접근
- [x] 실측 1 — Apple JWS ✅ **ADR 0009 Accepted**. `richzw/appstore` + OCSP 자체 추가
- [x] 실측 2 — Firestore ✅
- [x] 실측 6 — DRS ✅ 막힘 확인, `--no-invoker-iam-check` 우회
- [x] 실측 7 — 콜드스타트 ⚠️ **425ms, 목표 초과** → **warm-up ping 도입 확정**
- [ ] 실측 3·4·5 — AIT. 실제 `.ait` 빌드와 심사 제출이 필요해 별도 사이클
- [ ] `cmd/fs` 구현 — **직접 작성 대기**
- [ ] WIF — P1에서 CI와 함께

### P0에서 바뀐 판단

| 항목 | 계획 | 실측 후 |
|---|---|---|
| Apple JWS | 자체 구현이 유력. 최악엔 하이브리드 | **라이브러리 채택.** 체인 검증이 올바름을 확인해 자체 구현 이유가 사라졌다 |
| warm-up ping | 콜드 300ms 이하면 불필요 | **도입 확정.** 425ms이고 의존성이 붙으면 더 늘어난다 |
| 하이브리드 대비책 | Apple만 기존 Functions 유지 | **불필요해졌다** |

절차와 함정은 `../06-release/gcp-bootstrap.md`에 남겼다.

## D0 진행

- [x] Obsidian 노트 3건 — `프로젝트/개인/공통 플랫폼/`
- [x] 저장소 골격 + org 표준 docs 구조
- [x] ADR 0001~0008
- [x] `spec/openapi.yaml` 초안 — redocly lint 통과, 경고 0
- [x] `spec/conformance/*.json` 벡터 3종
- [x] `spec/events.md` 이벤트 사전
- [x] GitHub private repo push

## P0 착수 전 확인

ADR 0006(Go 채택)과 0008(원장 소유자 키)이 P4 이후를 좌우하므로 코드 전에 확정했다.

**P0의 첫 작업은 Apple JWS 검증 Go 방안 결정**이다. 이 결과가 ADR 0009가 되고, 실패하면 Apple만 기존 Cloud Functions를 유지하는 하이브리드로 간다.

## 미확정 항목 추적

여기 있는 것들은 **해당 단계 전에 반드시 채워야** 한다.

| 항목 | 필요 시점 | 위치 |
|---|---|---|
| Apple JWS 검증 Go 방안 | **P0** | ADR 0009 |
| ~~GCP `ORG_ID`, 과금 계정~~ | ~~P0~~ | **확보 — `06-release/gcp-bootstrap.md`** |
| Cloud Run URL — iap/ingest/admin | P2·P5·P7 | `08-ops/BREAK-GLASS.md` |
| Firestore PITR 활성화 여부 | 실데이터 축적 시 | `06-release/gcp-bootstrap.md` |
| support code 파생 규칙 | P1 | `03-architecture/identity.md` |
| 앱별 현행 이벤트 이름 매핑 | P2 | `spec/events.md` |
| IAP rate limit·재시도 파라미터 | **P4** | `03-architecture/iap.md` |
| dead-letter 보존기간·alert 채널 | **P4** | `03-architecture/iap.md` |
| Play 런타임 SA + Console 권한 | P5 | `05-markets/README.md` |
| Apple issuer ID·key ID·`.p8` | P5 | `05-markets/README.md` |
| **AIT mTLS 인증서·상품 ID·claim 발급 경로** | P5 | `05-markets/README.md` |

AIT 항목 3건은 **lizard-tycoon에서도 미해결**이다. 확보하지 못하면 AIT provider는 스텁으로 두고 Play·App Store만 먼저 간다.

## P0에서 답을 얻어야 하는 것

`../07-qa/README.md`의 실측 항목 7가지. 그중 **Apple JWS 검증 Go 방안**이 최우선이며, 실패 시 Apple만 기존 Cloud Functions를 유지하는 하이브리드로 간다.
