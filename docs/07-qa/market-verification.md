# 마켓 검증 상태

provider가 실제 마켓 API에 붙는지 확인한 기록.

fake HTTP 테스트는 "우리가 아는 형식"만 검증한다. 실제 마켓이 그 형식으로
답하는지, 자격증명이 통하는지는 붙어봐야 안다. SDK 이벤트가 정확히 그
간극 때문에 전량 유실되고 있었다 — 양쪽이 각자 자기 형식으로 통과했다.

## 실행

```bash
set -a; . ~/.config/seorilabs/app-store-server-api.env; set +a
export APPLE_IAP_BUNDLE_ID=com.seorilabs.lizardtycoon
export GOOGLE_APPLICATION_CREDENTIALS=~/.config/seorilabs/play-store/seorilabs-play-publisher.json
export IAP_PLAY_PACKAGE_NAME=com.seorilabs.lizardtycoon

go test -tags=market ./internal/iap/providers/ -v
```

자격증명이 없는 마켓은 `t.Skip`으로 건너뛴다.

## 2026-07-31 결과

| 마켓 | 인증 | 에러 매핑 | 실제 구매 검증 | shadow 대조 | 웹훅 |
|---|---|---|---|---|---|
| App Store | **통과** | `4000006` | **통과** | **통과** | **통과** |
| Google Play | **통과** | 400 | **통과** | — | **통과** |
| AppsInToss | 미확보 | — | — | — | — |

## Play 실제 구매 검증 — 토큰을 얻는 법

원장에는 `purchaseToken`을 저장하지 않는다(PII 최소화). 하지만
**Play API `purchases.voidedpurchases.list`가 환불된 구매의 토큰을 준다.**

```bash
curl -H "Authorization: Bearer $TOKEN" \
  "https://androidpublisher.googleapis.com/androidpublisher/v3/applications/$PKG/purchases/voidedpurchases?maxResults=5"
```

이 토큰으로 실제 검증을 돌렸다.

```
productId       sp_galaxy_gecko
canonicalId     hlffalpebliemceanhakmcml... (purchaseToken)
providerOrderId GPA.3309-0275-2077-97107
state           revoked
completion      none
purchasedAt     2026-07-19T08:42:09Z
orderKey        73a88841...37f968b8
```

- 불변식 1: canonicalId가 purchaseToken이다
- 환불된 구매를 `revoked`로 판정한다
- 환불된 구매에 완료 처리를 요구하지 않는다 (`completion: none`)

### 환불 알림 전 경로 — 실데이터

같은 토큰으로 RTDN 환불 알림을 Pub/Sub에 넣었다.

| 단계 | 결과 |
|---|---|
| Pub/Sub push + OIDC | 200 |
| 알림 파싱 | `voided_purchase` |
| **실제 Play API 재검증** | `state: revoked` |
| 원장 반영 | `known: false` |
| **tombstone 생성** | `tombstone: true` |

`known: false`가 정확한 동작이다. 이 구매는 lizard-tycoon의 Firebase
원장에 있고 플랫폼 원장에는 없다. **불변식 10에 따라 알림만으로
신규 지급을 하지 않고, 환불이므로 tombstone만 남긴다.** 나중에 그
구매가 검증되면 stale 억제가 재지급을 막는다.

## shadow 대조 — App Store

**가장 중요한 검증이다.** lizard-tycoon이 Firebase Functions(TypeScript)로
운영하며 남긴 실제 샌드박스 구매를 우리 Go provider로 다시 검증했다.
사람이 기기에서 실제로 결제한 거래다.

```
transactionId    2000001213754304
productId        com.seorilabs.lizardtycoon.premium.galaxy_gecko
canonicalId      2000001213754304  (originalTransactionId)
state            active
completion       apple_finish
purchasedAt      2026-07-30T12:09:29Z
orderKey         32de2fba...c5e598a7
```

`orderKey`가 **기존 구현이 Firestore 문서 ID로 쓴 값과 정확히 일치**했다.

이게 왜 결정적인가. orderKey가 다르면 전환 시점에 같은 구매가 두 주문으로
갈라진다. 멱등이 깨지고 이미 지급한 것을 다시 지급한다. 두 구현이
`sha256("{platform}:{canonicalId}")`를 같은 입력으로 계산한다는 것을
실제 데이터로 확인했다.

