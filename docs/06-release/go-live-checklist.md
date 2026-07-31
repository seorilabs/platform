# 전환 체크리스트

lizard-tycoon을 Firebase Functions IAP에서 플랫폼으로 옮기는 절차.

**전환 스위치는 꺼진 채 커밋되어 있다.** 아래를 통과하기 전에 켜지 않는다.

## 현재 상태

구현은 전부 끝났고 검증도 코드로 할 수 있는 것은 전부 했다.
남은 셋은 외부 시스템에서 사람이 조작해야 한다.

| 항목 | Apple | Play | AIT |
|---|---|---|---|
| 마켓 인증 | ✅ | ✅ | ⏳ 인증서 |
| 실제 구매 검증 | ✅ | ✅ | — |
| shadow 대조 (orderKey 일치) | ✅ | — | — |
| 웹훅 (실데이터) | ✅ | ✅ | 해당 없음 |
| 완료 호출 경로 | ✅ | ✅ | 클라이언트 담당 |
| 완료 **반영** 확인 | ✅ | ⏳ 활성 구매 | — |
| RTDN 실연동 | 해당 없음 | ⏳ 콘솔 등록 | — |
| mTLS 배선 | — | — | ✅ |

검증 근거는 [market-verification.md](../07-qa/market-verification.md)에 있다.

---

## 1. Play 활성 구매 만들기 — 기기 필요

**왜 필요한가.** acknowledge가 실제로 반영되는지 확인해야 한다.
Play는 3일 안에 acknowledge하지 않으면 자동 환불한다. 이 경로가
막혀 있으면 유저는 산 물건을 잃고 우리는 매출을 잃는다.

**왜 코드로 못 하는가.** 활성 구매의 `purchaseToken`을 얻는 API가
없다. `androidpublisher` v3 discovery로 확인했다 — `orders.get`이
없고 `voidedpurchases.list`는 환불된 것만 준다.

### 절차

1. lizard-tycoon을 Play 내부 테스트 트랙에 올린다
2. 라이선스 테스터 계정으로 `sp_galaxy_gecko`를 구매한다
3. **acknowledge하지 않은 상태로** 토큰을 확보한다
   - 앱이 자동으로 acknowledge하면 검증 대상이 사라진다
   - `seori-platform.config.json`의 `enabled`가 `false`인 빌드로
     구매하면 기존 Functions가 처리하므로, 그 전에 토큰을 로그로 뽑는다
4. 토큰을 넘겨주면 아래를 확인한다

```bash
export PLAY_REAL_PURCHASE_TOKEN="<토큰>"
go test -tags=market ./internal/iap/providers/ -run TestPlayReal -v
go test -tags=market ./internal/iap/providers/ -run TestPlayAcknowledge -v
```

기대: `state: active`, `completion: google_acknowledge`,
acknowledge 후 재조회 시 `acknowledgementState: ACKNOWLEDGED`

---

## 2. RTDN 실연동 — Play Console 필요

**왜 필요한가.** 지금 검증은 우리가 Pub/Sub에 직접 넣은 것이다.
Google이 실제로 보내는지는 확인되지 않았다.

**왜 코드로 못 하는가.** RTDN topic 설정 API가 discovery에 없다.
Play Console UI 전용이다.

### 절차

Play Console → 앱 → **수익 창출 설정** → 실시간 개발자 알림

```
projects/seorilabs-platform/topics/play-iap-rtdn
```

topic은 이미 만들어져 있고 push subscription도 걸려 있다.

> **주의**: 등록하면 기존 Firebase Functions로 가던 RTDN이 여기로
> 온다. 전환 시점에 하는 것이 맞고, 그전에 하면 기존 시스템이
> 환불 알림을 못 받는다.

등록 후 "테스트 알림 보내기"를 누르면 확인할 수 있다.

```bash
gcloud logging read 'resource.labels.service_name="platform-iap"
  AND httpRequest.requestUrl:"webhooks/play"' \
  --project=seorilabs-platform --limit=5 --freshness=5m \
  --format="value(httpRequest.status,jsonPayload.msg)"
```

기대: `200`, `알림 반영`

---

## 3. AppsInToss mTLS 인증서 — 파트너 콘솔 필요

**왜 필요한가.** AIT는 mTLS로 인증한다. 인증서가 없으면 검증기가
조립되지 않고 AIT 결제만 `platform_unavailable`로 거부된다.

**배선은 이미 검증했다.** 자체 서명 CA로 실제 TLS 핸드셰이크를
통과시켰고, 인증서 없는 연결·다른 CA·서버 미검증이 전부 거부되는
것까지 확인했다. **인증서를 받는 즉시 동작한다.**

