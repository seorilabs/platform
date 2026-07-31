# Work

작업 로그와 backlog.

## 현재 단계

**D0 완료.** 다음은 P0 — 실측과 부트스트랩.

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
| GCP `ORG_ID`, `PROVISIONER_BILLING_ID` | P0 | `AGENTS.local.md` |
| Cloud Run 실제 URL 3종 | P0 | `AGENTS.local.md`, `08-ops/BREAK-GLASS.md` |
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
