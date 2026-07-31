# BREAK-GLASS

백오피스가 죽었을 때 플랫폼을 직접 조작하는 긴급 절차.

**여기 적힌 명령은 실제 구현과 대조했다.** 문서와 코드가 어긋난 런북은
장애 중에 동작하지 않는 명령을 치게 만든다.

## 언제 쓰는가

`backoffice.vzyx.xyz`에 접근할 수 없는데 즉시 조작이 필요할 때. 원인은 보통 셋 중 하나다.

- RPI k8s 장애 — 노드 다운, 정전, ISP 장애
- 백오피스 배포 실패로 pod가 안 뜸
- **GitHub OAuth 장애로 로그인 불가**

## 전제 — 평시에 준비해둘 것

**이 절차는 GitHub에 의존하지 않는다.** GitHub 장애가 백오피스를 못 쓰게 만드는 원인 중 하나이기 때문이다.

```bash
# 사람 계정에 run.invoker를 평시에 부여해둔다. 장애 중에 IAM을 바꿀 수는 없다.
gcloud run services add-iam-policy-binding platform-admin \
  --region=asia-northeast3 \
  --member="user:ih@seorilabs.com" \
  --role="roles/run.invoker" \
  --project=seorilabs-platform
```

> **반드시 `@seorilabs.com` 계정이어야 한다.** org policy
> `iam.allowedPolicyMemberDomains`가 디렉토리 `C02f93h8p`(seorilabs.com)만
> 허용한다. `ih@seorilabs.com`으로 시도하면 다음과 같이 막힌다.
>
> ```
> FAILED_PRECONDITION: One or more users named in the policy do not
> belong to a permitted customer, perhaps due to an organization policy.
> ```
>
> 리허설에서 실제로 겪었다. 장애 중에 이걸 발견하면 늦는다.

### 인증이 두 겹이다

Cloud Run IAM을 통과해도 애플리케이션이 한 번 더 본다.

| 층 | 실패 시 |
|---|---|
| Cloud Run `run.invoker` | 403, 응답 본문 없음 |
| `ADMIN_ALLOWED_ACCOUNTS` | 403, `{"ok":false,"error":{"code":"auth_forbidden"}}` |

`auth_forbidden`이 오면 IAM은 통과했고 허용 목록에 계정이 없는 것이다.
`ADMIN_ALLOWED_ACCOUNTS` 환경변수를 확인한다.

## 준비

```bash
PROJECT=seorilabs-platform
REGION=asia-northeast3

ADMIN_URL="$(gcloud run services describe platform-admin \
  --project="$PROJECT" --region="$REGION" --format='value(status.url)')"

# --audiences를 빼면 audience가 맞지 않아 401이 온다.
TOKEN="$(gcloud auth print-identity-token --audiences="$ADMIN_URL")"
```

모든 요청에 두 헤더를 붙인다.

| 헤더 | 뜻 |
|---|---|
| `Authorization: Bearer $TOKEN` | OIDC 인증. 없으면 401 |
| `X-Seori-Actor: ih@seorilabs.com` | 누가 눌렀는지. 없으면 서비스 계정 이름만 남는다 |

`X-Seori-Actor`는 증명되지 않는 값이라 권한 판단에 쓰이지 않는다.
감사 기록에만 들어간다. 그래도 반드시 넣는다 — 나중에 이 기록을 보는
사람이 누가 했는지 알아야 한다.

## 절차

### 1. 상태 확인

```bash
curl -sS -H "Authorization: Bearer $TOKEN" "$ADMIN_URL/v1/admin/health"
```

```json
{"ok":true,"result":{"environment":"production","deadLetterCount":0}}
```

`deadLetterCount`가 0이 아니면 마켓에 완료를 알리지 못한 주문이 있다.
**Play는 3일 안에 acknowledge하지 않으면 자동 환불한다.** 급하다.

`environment`가 예상과 다르면 잘못된 서비스를 보고 있는 것이다.

### 2. 점검 모드 켜기

가장 자주 쓸 절차다. RemoteConfig의 `maintenance`가 켜지면 클라이언트가
점검 안내를 띄운다.

```bash
curl -sS -X POST "$ADMIN_URL/v1/admin/config/maintenance" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Seori-Actor: ih@seorilabs.com" \
  -H "Content-Type: application/json" \
  -d '{"appId":"lizard-tycoon","minutes":30}'
```

**본문 텍스트를 받지 않는다.** 앱과 시간만 받고 문구는 서버가 갖고 있다.
장애 중에 자유 텍스트 입력이나 외부 LLM 호출에 의존하면 안 된다.
`message` 같은 필드를 넣으면 400으로 거부된다.

### 3. 점검 모드 끄기

`minutes`를 0으로 준다.

```bash
curl -sS -X POST "$ADMIN_URL/v1/admin/config/maintenance" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Seori-Actor: ih@seorilabs.com" \
  -H "Content-Type: application/json" \
  -d '{"appId":"lizard-tycoon","minutes":0}'
```

### 4. 사용자 entitlement 조회

```bash
PUID="pu_01J..."
curl -sS -H "Authorization: Bearer $TOKEN" \
  "$ADMIN_URL/v1/admin/users/$PUID/entitlements"
```

비활성도 함께 온다. **왜 없는지를 봐야 원인을 찾는다.** `sources`에
어느 마켓에서 어떤 상태로 들어왔는지가 있다.

### 5. 최근 주문 조회

```bash
curl -sS -H "Authorization: Bearer $TOKEN" \
  "$ADMIN_URL/v1/admin/orders/recent?limit=20"
```