함께 확인된 것

- 실제 Apple JWS를 파싱하고 인증서 체인을 검증한다
- 불변식 1: canonicalId가 originalTransactionId다
- 불변식 9: `NON_CONSUMABLE`이라 통과했다. 아니면 거부됐을 것이다
- bundleId·환경 대조를 통과한다
- `purchasedAt`이 원장 기록(`2026-07-30T12:09:29.000Z`)과 같다

```bash
export APPLE_REAL_TRANSACTION_ID=2000001213754304
export APPLE_REAL_ORDER_KEY=32de2fba5b5f2bb536dfd396bb90edd8c3db25cb1fce89a2e396410bc5e598a7
go test -tags=market ./internal/iap/providers/ -run TestAppleRealSandboxPurchase -v
```

### Play는 orderKey 대조를 할 수 없다

기존 Play 주문의 `purchaseToken`을 원장에 저장하지 않았기 때문에,
그 주문의 orderKey를 다시 계산할 수 없다. `voidedpurchases`로 얻은
토큰은 다른 구매의 것이다.

다만 orderKey 계산은 두 마켓이 같은 `domain.OrderKey` 함수를 쓰므로
App Store에서 확인된 알고리즘 일치가 Play에도 그대로 적용된다.

### App Store

`.p8` + keyId + issuerId로 App Store Server API에 붙었다. 존재하지 않는
transactionId를 조회해 Apple이 `4000006 Invalid transaction id`를 줬고,
우리가 `purchase_invalid`로 매핑하는 것을 확인했다.

자격증명이 거부되면 `provider_auth_failed`가 온다. 둘이 구분되므로
운영 중에 "키가 틀렸나 거래가 없나"를 판별할 수 있다.

### 미결 — 샌드박스 초기화 뒤 재구매가 앱에서 거부된다

**미해결로 남긴다. 프로덕션 영향은 없다고 판단했다.**

샌드박스 구매 내역을 초기화한 뒤 같은 상품을 다시 사면 이렇게 된다.

```
Apple    구매 시트 없이 옛 트랜잭션을 그대로 복원
서버     200 · {status: "revoked",
                completion: {action: "app_store_sync_after_sandbox_reset"}}
앱       iap_response_invalid 로 거부
```

서버 의도는 "이 거래를 finish했으니 다음엔 진짜 시트로 사라"인데, 앱이
그 응답을 거부해 정리 신호가 전달되지 않는다. 그래서 몇 번을 눌러도
같은 자리를 맴돈다 — 실기기에서 6회 시도했고 서버는 6회 다 200이었다.

**프로덕션에 없는 경로다.** `completion`이 붙은 `revoked` 응답을 만드는
곳은 `blockedBySandboxReset` 한 군데뿐이고, 프로덕션에는 샌드박스
초기화라는 개념이 없다. 환불로 인한 `revoked`는 다른 분기로 나가며
`completion` 없이 반환돼 앱이 정상 처리한다.

**다만 실패 지점은 특정하지 못했다.** 코드상 검증 대상은 전부 유효하다
— `entitlementId`는 `sp_shootingstar_tokay`, `entitlements`는
`listActive(uid)` 결과, `completion.action`은 서버가 보내는 문자열과
정확히 일치한다. 그런데도 거부됐다. 그래서 "샌드박스 전용일 가능성이
높다"까지만 말할 수 있다.

재발하면 진단 로그로 바로 잡힌다. 로깅은 이미 들어가 있다(build 14~).

```bash
# iOS 기기 로그는 root가 필요하다. 별도 터미널에서 실행한다.
sudo log collect --device-udid <udid> --last 30m --output ~/ios-iap.logarchive
log show ~/ios-iap.logarchive --predicate 'eventMessage CONTAINS "[iap]"'
```

Android는 그냥 `adb logcat | grep '\[iap\]'`로 보인다.

### Google Play

publisher SA로 `purchases.productsv2`에 붙었다. 인증이 통했고 가짜
토큰에 400을 받아 `purchase_invalid`로 매핑했다.

