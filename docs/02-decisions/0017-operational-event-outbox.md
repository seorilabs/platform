# ADR 0017 — 운영 이벤트 outbox와 Backoffice 전달 경계

## 상태

승인

## 맥락

Discord를 운영 알림 화면으로 쓰려면 신규 플랫폼 사용자, IAP 지급, 광고 보상
지급처럼 서버가 확정한 사건을 Backoffice로 보내야 한다. 요청 처리 뒤 별도 HTTP
호출만 하면 도메인 커밋은 성공했는데 알림이 유실될 수 있고, 사용자 식별자를
알림에 넣으면 Discord와 Backoffice의 개인정보 범위가 불필요하게 넓어진다.

## 결정

- `identity.created`, `iap.granted`, `ad.reward.delivered`만 확정 상태 전이와 같은
  Firestore transaction에서 `operational_event_outbox`에 기록한다.
- event ID는 내부 사용자 ID·주문 키·claim ID를 SHA-256으로 다시 해시해 만들고,
  payload에는 사용자 식별자, 영수증, purchase token, provider order ID를 넣지 않는다.
- API, IAP, Ads role은 요청 응답 뒤 짧게 outbox를 비우며, Worker가 주기적으로
  만료 lease와 미전송 항목을 재시도한다.
- Platform은 timestamp와 본문을 HMAC-SHA256으로 서명한다. Backoffice는 5분
  replay window, event allowlist, attribute allowlist를 통과한 요청만 받는다.
- Backoffice가 이벤트를 저장하고 Discord 채널 선택·표현·재시도를 담당한다.
  Platform은 Discord webhook을 알지 못한다.

## 결과

- 도메인 커밋과 event enqueue가 원자적이며 Backoffice 장애가 사용자 요청을
  직접 실패시키지 않는다.
- Discord 공급자를 교체해도 Platform 계약은 바뀌지 않는다.
- 전달 쿼리에 복합 Firestore 인덱스 두 개가 필요하고, 배포 전 `READY` 상태를
  확인해야 한다.
- Secret Manager의 `backoffice-operational-events-secret`은 로컬 카탈로그
  `shared/platform/backoffice-operational-events-secret`과 같은 값을 사용한다.
  `platform-api`, `platform-ads`, `platform-worker`에는 이 secret에 대한
  resource-level `secretAccessor`만 부여한다. `platform-iap`은 기존 프로젝트
  범위 accessor를 사용한다.
