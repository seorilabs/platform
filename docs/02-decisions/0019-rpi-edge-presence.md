# ADR 0019 — RPI Edge presence와 fail-open 경계

## 상태

승인

## 맥락

웹에서 앱별 실시간 동시 접속을 상시 표시하려면 클라이언트가 짧은 주기로
활성 상태를 알려야 한다. 이 heartbeat를 BigQuery에 적재하고 매분 조회하면 현재
규모에서는 무료 구간일 수 있지만, 상시 쿼리와 Cloud Run 요청이 사용량에 따라
늘어나는 비용 경로가 된다.

`edge.vzyx.xyz`는 집에서 운영하는 RPI 클러스터를 가리킨다. 전원, 인터넷,
Ingress, TLS, 프로세스, MySQL 중 하나가 중단될 수 있으므로 이 경로가 앱 시작,
인증, 게임 진행, 결제나 기존 분석을 막아서는 안 된다.

## 결정

- `edge.vzyx.xyz`를 외부 앱이 RPI 클러스터와 통신하는 안정적인 공개 API 경계로
  사용한다. 기능은 `/v1/{domain}/{action}` 경로로 나누고 presence 첫 계약은
  `POST /v1/presence/heartbeat`다.
- Platform ingest가 앱별 기능 플래그를 확인한 뒤 1시간짜리 Ed25519 presence
  token을 발급한다. token은 `aud=edge.vzyx.xyz`, `scope=presence:write`, 앱 ID와
  SHA-256 session key만 담는다. RPI에는 공개키만 둔다.
- heartbeat는 별도 SDK 모듈과 별도 HTTP 연결을 사용한다. 게임 흐름에서 await하지
  않고, 요청당 2초 안에 포기하며, 같은 heartbeat를 재시도하거나 outbox에 넣지
  않는다.
- 실패 뒤 다음의 **새 heartbeat**만 `60초 → 120초 → 300초 상한` 간격과 jitter로
  보낸다. RPI 복구 뒤 최대 5분 안에 새 상태가 다시 채워진다.
- Edge는 MySQL에 `(app_id, session_hash)`별 최신 `last_seen_at`만 upsert한다.
  클라이언트 시각은 활성 판정에 쓰지 않고 서버 수신 시각으로 150초 TTL을 정한다.
- DNS, TLS, Edge, DB 장애 시 presence만 유실한다. Cloud Run이나 BigQuery로
  우회하지 않는다. 장애가 클라우드 비용 증가로 바뀌면 안 된다.
- Backoffice는 Edge readiness가 확인되지 않으면 동접을 0으로 표시하지 않고
  `알 수 없음`과 마지막 정상 시각을 보여준다.

## 결과

- RPI 전체가 내려가도 앱 동작과 기존 GA4·Platform 이벤트 전송은 계속된다.
- Presence는 정확한 접속 원장이 아니라 최근 heartbeat 기반 운영 근사치다.
- 장애 구간은 복구하지 않는다. 오래된 heartbeat 재생이 현재 동접을 왜곡하는 것보다
  의도된 유실이 안전하다.
- 공개 Edge endpoint에는 작은 본문 상한, token/session rate limit, 제한된 DB pool,
  짧은 deadline을 둔다. 과부하는 무한 queue 대신 `429` 또는 `503`으로 밀어낸다.
- RPI MySQL과 인터넷 회선은 단일 장애점으로 남는다. 이 ADR의 목표는 presence의
  무중단이 아니라 **presence 장애를 제품 장애로 전파하지 않는 것**이다.
