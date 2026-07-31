# ADR 0004: 리전을 asia-northeast3로 고정한다

## Status

Accepted

## Context

Firestore와 BigQuery는 **생성 후 리전을 변경할 수 없다.** 잘못 고르면 데이터를 새로 만들어 옮기는 것 외에 방법이 없다.

BigQuery는 **교차 리전 조인이 불가**하므로 Firestore와 BigQuery가 같은 리전이어야 한다.

사용자는 대부분 한국에 있다. AppsInToss 트래픽은 사실상 100% 한국이다.

## Decision

**`asia-northeast3`(서울)로 고정한다.** Firestore, BigQuery, Cloud Run 전부.

## Consequences

- 한국 사용자의 네트워크 지연이 최소화된다. 결제 검증처럼 유저가 대기하는 경로에서 의미가 있다
- **되돌릴 수 없다.** 글로벌 확장이 필요해지면 멀티리전이 아니라 별도 배포를 검토해야 한다
- 기존 GA4 BigQuery export 데이터셋의 리전이 프로젝트마다 다르다는 점에 주의한다. 백오피스가 이미 이 문제를 겪어 데이터셋 메타에서 job location을 조회하는 방식으로 처리하고 있다. **US 폴백을 쓰지 않는다**
- 플랫폼이 AIT 이벤트를 GA4로 릴레이할 때 **요청 IP가 서울이 되어 geo가 KR로 붙는다.** AIT는 어차피 100% 한국이라 실질 손실이 없지만, 네이티브·RN 트래픽까지 릴레이하면 안 되는 이유가 이것이다

## Alternatives Considered

- **`asia-northeast1`(도쿄)** — 서비스 가용성이 더 넓지만 한국 사용자에게 지연이 늘어난다. 조직의 사용자 분포상 이점이 없다
- **`us-central1`** — 가장 저렴하고 신기능이 먼저 오지만 한국 사용자 지연이 크다. 결제 경로에 부적합
- **멀티리전** — 비용이 크게 늘고 현재 규모에 과설계다
