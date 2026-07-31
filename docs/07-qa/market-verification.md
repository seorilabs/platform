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

| 마켓 | 인증 | 에러 매핑 | 실제 구매 검증 | shadow 대조 |
|---|---|---|---|---|
| App Store | **통과** | `4000006` → `purchase_invalid` | **통과** | **통과** |
| Google Play | **통과** | 400 → `purchase_invalid` | 불가 (아래) | — |
| AppsInToss | 미확보 | — | — | — |

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

### Play는 같은 방법을 쓸 수 없다

purchaseToken을 원장에 저장하지 않기 때문이다. **PII 최소화 원칙이고
우리도 같은 규칙을 따른다.** orderKey는 sha256이라 역산도 안 된다.

원장에 남은 것은 `providerOrderId`(`GPA.3370-...`)뿐이고, Play API는
orderId로 purchaseToken을 되찾는 경로를 주지 않는다.

Play 재검증은 기기에서 새 구매를 만들어야 한다. 다만 orderKey 계산은
두 마켓이 같은 `domain.OrderKey` 함수를 쓰므로 App Store에서 확인된
알고리즘 일치가 Play에도 그대로 적용된다.

### App Store

`.p8` + keyId + issuerId로 App Store Server API에 붙었다. 존재하지 않는
transactionId를 조회해 Apple이 `4000006 Invalid transaction id`를 줬고,
우리가 `purchase_invalid`로 매핑하는 것을 확인했다.

자격증명이 거부되면 `provider_auth_failed`가 온다. 둘이 구분되므로
운영 중에 "키가 틀렸나 거래가 없나"를 판별할 수 있다.

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

## 아직 못 한 것 — 신규 실결제

기존 구매 재검증으로 상당 부분이 덮였지만, 새 결제를 해야만
확인되는 것이 남는다.

- **Play 구매 검증** — purchaseToken이 원장에 없어 기존 거래로는 못 한다
- **acknowledge / finishTransaction이 실제로 반영되는지** — 기존 거래는
  이미 완료 처리되어 있어 다시 부를 수 없다
- **환불 웹훅이 실제로 도착하는지** — Apple ASSN v2, Play RTDN 배선
- **`originalTransactionId`가 복원 시에도 유지되는지** — 재구매·복원 흐름
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

## 관련

- provider 구현: `server/internal/iap/providers/`
- fake HTTP 테스트: 같은 디렉토리의 `*_test.go`
- 불변식: `docs/03-architecture/iap.md`
