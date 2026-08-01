# Go 서버 로그 보기

플랫폼은 Cloud Run 서비스 넷과 Job 하나로 돈다. 전부 `seorilabs-platform`
프로젝트, `asia-northeast3` 리전이다.

| 이름 | 역할 | 리소스 종류 |
|---|---|---|
| `platform-api` | 세션·RemoteConfig | `cloud_run_revision` |
| `platform-iap` | 결제 검증·웹훅 | `cloud_run_revision` |
| `platform-ingest` | 이벤트 수집 | `cloud_run_revision` |
| `platform-admin` | 백오피스 전용 | `cloud_run_revision` |
| `platform-worker` | 완료 재시도 | `cloud_run_job` |

로그는 `log/slog` 구조화 JSON이다. 즉 `jsonPayload.*`로 필드를 직접
거를 수 있고, 문자열 grep에 기대지 않아도 된다.

접근 요청 한 줄은 이렇게 생겼다.

```json
{
  "level": "INFO",
  "msg": "요청",
  "method": "POST",
  "path": "/v1/iap/account-references",
  "status": 200,
  "latency_ms": 0,
  "trace": "11ece8d9.../1509294709...;o=1"
}
```

> 토큰·영수증·purchaseToken 원문은 로그에 넣지 않는다. 그래서 로그만
> 봐서는 "누구의 어떤 구매인지"를 알 수 없다. 그건 원장(`cmd/fs`)이나
> 백오피스에서 본다. 이 분리가 의도한 것이다.

## 1. 실시간으로 따라 보기

실기기 테스트 중에 제일 많이 쓴다.

진짜 스트리밍(`gcloud alpha logging tail`, `gcloud beta run services logs
tail`)은 **추가 컴포넌트 설치가 필요하다.** 한 번만 깔면 된다.

```bash
gcloud components install alpha        # 또는 beta
```

깔았다면 네 서비스를 한 번에 흘려볼 수 있다.

```bash
gcloud alpha logging tail \
  'resource.type="cloud_run_revision" AND resource.labels.service_name=~"^platform-"' \
  --project=seorilabs-platform \
  --format='value(timestamp, resource.labels.service_name, jsonPayload.method, jsonPayload.path, jsonPayload.status)'
```

깔지 않고 쓰려면 짧은 `--freshness`로 반복 조회한다. 설치 없이 바로
되고, 실기기 테스트에는 이걸로 충분하다.

```bash
while true; do
  gcloud logging read \
    'resource.type="cloud_run_revision" AND resource.labels.service_name=~"^platform-"' \
    --project=seorilabs-platform --freshness=2m --limit=20 \
    --format='value(timestamp, resource.labels.service_name, jsonPayload.method, jsonPayload.path, jsonPayload.status)'
  echo "----"
  sleep 20
done
```

> ⚠️ **`--order=asc`를 붙이면 `--freshness`가 먹지 않는다.** 시간순으로
> 보고 싶어 붙이기 쉬운데, 그러면 하루 전 로그가 그대로 딸려 온다.
> 실제로 겪었다 — "최근 10분"이라고 적어 둔 출력에 24시간 전 웹훅이
> 나왔다. 최신순(기본값)으로 두고 읽는 편이 안전하다.
>
> 같은 줄이 반복해서 나온다. `--freshness=2m`과 `sleep 20`이 겹치기
> 때문이다. 겹치게 두는 편이 낫다 — 줄이면 로그 반영 지연(보통 수 초)
> 때문에 요청을 통째로 놓친다.

## 2. 최근 오류만

가장 많이 쓰게 될 명령이다. 4xx 이상만 추린다.

```bash
gcloud logging read \
  'resource.type="cloud_run_revision"
   AND resource.labels.service_name=~"^platform-"
   AND jsonPayload.status>=400' \
  --project=seorilabs-platform --freshness=1h --limit=50 \
  --format='table(timestamp, resource.labels.service_name, jsonPayload.method, jsonPayload.path, jsonPayload.status)'
```

5xx만 보려면 `>=500`으로 바꾼다. 4xx는 대부분 클라이언트 실수라
평소에는 시끄럽고, 5xx가 우리 문제다.

## 3. 결제만

```bash
gcloud logging read \
  'resource.type="cloud_run_revision"
   AND resource.labels.service_name="platform-iap"
   AND jsonPayload.path=~"^/v1/iap/"' \
  --project=seorilabs-platform --freshness=30m --limit=50 \
  --format='table(timestamp, jsonPayload.method, jsonPayload.path, jsonPayload.status, jsonPayload.latency_ms)'
```

실기기에서 구매를 눌렀는데 앱만 실패할 때, 여기서 `status`가 200이면
서버는 제 일을 한 것이고 앱의 응답 해석이 문제다. 이번 전환에서 실제로
그런 경우가 있었다 — 서버가 계정 참조 필드 이름을 줄여 쓰는 바람에
클라이언트가 응답을 통째로 거부했고, 서버 로그에는 200만 남았다.

