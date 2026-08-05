# Firestore 인덱스

Firestore는 인덱스 없는 복합 쿼리를 **런타임에** 거부한다. 컴파일도 테스트도
통과하고 배포 후 첫 호출에서 터진다. 그래서 쿼리를 추가할 때 인덱스를 같이 만든다.

## 컬렉션 ID 기준이다

인덱스는 경로가 아니라 **컬렉션 ID**로 정의된다. 원장 경로는 환경에 따라 셋으로 갈리지만

```
iap_completion_outbox/{orderKey}                                   production 배포 + production 원장
iap_environments/sandbox/iap_completion_outbox/{orderKey}          production 배포 + sandbox 원장
stg_iap_environments/sandbox/iap_completion_outbox/{orderKey}      staging 배포 + sandbox 원장
```

컬렉션 ID는 셋 다 `iap_completion_outbox`다. `COLLECTION` scope 인덱스 하나가 전부를 덮는다.

## 필요한 복합 인덱스

### `iap_completion_outbox` — 완료 재시도 대기열

| 필드 | 순서 |
|---|---|
| `platform` | ASC |
| `status` | ASC |
| `nextAttemptAt` | ASC |

`ledger.ClaimNext`가 쓴다. 자격증명이 있는 마켓의 pending 항목 중 시간이 된 것을
가장 오래 기다린 순으로 하나 집는다.

```bash
gcloud firestore indexes composite create \
  --project=seorilabs-platform --billing-project=seorilabs-platform \
  --collection-group=iap_completion_outbox \
  --query-scope=COLLECTION \
  --field-config=field-path=platform,order=ascending \
  --field-config=field-path=status,order=ascending \
  --field-config=field-path=nextAttemptAt,order=ascending
```

생성에 수 분이 걸린다. 배포 전에 미리 만들어 둔다.

### `pending_refund_reviews` — Google Play 환불 검토

대기열 조회, 24시간 만료 sweep, 결정 제출 worker가 각각 다른 복합 쿼리를 쓴다.

| 쿼리 | 필드 순서 |
|---|---|
| 앱 전체 목록 | `appId ASC`, `dueAt ASC` |
| 앱 상태별 목록 | `appId ASC`, `state ASC`, `dueAt ASC` |
| 만료·마감 임박 | `state ASC`, `dueAt ASC` |
| 제출할 결정 claim | `state ASC`, `nextAttemptAt ASC` |

```bash
for fields in \
  'appId:ascending,dueAt:ascending' \
  'appId:ascending,state:ascending,dueAt:ascending' \
  'state:ascending,dueAt:ascending' \
  'state:ascending,nextAttemptAt:ascending'; do
  args=()
  IFS=',' read -r -a pairs <<< "$fields"
  for pair in "${pairs[@]}"; do
    args+=("--field-config=field-path=${pair%%:*},order=${pair##*:}")
  done
  gcloud firestore indexes composite create \
    --project=seorilabs-platform --billing-project=seorilabs-platform \
    --collection-group=pending_refund_reviews \
    --query-scope=COLLECTION "${args[@]}"
done
```

`state IN (...)`도 equality 필드로 인덱스의 첫 자리에 둔다. `dueAt`과
`nextAttemptAt`은 range와 정렬에 함께 쓰므로 마지막 필드다.

## 단일 필드로 충분한 쿼리

Firestore가 자동으로 인덱싱하므로 따로 만들지 않는다.

- `iap_completion_outbox` where `status == dead_letter` — `CountDeadLetters`
- `iap_users/{puid}/entitlements` where `active == true` — `ListActive`
- `pending_refund_reviews` where `state IN (...)` — 전체 pending health count
- `pending_refund_reviews` where `state == failed` — failed health count

## 인덱싱하지 않는 필드

`canonicalId`는 **단일 필드 인덱스를 끈다.** 마켓 구매 토큰이라 인덱스에 남기면
검색 가능한 형태로 보존된다. 조회는 언제나 `orderKey`(sha256) 기준으로 한다.

필드 이름은 플래그가 아니라 positional 인자다.

```bash
gcloud firestore indexes fields update canonicalId \
  --project=seorilabs-platform --billing-project=seorilabs-platform \
  --collection-group=processed_orders \
  --disable-indexes
```

`processed_orders`는 적용을 확인했다. 이 명령은 응답이 오래 걸리므로
`--quiet`을 붙이고 결과는 아래 조회로 따로 확인한다.

```bash
gcloud firestore indexes fields list \
  --project=seorilabs-platform --billing-project=seorilabs-platform \
  --collection-group=processed_orders
```

환불 검토의 `secret` map은 AES-GCM ciphertext라 조회 조건으로 사용할 일이 없다.
불필요한 색인 복제를 막기 위해 단일 필드 인덱스를 끈다.

```bash
gcloud firestore indexes fields update secret \
  --project=seorilabs-platform --billing-project=seorilabs-platform \
  --collection-group=pending_refund_reviews \
  --disable-indexes --quiet
```

## 확인

```bash
gcloud firestore indexes composite list \
  --project=seorilabs-platform --billing-project=seorilabs-platform \
  --format="table(name.basename(),queryScope,state)"
```

`state`가 `READY`여야 쿼리가 동작한다. `CREATING` 중에는 여전히 실패한다.
