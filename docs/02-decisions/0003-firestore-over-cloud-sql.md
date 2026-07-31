# ADR 0003: Firestore를 쓰고 Cloud SQL을 배제한다

## Status

Accepted

## Context

플랫폼은 identity 매핑, RemoteConfig, **IAP entitlement 원장**을 저장해야 한다.

조직은 아직 수익이 나지 않는 1인 기업이고 **비용이 가장 큰 제약**이다. 실측 규모는 전 앱 합계 DAU 44, 이벤트 12,689건/일이다.

조직의 기존 스택은 TypeScript + Prisma + MySQL이라 SQL과 ORM에 익숙하다.

## Decision

**Firestore를 쓴다. Cloud SQL을 도입하지 않는다.**

이벤트는 Firestore에 넣지 않고 **BigQuery에만** 적재한다.

| 워크로드 | 저장소 | 근거 |
|---|---|---|
| identity 매핑 | Firestore, 문서 ID = `{app_id}__{uid}` | 쿼리가 아닌 직접 읽기 1회 |
| RemoteConfig | Firestore + 인메모리 캐시 | 문서 소수, 읽기 캐시가 압도적 |
| **IAP 원장** | Firestore 트랜잭션 | lizard-tycoon이 이미 1,421줄 repository로 운영 중 |
| 이벤트 | **BigQuery만** | Firestore write가 최대 비용 항목 |

### 비용이 결정타다

| | Cloud SQL | Firestore |
|---|---|---|
| 과금 기준 | **인스턴스 가동 시간** | 읽은/쓴 문서 수, 저장 용량 |
| 트래픽 0일 때 | **월 10~25달러 고정** | **0원** |
| 무료 한도 | 없음 | 일 5만 read / 2만 write / 1GiB |

**P0에서 실측 확인**: 생성된 데이터베이스가 `freeTier: true`를 반환했다. `(default)` 데이터베이스이므로 무료 할당량이 적용된다. named database를 만들지 않고 컬렉션 prefix로 staging을 나누는 결정의 근거가 이것이다.

`min-instances=0` 전제와 Cloud SQL의 고정비는 정면 충돌한다. Cloud Run scale-to-zero와 커넥션 풀의 궁합도 나쁘다.

### IAP 원장을 Firestore로 두는 근거

결제는 엄격한 일관성이 필요하지만 **Firestore 트랜잭션으로 충분하다.** lizard-tycoon이 이미 그렇게 운영하고 있고, 불변식 12개가 전부 Firestore 트랜잭션 위에서 성립하고 있다.

## Consequences

- **Prisma를 못 쓴다.** 조직 익숙함과 마찰이 있다. `@google-cloud/firestore` Go 클라이언트를 **repository 포트 뒤에 격리**해 나중 전환 여지를 남긴다
- **조인·`LIKE` 검색·집계가 안 된다.** 그래서 **모든 쓰기 작업을 BigQuery `platform.audit`에 append**한다. "지난주 결제 성공률" 같은 운영 질문은 전부 SQL로 답한다
- **복합 쿼리는 사전 인덱스가 필수다.** 인덱스 없는 쿼리는 런타임에 에러가 난다. 필요한 인덱스 5종을 `03-architecture/iap.md`에 명시했다
- **`gcloud`에 Firestore 문서 조회 명령이 없다.** export/import/indexes만 있어서 `cmd/fs` 조회 CLI를 직접 만든다
- **staging을 named database로 나눌 수 없다.** 무료 할당량이 `(default)` DB에만 적용되므로 컬렉션 prefix로 나눈다
- 규모가 커져 SQL이 절실해지면 repository 포트 구현만 교체한다

## Alternatives Considered

- **Cloud SQL PostgreSQL** — SQL·조인·텍스트 검색·Prisma가 전부 되지만 트래픽 무관 고정비가 붙는다. DAU 44 규모에서 정당화 불가
- **RPI k8s의 기존 MySQL 재사용** — 추가 과금 0이지만 앱 클라이언트가 가정용 회선·단일 노드·HA 없는 인프라에 의존하게 된다. 결제 경로에는 부적합
- **Firestore Datastore 모드** — Native 모드가 실시간 리스너와 문서 모델을 주는데 Datastore 모드는 이점이 없다