> **주의**: `inappproducts` 엔드포인트는 403을 준다. 권한 문제가 아니라
> **deprecated**다 — 응답 본문이 `"Please migrate to the new publishing API"`다.
> 403만 보고 "SA 권한이 없다"고 단정하면 엉뚱한 곳을 고치게 된다.

### AppsInToss

**mTLS 클라이언트 인증서가 없다.** `~/.config/seorilabs/apps-in-toss.env`에
있는 것은 `APPS_IN_TOSS_API_KEY`로 배포용이며, provider가 요구하는
클라이언트 인증서가 아니다.

인증서를 받기 전까지 AIT 검증기는 조립되지 않고, AIT 결제만
`platform_unavailable`로 거부된다. 나머지 두 마켓은 정상 동작한다.

#### mTLS 배선은 검증했다

실제 인증서는 없지만 **자체 서명 CA로 클라이언트 인증을 요구하는
서버를 띄워 실제 TLS 핸드셰이크를 통과시켰다.** 다른 테스트는
평문 서버를 꽂아 이 경로를 타지 않는다.

| 검증 | 결과 |
|---|---|
| 클라이언트 인증서 제시 → 서버가 CN 확인 | **통과** |
| 인증서 없이 연결 → 핸드셰이크 실패 | **통과** |
| 다른 CA 인증서 → 거부 | **통과** |
| 서버 인증서 미검증 → 연결 거부 | **통과** |

마지막 항목이 중요하다. AIT를 사칭하는 서버에 주문 정보와
사용자 키를 보내면 안 된다.

#### API 계약도 공식 문서와 대조했다

배선이 맞아도 주소나 헤더가 틀리면 인증서를 받은 뒤에야 알게 된다.
그때는 되돌릴 시간이 없으므로 미리 맞췄다.

| 항목 | 공식 문서 | 우리 구현 |
|---|---|---|
| base URL | `https://apps-in-toss-api.toss.im` | 같다 |
| 경로 | `/api-partner/v1/apps-in-toss/order/get-order-status` | 같다 |
| 메서드 | POST | 같다 |
| 요청 본문 | `{"orderId": "..."}` | 같다 |
| 사용자 식별 | `x-toss-user-key` | 같다 |
| 인증 | mTLS 클라이언트 인증서 (CN으로 미니앱 식별) | 같다 |

호스트가 둘이라 헷갈리는 자리다 — `pay-apps-in-toss-api.toss.im`도
있지만 **인앱결제 주문 조회는 `apps-in-toss-api` 쪽이다.**

같이 확인한 운영 정보:

- 요청 한도 **분당 3,000회**. 초과하면 일정 시간 차단된다
- 인증서를 **2개 이상 등록**할 수 있다. 만료 전 무중단 교체용이다
- 비로그인 사용자는 `x-anon-key`를 쓴다. 우리는 익명 신원의 결제를
  금지하므로(사칭이 가능하다) 이 경로를 쓰지 않는다

**인증서를 받는 즉시 동작한다는 것까지 확인된 상태다.**

## Apple 웹훅 E2E — 실제 알림으로 검증

Apple의 `SendRequestTestNotification`을 호출하면 Apple이 실제 ASSN v2
알림을 보내고, `GetTestNotificationStatus`가 **Apple이 서명한
signedPayload를 그대로 돌려준다.** 그것을 우리 웹훅에 직접 보냈다.

실결제 없이 웹훅 배선 전체를 검증할 수 있는 경로다.

```
notificationType   TEST
bundleId           com.seorilabs.lizardtycoon
environment        Sandbox
notificationUUID   045a6246-8788-404e-bd82-273c6ed1b37f
```

| 검증 | 결과 |
|---|---|
| Apple 서명 JWS 검증 (인증서 체인) | **통과** |
| bundleId 대조 | **통과** |
| 웹훅 응답 | HTTP 200 |
| 이벤트 원장 기록 | `status: completed`, `attemptCount: 1` |
| 같은 알림 3회 전송 | 전부 200, `attemptCount`는 1 유지 |

멱등성이 실제로 동작한다. 같은 알림이 두 번 와도 한 번만 처리하고,
Apple에는 200을 줘서 재전송을 멈춘다.

