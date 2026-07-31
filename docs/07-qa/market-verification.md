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

| 마켓 | 인증 | 에러 매핑 | 실결제 |
|---|---|---|---|
| App Store | **통과** | `4000006` → `purchase_invalid` 확인 | 미실시 |
| Google Play | **통과** | 400 → `purchase_invalid` 확인 | 미실시 |
| AppsInToss | **미확보** | — | — |

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

## 아직 못 한 것 — 실결제

**샌드박스 실결제는 사람이 기기에서 해야 한다.** API 검증으로는
대체할 수 없는 것이 남는다.

- 실제 구매 토큰의 형식과 응답 스키마
- `originalTransactionId`가 복원 시에도 유지되는지
- acknowledge / finishTransaction이 실제로 반영되는지
- 환불 웹훅이 실제로 도착하는지

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
