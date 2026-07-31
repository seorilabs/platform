# Ops

## 관측

**Cloud Monitoring을 쓴다. Grafana로 끌어오지 않는다.**

vzyx-cluster의 rpi4001이 이미 CPU 66% / MEM 61%로 포화에 근접해 있다. exporter와 스크레이프 대상을 추가하는 건 명백히 나쁜 선택이다.

알림 채널만 **Telegram**으로 통일한다. 백오피스가 이미 라우터를 갖고 있다.

### 필수 알림

| 알림 | 조건 |
|---|---|
| 5xx 비율 | > 2% (5분 창) |
| p95 지연 | > 2s |
| 인스턴스 포화 | 인스턴스 수 = `max-instances` — 비용 폭주 + 장애 신호 |
| **IAP dead-letter 발생** | 1건이라도 |
| **결제 검증 실패율 급증** | 7일 중앙값 대비 급등 |
| **Billing budget** | **20달러 / 50달러** |

Billing budget 알림이 **최후 방어선**이다. 없으면 사고를 청구서로 알게 된다.

### 로깅

`log/slog` 구조화 JSON. 필드는 `severity`, `trace`, `app_id`, `platform_user_id`, `route`, `latency_ms`.

**절대 로그 금지**: 토큰, refresh token, 영수증, `purchaseToken`, FCM 토큰 원문, 마켓 계정 식별자 원문.

에러 추적은 Cloud Error Reporting — Cloud Logging에서 자동 파생되고 비용 0. **Sentry를 도입하지 않는다.**

## 보안

| 항목 | 결정 |
|---|---|
| App Check | **optional로 시작.** Godot에 App Check SDK가 없고, replay 방지(`consume`)가 Go SDK에 없다. 자체 nonce 저장소는 후속 |
| Rate limit | **Firestore 기반.** 결제 경로는 인스턴스 로컬 토큰 버킷으로 부족하다 |
| 비용 상한 | `max-instances` 하드 고정. DDoS를 못 막아도 **청구서는 막는다** |
| Kill switch | 레지스트리 `status: paused` → 403 / `blocked_uids` / RC `maintenance` |
| Cloud Armor | **도입하지 않는다.** 외부 HTTPS LB 고정비가 월 18달러 이상이라 규모 대비 정당화 불가 |
| 마켓 자격증명 | **`platform-iap` 서비스에만** 마운트 → R3 |
| 마켓 계정 ID | 원문 저장 금지. sha256만 |

## 비용

| 시나리오 | 비용 |
|---|---|
| **현재 DAU 44** | **0원** — 무료 한도의 1~2% |
| 10배 | **0원** |
| 100배 | 월 2~8달러 |

무료 한도: Firestore 일 5만 read / 2만 write / 1GiB · Cloud Run 월 200만 요청 / 18만 vCPU-초 · BigQuery 월 1TiB 쿼리 / 10GiB 저장 / 2TiB Storage Write.

### 진짜 위험은 사용량이 아니라 사고다

| 위험 | 대응 |
|---|---|
| 클라이언트 무한 재시도 루프 | `max-instances` 하드 고정. SDK 백오프를 conformance 벡터로 CI 강제 |
| BigQuery 전체 스캔 쿼리 하나 | `maximum_bytes_billed` 강제 + 파티션·클러스터링 |
| `min-instances=1` 상시 과금 | **전부 0** |
| Firestore named DB 무료 티어 0 | staging은 `(default)` + prefix |
| Cloud Logging 월 50GiB | sink 설정, 프로덕션 DEBUG 금지 |

## 조회 수단

`gcloud`에는 **Firestore 문서 조회 명령이 없다.** export/import/bulk-delete/indexes만 있다.

| 수단 | 용도 |
|---|---|
| GCP 콘솔 Firestore Studio | 눈으로 브라우징 |
| **`cmd/fs` 조회 전용 CLI** | 터미널·에이전트 조회. **쓰기 명령 미제공** |
| `bq query` | 집계·기간·크로스앱 질문 전부 |
| Firestore 에뮬레이터 UI | 로컬 개발 |

Firestore는 조인·`LIKE`·집계가 안 되므로 **운영 질문은 BigQuery `platform.audit`로 답한다.** 모든 쓰기 작업이 여기에 한 줄씩 append된다.

## 런북

- [BREAK-GLASS.md](BREAK-GLASS.md) — 백오피스 다운 시 긴급 조작