`TEST` 타입은 우리가 반응하는 알림이 아니다. 점유만 하고 완료 처리하는
것이 정확한 동작이고, 실패로 만들면 Apple이 계속 재전송한다.

### 재현

```bash
export APPLE_SEND_TEST_NOTIFICATION=1
go test -tags=market ./internal/iap/providers/ -run TestAppleTestNotification -v

# 출력의 signedPayload를 웹훅에 보낸다
curl -X POST -H 'Content-Type: application/json' \
  -d "{\"signedPayload\":\"$PAYLOAD\"}" \
  "$IAP_URL/v1/iap/webhooks/apple"
```

> **주의**: 알림 URL은 App Store Connect에 등록된 것으로 간다. 현재는
> lizard-tycoon의 Firebase Functions다. 위 절차는 Apple이 준 payload를
> 우리가 직접 우리 웹훅에 넣는 방식이라 URL 등록과 무관하게 동작한다.
> 실제 전환 시에는 App Store Connect에서 URL을 바꿔야 한다.

## Play RTDN E2E — Pub/Sub push로 검증

Play는 Apple과 달리 테스트 알림을 API로 요청할 수 없다. 대신 **RTDN이
Pub/Sub push이므로 우리가 직접 topic에 publish하면 그 뒤 경로가 전부
실제로 동작한다** — Pub/Sub이 OIDC 토큰을 붙여 우리 웹훅에 밀어준다.

```bash
gcloud pubsub topics create play-iap-rtdn --project=seorilabs-platform
gcloud pubsub subscriptions create play-iap-rtdn-push \
  --topic=play-iap-rtdn \
  --push-endpoint="$IAP_URL/v1/iap/webhooks/play" \
  --push-auth-service-account="pubsub-rtdn-pusher@..." \
  --push-auth-token-audience="$IAP_URL"

gcloud pubsub topics publish play-iap-rtdn --message="$(cat notification.json)"
```

| 검증 | 결과 |
|---|---|
| Pub/Sub push 도착 | **통과** |
| OIDC 토큰 검증 | **통과** (인증 없이는 401) |
| 발신 SA 허용 목록 | **통과** |
| 알림 파싱 | **통과** |
| 이벤트 원장 기록 (`messageId` 키) | `status: completed` |

### 여기서 실제 결함을 찾았다 — Pub/Sub 재전송 시맨틱

**Pub/Sub은 2xx가 아니면 무조건 재전송한다.** Apple은 4xx를 받으면
멈추지만 Pub/Sub은 그렇지 않다. 4xx로 "이 메시지는 버려라"를
표현할 수 없다.

부분 환불 알림에 422를 줬더니 **같은 메시지가 무한 재전송됐다.**
로그에 422가 계속 쌓였다.

고친 것

1. Play 웹훅은 재시도 가치가 없는 실패에 **200**을 준다. 버리기 전에
   로그를 남긴다 — 조용히 삼키면 왜 반영되지 않았는지 알 수 없다
2. `provider_auth_failed`는 웹훅에서 **재시도 가능**으로 본다.
   검증 경로에서는 사용자를 기다리게 할 이유가 없어 재시도 불가가
   맞지만, 알림 경로에서는 운영자가 설정을 고치면 처리할 수 있다.
   완료로 남기면 그동안 온 환불 알림을 전부 잃는다

수정 후 재확인: 부분 환불 알림에 200 + `partial_refund_unsupported`
로그, 재전송 1회로 종료.

이것이 계획서가 "Pub/Sub retry 시맨틱 재설계 필요"로 남겨둔 항목이다.
실제로 붙여보고서야 정확한 규칙을 알았다.

## 완료 처리와 워커 — 실서버 E2E

### finishTransaction

이미 완료된 거래에 다시 호출해 경로를 확인했다. Apple이 멱등하게
받아준다.

```
finishTransaction 성공 — 완료 처리 경로가 동작한다
```

이 경로가 막히면 지급은 했는데 마켓은 모르는 상태가 된다.
Play는 3일 뒤 자동 환불하고, Apple도 거래를 미완료로 본다.

### 재시도 워커

실제 Firestore(`stg_` + sandbox)와 실제 Apple API로 전 구간을 돌렸다.

