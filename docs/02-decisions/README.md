# Decisions

ADR과 장기 의사결정을 관리한다.

새 결정은 `0009-title.md`처럼 번호를 올려 추가한다. 이미 결정된 내용을 바꿀 때는 기존 문서를 조용히 덮어쓰기보다 **supersede 상태를 남긴다.**

## 무엇을 ADR로 남기는가

**되돌리기 어려운 결정.** 구체적으로:

- 리전, 저장소 종류 — 생성 후 변경 불가하거나 마이그레이션 비용이 큰 것
- 언어, 계약 형식 — 코드 전체에 퍼지는 것
- 원장의 소유자 키 — 데이터 마이그레이션을 유발하는 것
- 조직 규칙에 예외를 만드는 것

일상적인 구현 선택은 `03-architecture/`에 쓰고 ADR로 만들지 않는다.

## 목록

| # | 제목 | 상태 |
|---|---|---|
| [0001](0001-extend-backoffice.md) | 백오피스를 확장하고 분리하지 않는다 | Accepted |
| [0002](0002-infrastructure-project-category.md) | seorilabs-platform은 인프라 프로젝트다 | Accepted |
| [0003](0003-firestore-over-cloud-sql.md) | Firestore를 쓰고 Cloud SQL을 배제한다 | Accepted |
| [0004](0004-region-asia-northeast3.md) | 리전을 asia-northeast3로 고정한다 | Accepted |
| [0005](0005-no-pii-in-platform.md) | 플랫폼은 PII를 저장하지 않는다 | Accepted |
| [0006](0006-go-as-server-language.md) | 서버 언어를 Go로 한다 | Accepted |
| [0007](0007-openapi-as-contract-sot.md) | 계약의 SoT는 OpenAPI다 | Accepted |
| [0008](0008-platform-user-id-as-ledger-owner.md) | IAP 원장의 소유자 키를 platform_user_id로 한다 | Accepted |
| [0009](0009-apple-jws-verification-go.md) | Apple JWS 검증 Go 방안 | **Proposed** — P0 실측 후 확정 |
| [0010](0010-market-account-as-ownership-anchor.md) | 소유의 근거는 마켓 계정이다 | Accepted |
| [0011](0011-admin-management-boundary.md) | 플랫폼 관리 조작은 좁은 Admin 경계에서만 수행한다 | Accepted |
| [0012](0012-sandbox-reset-durable-intent.md) | sandbox reset은 영구 intent로 시작 순서를 확정한다 | Accepted |
| [0013](0013-platform-firebase-custom-token-bridge.md) | platform이 앱 Firebase custom token을 원격 서명한다 | Accepted |
| [0014](0014-google-play-refund-review.md) | Google Play 환불 검토 결정은 외부 호출 전에 영구 확정한다 | Accepted |
| [0015](0015-platform-ads-boundary.md) | 광고 검증을 platform-ads 경계로 분리한다 | Accepted |
| [0016](0016-iap-app-scoped-ledger.md) | 신규 IAP 원장을 앱 범위로 격리한다 | Accepted |
| [0018](0018-private-versioned-content-delivery.md) | 보호 콘텐츠는 private GCS 릴리스와 Platform 선택 API로 전달한다 | Accepted |
| [0019](0019-rpi-edge-presence.md) | RPI Edge presence와 fail-open 경계 | Accepted |
| [0020](0020-immutable-platform-fleet-release.md) | Platform Fleet release를 불변 manifest로 배포한다 | Accepted |
| [0021](0021-platform-fleet-reconciler-boundary.md) | Platform Fleet fan-out은 서명된 dry-run 계획에서 시작한다 | Accepted |
| [0022](0022-platform-fleet-approval-and-release-gate.md) | Fleet 승인은 broker FD로 서명하고 release build는 receipt로 차단한다 | Accepted |
| [0023](0023-consumable-content-ticket-and-flow-unlock.md) | 콘텐츠 열람권은 소모성 상품으로 검증하고 연간 흐름을 한 번에 해금한다 | Accepted |
| [0024](0024-provider-account-link-and-paid-access.md) | 외부 계정은 OIDC 어댑터로 연결하고 유료 접근은 연결 계정에 묶는다 | Accepted |
