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
  --member="user:ih@toss.im" \
  --role="roles/run.invoker" \
  --project=seorilabs-platform
```

> 이 조직은 org policy `iam.allowedPolicyMemberDomains`가 seorilabs 디렉토리만 허용한다. **사람 계정 바인딩은 같은 디렉토리 소속이므로 통과하지만 `allUsers`는 막힌다.**

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
| `X-Seori-Actor: ih@toss.im` | 누가 눌렀는지. 없으면 서비스 계정 이름만 남는다 |

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
  -H "X-Seori-Actor: ih@toss.im" \
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
  -H "X-Seori-Actor: ih@toss.im" \
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
  -H "X-Seori-Actor: ih@toss.im" \
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

> **미실시**: `platform-admin`이 아직 배포되지 않아 리허설을 하지 못했다.
> 배포 후 반드시 한 번 돌린다. 그때까지 이 문서는 검증되지 않은 상태다.