```
대기열에 적재: 32de2fba...c5e598a7
워커 결과: claimed=1 completed=1 failed=0
대기열에서 제거됨
```

| 검증 | 결과 |
|---|---|
| `(platform, status, nextAttemptAt)` 복합 인덱스로 claim | **통과** |
| lease 점유 | **통과** |
| 실제 마켓 완료 호출 | **통과** |
| 성공 시 대기열에서 삭제 | **통과** |

원장에서 문서를 지우는 유일한 경우다. 불변식 5의 예외이고,
실제로 지워지는 것을 확인했다.

```bash
go test -tags=market ./internal/iap/worker/ -v
```

### Play acknowledge 경로

환불된 구매에 호출해 경로를 확인했다. Play가 거래를 거절하지만
자격증명 오류와 구분되는 응답이 오므로 요청이 Play에 닿는다.

```
acknowledge 응답: purchase_invalid
Play가 거래를 거절했다 — 인증은 통했고 경로는 살아 있다
```

완전한 검증은 완료하지 않은 **활성** 구매가 필요하다.

## Play API에 없는 것 — 확인된 사실

추측이 아니라 API discovery로 확인했다.

```bash
curl "https://androidpublisher.googleapis.com/\$discovery/rest?version=v3"
```

| 필요한 것 | API |
|---|---|
| orderId로 purchaseToken 조회 | **없다.** `orders.refund`만 있고 `orders.get`이 없다 |
| 활성 구매 목록 | **없다.** `voidedpurchases.list`는 환불된 것만 준다 |
| RTDN topic 등록 | **없다.** Play Console UI 전용 |

그래서 아래 둘은 콘솔이나 기기가 있어야 한다.

## 아직 못 한 것 — 신규 실결제

기존 구매 재검증으로 상당 부분이 덮였지만, 새 결제를 해야만
확인되는 것이 남는다.

- **Play acknowledge 완전 검증** — 호출 경로는 확인했지만 실제로
  반영되는지는 완료하지 않은 활성 구매가 필요하다. 그 토큰을 얻는
  API 경로가 없다(위 표 참고)
- **`originalTransactionId`가 복원 시에도 유지되는지** — 재구매·복원 흐름
- **RTDN 실연동** — 현재 검증은 우리가 Pub/Sub에 직접 넣은 것이다.
  Play Console에서 이 topic을 RTDN 대상으로 등록해야 Google이 직접 보낸다.
  `androidpublisher` API에 해당 설정 경로가 없어 콘솔 UI로만 가능하다
- **AppsInToss 전 경로** — mTLS 인증서부터 필요하다

### 절차

1. lizard-tycoon을 TestFlight(iOS) / 내부 테스트(Android)로 올린다
2. 샌드박스 계정으로 `sp_galaxy_gecko`를 구매한다
3. `platform-iap`의 `/v1/iap/verify`에 그 토큰을 보낸다
4. 원장을 확인한다
   ```bash
   go run ./cmd/fs get "processed_orders/<orderKey>"
   ```
5. 마켓 콘솔에서 환불하고 웹훅이 도착하는지 본다

**이 절차를 통과하기 전에 lizard-tycoon 전환 스위치를 켜지 않는다.**

## 실기기에서만 드러난 것 — 본문 없는 POST와 411

앱을 Go 서버로 전환한 첫 실기기 구매가 **첫 호출에서 막혔다.**

```
[iap] purchase stage=account_references_start platform=google_play
ERROR: [iap] iap_response_invalid | 응답을 해석하지 못했어요
```

서버 로그에는 **아무것도 없었다.** Cloud Run 요청 로그에도 없었다.
세션은 200으로 잘 열린 뒤였다.

원인은 Godot의 `HTTPRequest`가 **본문이 빈 문자열이면 `Content-Length`를
붙이지 않는 것**이었다. Google 프론트엔드가 그 POST를 411 Length Required로
거부하고 HTML을 돌려준다. 컨테이너까지 오지 않으니 서버는 멀쩡하고 앱만
실패한다.

`/v1/iap/account-references`가 본문 없는 유일한 POST였고, 하필 구매 흐름의
첫 호출이다.

### 왜 배포 검증을 통과했나

