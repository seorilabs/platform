# ADR 0011 — 플랫폼 관리 조작은 좁은 Admin 경계에서만 수행한다

## 상태

채택. 2026-08-02.

## 맥락

백오피스가 공통 인증과 IAP를 관리하려면 런타임 데이터를 조회하고 일부
entitlement를 조작해야 한다. 조회 자격증명과 조작 자격증명이 같거나,
백오피스가 Firestore를 직접 쓰면 한 번의 자격증명 유출이 전체 원장 수정으로
이어진다. 앱·사용자·원장 환경을 서로 대조하지 않는 수동 지급은 다른 앱이나
production 원장에 잘못 적용될 수 있다.

## 결정

기존 private `platform-admin`을 유일한 관리 경계로 유지한다.

- `ADMIN_READ_ALLOWED_ACCOUNTS`와 `ADMIN_WRITE_ALLOWED_ACCOUNTS`를 분리한다.
  GET은 두 목록 중 하나, mutation은 write 목록만 허용한다. 예전
  `ADMIN_ALLOWED_ACCOUNTS`에는 fallback하지 않는다.
- Admin role은 `PLATFORM_SESSION_SECRET`과 identity issuer를 조립하지 않는다.
  PII 없는 `platformUserId`·`supportCode` 조회 저장소만 주입한다.
- 백오피스 MySQL에는 런타임 사용자 데이터를 저장하지 않는다. 매 조회는
  Admin API가 플랫폼 원장에서 직접 읽는다.
- IAP mutation은 `expectedEnvironment`, 앱과 PUID의 소속, 앱 레지스트리의
  IAP 활성화와 원장 환경, 카탈로그 entitlement, 서버가 계산한 typed
  confirmation을 모두 검증한다.
- Admin role은 SKU 카탈로그만 읽고 마켓 자격증명과 계정 바인딩 키를 갖지 않는다.
  백오피스에는 `/v1/admin/iap/catalog`로 entitlement ID만 노출한다.
- 운영자 지급은 order, entitlement projection, 영구 감사 레코드를 한 Firestore
  트랜잭션에서 쓴다. 같은 `requestId`의 다른 payload는 409로 거부한다.
- 회수는 `grantRequestId`가 가리키는 활성 operator source만 revoked로 바꾼다.
  마켓 구매 source는 변경하지 않는다.
- 인증된 OIDC principal별 Admin mutation을 Firestore에서 분당 5회, 시간당 20회,
  일 50회로 제한한다. `X-Seori-Actor`는 증명되지 않았으므로 한도 키가 아니다.
- `reason`은 네 가지 고정 코드만 저장한다. `X-Seori-Actor`는 GitHub login 형식만
  받고, 없거나 형식이 다르면 OIDC 이메일 원문 대신 전체 SHA-256 참조를 저장한다.
- grant·revoke·sandbox reset의 `requestId`는 조작 종류를 넘어 중복될 수 없다.
  sandbox reset의 대상 주문·projection·영구 요청 레코드도 한 트랜잭션으로 쓴다.
- Admin 응답은 내부 ledger·identity struct를 직접 직렬화하지 않고 명시 DTO로
  투영한다. 계약에 없는 미래 필드가 브라우저 payload에 자동 노출되지 않는다.

## 결과

- read 자격증명 유출만으로 entitlement를 바꿀 수 없다.
- 요청 재시도 중 감사 레코드만 먼저 남아 실제 지급이 영구히 건너뛰어지는 상태를
  만들지 않는다.
- 운영자 회수 이력의 `grantRequestId`로 백오피스가 지급 source의 현재 상태를
  정확히 합성할 수 있다.
- 기존 `processed_orders`에는 `appId`가 없다. 최근 주문의 앱은 PII 없는 identity
  binding으로만 도출하며, 삭제 사용자나 owner 없는 tombstone은 빈 값으로 둔다.
- 현재 카탈로그는 배포 단위 전역이다. 여러 IAP 앱을 한 Admin 배포에 올리기 전에
  앱별 카탈로그 경계를 별도 결정해야 한다.

## 환경변수 마이그레이션

1. 기존 `ADMIN_ALLOWED_ACCOUNTS` 값을 최소 권한에 따라 read/write 두 목록으로 나눈다.
2. `ADMIN_READ_ALLOWED_ACCOUNTS`와 `ADMIN_WRITE_ALLOWED_ACCOUNTS`를 같은 revision에 넣는다.
3. 새 revision의 GET·mutation 권한을 각각 확인한다.
4. 검증 후 `ADMIN_ALLOWED_ACCOUNTS`를 제거한다. 코드는 이 값을 경고만 하고 사용하지 않는다.

Admin role에는 `IAP_CATALOG_JSON`이 필요하고 `PLATFORM_SESSION_SECRET`, 마켓 API 키,
`IAP_ACCOUNT_BINDING_KEYS`는 필요하지 않다.