## 4. 한 요청을 끝까지 따라가기

`trace`가 같은 줄이 한 요청이다.

```bash
TRACE=11ece8d9869a2bde4b94304e4fa4a7c1
gcloud logging read \
  "resource.type=\"cloud_run_revision\" AND jsonPayload.trace=~\"^${TRACE}\"" \
  --project=seorilabs-platform --freshness=6h \
  --format='value(timestamp, resource.labels.service_name, jsonPayload.msg, jsonPayload.status)'
```

## 5. 워커

Job은 리소스 종류가 다르다. `cloud_run_revision`으로 거르면 안 잡힌다.

```bash
gcloud logging read \
  'resource.type="cloud_run_job" AND resource.labels.job_name="platform-worker"' \
  --project=seorilabs-platform --freshness=24h --limit=30 \
  --format='value(timestamp, jsonPayload.msg, textPayload)'
```

워커가 도는지, dead-letter가 생겼는지는 Admin API로도 볼 수 있다.

```bash
URL=https://platform-admin-306278488979.asia-northeast3.run.app
TOKEN=$(gcloud auth print-identity-token --audiences="$URL")
curl -s -H "Authorization: Bearer ${TOKEN}" "$URL/v1/admin/health"
# {"ok":true,"result":{"deadLetterCount":0,"environment":"sandbox"}}
```

> `--audiences`를 빼면 401이다. Cloud Run은 토큰의 `aud`가 서비스 URL과
> 같은지 본다. 다른 서비스용으로 발급된 토큰을 재사용하지 못하게 하는
> 장치라, 빠뜨리면 인증 실패로만 보이고 원인이 드러나지 않는다.

`deadLetterCount`가 0이 아니면 마켓에 완료를 알리지 못한 주문이 있다.
유저는 이미 물건을 받았고 마켓만 모르는 상태다 — Play는 3일 뒤 자동
환불하므로 사람이 봐야 한다.

## 6. 콘솔에서

명령을 외우기 싫을 때. Logs Explorer에 쿼리를 붙여넣으면 된다.

- [Logs Explorer](https://console.cloud.google.com/logs/query?project=seorilabs-platform)
- [Cloud Run 서비스](https://console.cloud.google.com/run?project=seorilabs-platform) → 서비스 → 로그 탭
- [Error Reporting](https://console.cloud.google.com/errors?project=seorilabs-platform) — 스택 트레이스가 자동으로 묶인다

## 7. 세션 안에서 — MCP

Claude Code 세션에서는 `gcloud-observability` MCP가 붙어 있어 셸을
거치지 않고 읽을 수 있다.

- `list_log_entries` — 위 필터 문자열을 그대로 넣는다
- `list_time_series` — 요청 수·지연·인스턴스 수 같은 지표
- `list_alerts` / `list_alert_policies` — 알림 상태

## 자주 보게 되는 것

| 로그 | 뜻 | 할 일 |
|---|---|---|
| `status: 403`, `anonymous_not_allowed` | AIT 익명 신원이 결제를 시도했다 | 정상 거부다. Firebase 익명 계정은 여기 해당하지 않는다 |
| `status: 409`, `purchase_owned_by_another_user` | 다른 uid가 소유한 구매다 | 재설치로 uid가 바뀐 경우가 대부분. 자동 이전은 하지 않는다 |
| `msg: "요청 처리 실패"` | 5xx 또는 정체불명 에러 | `err` 필드에 원인이 있다. 응답에는 넣지 않는다 |
| `msg: "정상 종료"` | 인스턴스가 스케일다운됐다 | 정상이다 |
| Job `완료 재시도 종료` | 워커가 한 바퀴 돌고 끝났다 | 정상이다 |

## 로그에 없는 것

- 토큰, refresh token, 영수증, purchaseToken 원문 — 의도적으로 뺀다
- 마켓 계정 식별자 원문 — sha256만 원장에 있다
- 어느 유저가 무엇을 샀는지 — 원장에서 본다

원장 조회는 `cmd/fs`나 백오피스 commerce 탭을 쓴다.

```bash
cd server
export GOOGLE_CLOUD_PROJECT=seorilabs-platform

# 최근 주문 나열
go run ./cmd/fs ls iap_environments/sandbox/processed_orders --limit 10

# 주문 하나
go run ./cmd/fs get iap_environments/sandbox/processed_orders/<orderKey>

# 어떤 사용자의 소유권
go run ./cmd/fs ls iap_users/<puid>/entitlements --prefix iap_environments/sandbox/
```

production 원장은 prefix가 없다. `processed_orders/...`로 바로 시작한다.