**curl로는 재현되지 않는다.** curl이 `Content-Length: 0`을 자동으로 붙인다.

```bash
# 통과한다 — curl이 Content-Length를 붙여 준다
curl -s -X POST ".../v1/iap/account-references" -H "Authorization: Bearer $TOK"
```

배포 후 curl 스모크가 전부 200이었는데 실기기만 깨졌다. 클라이언트 HTTP
스택으로 실제로 쏘아 보기 전에는 알 수 없는 종류다.

### 남긴 것

`lizard-tycoon/tools/empty_post_body_probe.gd`가 네트워크를 실제로 타고
확인한다. 인증 없이 호출해 **우리 서버의 JSON 401**이 오는지 본다. 411이면
프론트엔드가 HTML로 막은 것이라 실패한다. 오프라인이면 SKIP.

fake transport로는 잡히지 않는다. 요청이 프론트엔드에서 막히는 것이라
서버까지 갔는지 자체가 검증 대상이다.

### 일반화

> 클라이언트 HTTP 스택이 curl과 같게 동작한다고 가정하지 않는다.
> 특히 **본문 없는 POST**, 리다이렉트, 압축, keep-alive는 스택마다 다르다.
> 서버 로그가 비어 있는데 클라이언트가 응답을 받았다면, 답한 것은
> 우리 서버가 아니라 그 앞의 무언가다.

## 가짜가 원본과 갈라지면 테스트는 아무것도 지키지 못한다

복원이 실기기에서 매달렸다. 화면은 "구매 내역을 확인하고 있어요"에서
멈추고 `operation_timeout`이 났다. 원인은 SDK 호출 시그니처였다.

```gdscript
func verify_purchase(proof: Dictionary, callback: Callable) -> void:      # SDK 정의
_platform_client.verify_purchase(platform, product_id, token, callback)   # 호출부
```

GDScript는 이 호출을 런타임에 실패시키고 콜백을 돌려주지 않는다.
컴파일 시점에 잡히지 않는다.

### 왜 probe가 놓쳤나

**가짜 SDK가 잘못된 쪽을 흉내내도록 만들어져 있었다.** 호출부가 인자
넷을 쓰니 가짜도 넷을 받게 했고, 그래서 36건이 전부 통과했다.
잘못된 코드를 잘못된 테스트가 승인한 것이다.

`account_references`와 `list_entitlements`는 인자가 없어 우연히 맞았다.
그래서 구매 흐름의 앞부분만 동작했고 정확히 검증에서만 죽었다 — 어느
한 부분만 조용히 실패하는 형태라 원인을 좁히는 데 오래 걸렸다.

### 두 겹으로 막는다

**시그니처 대조.** `get_method_list()`로 가짜와 실제 SDK의 인자 수를
비교한다. 갈라지는 순간 CI에서 걸린다.

```gdscript
func _arg_count(obj: Object, method_name: String) -> int:
	for method in obj.get_method_list():
		if String(method.get("name", "")) == method_name:
			return (method.get("args", []) as Array).size()
	return -1
```

**가짜 없이 한 번 돌려본다.** `real_verify_chain_probe.gd`가 진짜
`PlatformClient`와 진짜 `PlatformIapClient`를 붙여 실제 서버까지 보낸다.
증빙이 가짜라 서버가 거부하는 것이 정상이고, 보는 것은 **콜백이
유실되지 않는다**는 사실이다.

### 일반화

> 가짜는 원본의 계약을 흉내내야지, 호출부의 가정을 흉내내면 안 된다.
> 가짜를 만들 때 참조할 것은 내가 쓴 호출 코드가 아니라 원본의 정의다.
>
> 그리고 연결부는 가짜 없이 한 번은 실제로 돌려봐야 한다. 단위
> probe가 아무리 많아도 그 하나를 대신하지 못한다.

이 결함 하나 때문에 실기기 빌드를 세 번 더 올렸다. 로컬에서 실제 SDK로
한 번만 돌려봤으면 첫 빌드 전에 끝났다.

## 관련

- provider 구현: `server/internal/iap/providers/`
- fake HTTP 테스트: 같은 디렉토리의 `*_test.go`
- 불변식: `docs/03-architecture/iap.md`
