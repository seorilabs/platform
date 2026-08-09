# ADR 0016: 신규 IAP 원장을 앱 범위로 격리한다

## 상태

Accepted — 2026-08-09

## 맥락

기존 IAP 원장은 한 서비스가 `lizard-tycoon` 하나만 처리한다는 전제로 환경
아래에 바로 컬렉션을 만들었다. Happy Farm에 `ad_free`를 추가하면 같은
production 환경의 앱들이 주문, entitlement, webhook event, completion outbox를
공유하게 된다. verifier만 앱별로 나눠도 worker가 다른 앱의 outbox를 집을 수
있으므로 `(appId, market, productId)` 경계를 끝까지 보존하지 못한다.

기존 lizard 원장 경로를 옮기면 이미 발급된 entitlement와 운영 도구가 다른
원장을 보게 된다. `/v1/iap/*` 응답 계약뿐 아니라 저장 데이터 호환도 유지해야 한다.

## 결정

- 신규 앱 IAP 원장은 환경 prefix 아래 `iap_apps/{appId}/...`에 저장한다.
- verify, entitlement 조회, webhook, completion worker, 광고 정책 projection 조회가
  모두 같은 앱 범위 원장을 사용한다.
- 기존 원장을 유지해야 하는 앱만 registry에 `iap.legacy_unscoped_ledger=true`를
  명시한다. 현재 대상은 `lizard-tycoon` 하나다.
- 새 앱에는 이 플래그를 사용하지 않는다. Happy Farm은
  `iap_apps/happy-farm/...`을 사용한다.
- IAP 문서의 append-only와 삭제 금지 불변식은 앱 범위 안에서도 그대로 적용한다.

## 결과

같은 환경과 같은 entitlement 이름을 쓰는 앱도 원장, webhook, worker가 섞이지
않는다. lizard의 기존 데이터 경로와 SDK 응답은 바뀌지 않는다. 운영자가
`legacy_unscoped_ledger`를 잘못 제거하면 새 빈 원장을 보게 되므로 변경은 registry
리뷰와 `regsync` readback을 거쳐야 한다.