구매 토큰과 마켓 계정 해시는 응답에 없다. 운영자가 볼 이유가 없고
화면에 뜨면 스크린샷과 로그로 퍼진다.

### 6. 긴급 지급

```bash
REQUEST_ID="$(uuidgen | tr 'A-Z' 'a-z')"

curl -sS -X POST "$ADMIN_URL/v1/admin/entitlements/grant" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Seori-Actor: ih@seorilabs.com" \
  -H "Content-Type: application/json" \
  -d "{
    \"requestId\": \"$REQUEST_ID\",
    \"platformUserId\": \"$PUID\",
    \"entitlementId\": \"sp_galaxy_gecko\",
    \"reason\": \"장애 대응 수동 지급 — 사유를 구체적으로\",
    \"appId\": \"lizard-tycoon\"
  }"
```

- `requestId`를 바꾸지 않고 다시 부르면 두 번 지급되지 않는다.
  응답의 `applied`가 `false`면 이미 처리된 요청이다 — **실패가 아니다**
- `reason`은 비울 수 없다. 없으면 400이다
- 회수는 같은 형식으로 `/v1/admin/entitlements/revoke`

**회수는 돈을 내고 산 물건을 빼앗는 것이다.** 오지급 정정이 아니면 하지 않는다.

### 7. 원장 직접 조회

Admin API도 못 쓸 때. `cmd/fs`는 이 저장소의 조회 전용 CLI다.

```bash
go run ./cmd/fs get "iap_users/pu_01JXYZ/entitlements/sp_galaxy_gecko"
go run ./cmd/fs ls  "processed_orders" --limit=20
```

집계가 필요하면 BigQuery로 간다.

```bash
bq query --nouse_legacy_sql --maximum_bytes_billed=2000000000 \
'SELECT action, COUNT(*) FROM `seorilabs-platform.platform.audit`
 WHERE ts >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 HOUR)
 GROUP BY 1 ORDER BY 2 DESC'
```

## 아직 없는 것

문서에 있다고 착각하면 장애 중에 시간을 버린다. 지금 없는 기능이다.

| 기능 | 상태 |
|---|---|
| 앱 전체 kill switch (`pause`) | 미구현. 점검 모드로 대신한다 |
| support code로 사용자 조회 | 미구현. `platform_user_id`를 직접 써야 한다 |
| 환불 검토 대기열 조회 | 미구현 |
| App Store 샌드박스 초기화 | 미구현 |

## 하지 말 것

- **Firestore 콘솔에서 entitlement를 직접 고치지 않는다.** `active`는
  `sources`의 OR로 계산되고 주문 원장·내부 원장·공개 projection 셋이
  함께 갱신되어야 한다. 손으로 맞추면 어딘가 어긋난다
- **원장 문서를 삭제하지 않는다.** 어떤 상황에서도.
  `iap_completion_outbox`만 예외이고 그건 워커가 한다
- **워커를 여러 개 띄워 밀린 완료를 밀어내지 않는다.** lease가 중복을
  막지만 원인이 마켓 장애라면 더 두드려도 소용없다

## 사후

1. break-glass로 만든 변경이 백오피스 화면에 보이는가
2. `platform.audit`에 `X-Seori-Actor` 값과 함께 기록됐는가
   ```bash
   curl -sS -H "Authorization: Bearer $TOKEN" \
     "$ADMIN_URL/v1/admin/operator-grants?limit=5"
   ```
   `actorLogin`이 서비스 계정 이름이면 헤더를 빠뜨린 것이다
3. 점검 모드를 껐는가

## 리허설

**실행해본 적 없는 런북은 장애 중에 작동하지 않는다.**

```bash
kubectl -n platform scale deploy/backoffice --replicas=0
# → 게임에서 결제·RC·이벤트가 정상인지 확인
# → 위 절차로 점검 모드 on/off
kubectl -n platform scale deploy/backoffice --replicas=1
```

### 2026-07-31 실시 결과

`platform-admin`을 배포하고 전 구간을 실행했다. **여기 적힌 명령은
실제로 돌려본 것이다.**

| 항목 | 결과 |
|---|---|
| 인증 없이 호출 | 403 |
| 상태 확인 | `deadLetterCount: 0` |
| 최근 주문 조회 | 실제 원장 3건 |
| 점검 모드 on → 클라이언트 반영 | **약 6초** (캐시 TTL 60초 이내) |
| 점검 모드 off | 정상 |
| 운영자 지급 1차 | `applied: true` |
| 같은 requestId 2차 | `applied: false` (멱등 동작) |
| 감사 기록 | `actorLogin: ih@seorilabs.com`, `reason` 보존 |
| 백오피스 다운 중 config·이벤트·break-glass | 전부 정상 |

리허설에서 찾은 것 셋을 함께 고쳤다.

1. **admin role이 부팅에 실패했다.** `buildHandler`가 결제 설정을
   요구하는데 `newDeps`가 admin에서는 조립하지 않았다
2. **`platform-api`와 `platform-admin`의 Firestore prefix가 달랐다.**
   admin이 `stg_`, api가 production이라 서로 다른 컬렉션을 봤다.
   점검 모드를 켜도 클라이언트에 아무 변화가 없었고, 운영자는
   "켰다"고 믿는다. **배포 시 두 서비스의 prefix가 같은지 확인한다**
3. **SDK가 보내는 이벤트가 전부 버려지고 있었다.** `eventId`가 없으면
   서버가 200을 주면서 그 이벤트만 버린다. SDK는 성공으로 알고
   outbox에서 지워 조용히 유실됐다
