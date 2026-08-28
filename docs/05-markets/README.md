# Markets

3마켓 결제 연동 원장. 자격증명과 설정의 **확정 여부**를 여기서 추적한다.

## 마켓별 요약

| | Google Play | App Store | AppsInToss |
|---|---|---|---|
| 검증 API | 비소모성 `purchases/productsv2`<br/>소모성 `purchases/products` | `getTransactionInfo` | `order/get-order-status` |
| 완료 API | 비소모성 `:acknowledge`<br/>소모성 `:consume` | `finishTransaction` | 클라이언트가 `completeProductGrant` |
| **자격증명** | **ADC** — 런타임 SA + Play Console 권한. **JSON 키 없음** | Secret — issuer ID, key ID, `.p8` + Apple Root CA | **mTLS** 클라이언트 인증서 + 키 |
| canonicalId | `purchaseToken` | 비소모성 `originalTransactionId`<br/>소모성 `transactionId` | `orderId` |
| 계정 바인딩 | `obfuscatedExternalAccountId` — HMAC | `appAccountToken` — HMAC를 UUID 형태로 | **면제** |
| 웹훅 | RTDN Pub/Sub topic `play-iap-rtdn` | ASSN v2 HTTPS + JWS | **없음** |
| 소비/비소비 | 카탈로그 유형별 검증·완료 | 카탈로그 유형과 서명 거래 유형 대조 | 카탈로그 유형은 지급 단위에 사용 |

## 확정 상태

| 항목 | 상태 |
|---|---|
| Play 런타임 SA + Console 권한 | `확정 필요` — P5 전 |
| Apple issuer ID / key ID / `.p8` | `확정 필요` — 전용 In-App Purchase 키 |
| Apple Root CA base64 | `확정 필요` |
| **AIT mTLS 클라이언트 인증서·키** | **`확정 필요` — 미확보. 없으면 AIT provider는 스텁** |
| **AIT 상품 ID** | **`확정 필요`** |
| **AIT `aitUserKey` claim 발급 경로** | **`확정 필요` — lizard-tycoon에서도 미구현** |
| RTDN topic 이름 | `play-iap-rtdn` 고정. "goog" prefix 금지라 이 이름이어야 한다 |

## 주의

- **Play는 SA JSON 키를 배포하지 않는다.** 런타임 SA의 ADC를 쓴다. 이 원칙을 깨지 않는다.
- **production과 sandbox 자동 fallback을 하지 않는다.** Apple 환경 설정과 원장 환경 설정이 불일치하면 부팅을 실패시킨다.
- 마켓 자격증명은 **`platform-iap` 서비스에만** 마운트한다. → R3

## AIT 웹훅 부재

AppsInToss는 서버 웹훅 공개 계약이 확인되지 않아 엔드포인트를 만들지 않는다. 클라이언트 pending 복구와 주문상태 재조회로만 정합성을 맞춘다. 이는 lizard-tycoon의 현재 상태를 그대로 승계한 것이다.