### 절차

1. AppsInToss 파트너 콘솔에서 클라이언트 인증서를 발급받는다
2. Secret Manager에 올린다

```bash
gcloud secrets create ait-client-cert --project=seorilabs-platform \
  --data-file=client.crt
gcloud secrets create ait-client-key --project=seorilabs-platform \
  --data-file=client.key

for s in ait-client-cert ait-client-key; do
  gcloud secrets add-iam-policy-binding $s --project=seorilabs-platform \
    --member="serviceAccount:platform-iap@seorilabs-platform.iam.gserviceaccount.com" \
    --role="roles/secretmanager.secretAccessor"
done
```

3. `platform-iap`에 붙인다

```bash
gcloud run services update platform-iap \
  --project=seorilabs-platform --region=asia-northeast3 \
  --update-secrets="IAP_TOSS_CLIENT_CERT=ait-client-cert:latest,IAP_TOSS_CLIENT_KEY=ait-client-key:latest"
```

부팅 로그에 `결제 준비 완료 markets=[... apps_in_toss]`가 나오면 된다.

---

## 자동 검증

셋을 조작한 뒤 한 번에 확인한다.

```bash
PLAY_ACTIVE_TOKEN="<기기에서 얻은 토큰>" scripts/verify_go_live.sh

# 일부만
scripts/verify_go_live.sh rtdn
scripts/verify_go_live.sh ait
```

| exit | 뜻 |
|---|---|
| 0 | 전부 통과. 전환으로 넘어간다 |
| 1 | 실패한 항목이 있다 |
| 2 | 아직 조작하지 않은 항목이 있다 |

Play 항목은 acknowledge **호출 성공**만 보지 않고 마켓에 다시 물어
`ACKNOWLEDGED`로 바뀌었는지 확인한다. 호출이 성공해도 반영되지
않으면 3일 뒤 자동 환불된다.

## 4. 전환 — 위 셋이 끝난 뒤

### 4.1 서비스 배포

production 환경으로 올린다. **`platform-api`와 `platform-iap`의
`PLATFORM_FS_PREFIX`가 같아야 한다.** 다르면 서로 다른 컬렉션을 보고,
점검 모드를 켜도 클라이언트에 아무 변화가 없다. 리허설에서 실제로 겪었다.

```bash
gcloud run services describe platform-api --region=asia-northeast3 \
  --format="value(spec.template.spec.containers[0].env)" | grep -i prefix
gcloud run services describe platform-iap --region=asia-northeast3 \
  --format="value(spec.template.spec.containers[0].env)" | grep -i prefix
```

`IAP_LEDGER_ENVIRONMENT`도 `production`으로 바꾼다. Apple 환경과
어긋나면 부팅이 실패한다 — 그게 안전장치다.

### 4.2 앱 설정

`lizard-tycoon/firebase/seori-platform.config.json`

```json
{
  "enabled": true,
  "baseUrl": "https://platform-api-....run.app",
  "appId": "lizard-tycoon"
}
```

### 4.3 shadow 기간

**기존 Firebase Functions를 지우지 않는다.** 일정 기간 두 경로가
같은 결과를 내는지 대조한다. orderKey 일치는 이미 확인했지만
운영 중 데이터로 다시 본다.

```bash
# 플랫폼 원장
go run ./cmd/fs ls "processed_orders" --limit=20

# 기존 원장
GOOGLE_CLOUD_PROJECT=lizard-tycoon \
  go run ./cmd/fs ls "iap_environments/production/processed_orders" --limit=20
```

### 4.4 롤백

문제가 생기면 `enabled`를 `false`로 되돌리고 재배포한다.
코드를 되돌릴 필요가 없다.

---

## 배포 후 확인

```bash
ADMIN_URL="$(gcloud run services describe platform-admin \
  --region=asia-northeast3 --format='value(status.url)')"
TOKEN="$(gcloud auth print-identity-token --audiences="$ADMIN_URL")"

curl -sS -H "Authorization: Bearer $TOKEN" "$ADMIN_URL/v1/admin/health"
```

`deadLetterCount`가 0이 아니면 마켓에 완료를 알리지 못한 주문이 있다.

워커를 Cloud Run Job으로 걸어둔다. 이게 멈추면 Play 자동 환불이 시작된다.

## 관련

- [BREAK-GLASS](../08-ops/BREAK-GLASS.md) — 백오피스 없이 조작하기
- [마켓 검증 상태](../07-qa/market-verification.md) — 무엇을 어떻게 확인했나
