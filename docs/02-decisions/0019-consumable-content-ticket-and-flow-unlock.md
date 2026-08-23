# ADR 0019: 콘텐츠 열람권은 소모성 상품으로 검증하고 연간 흐름을 한 번에 해금한다

- 상태: Accepted
- 날짜: 2026-08-23

## 맥락

운글의 유료 단위는 영구 기능 소유가 아니라 특정 명식의 특정 연도 심화 해설 1회 열람이다.
같은 열람권을 반복 구매할 수 있어야 하며, 세운과 12개월 월운을 따로 결제하게 만들면 한 화면의
연속된 흐름을 인위적으로 나누게 된다. 별도 2026 패스는 한 번 열람·캡처한 뒤 반복 가치가 낮고,
열람권과 겹치는 세 번째 권한 모델을 만든다.

기존 Platform IAP는 모든 카탈로그 상품을 비소모성으로 해석했다. Google Play는 구매 후
`acknowledge`, App Store는 `NON_CONSUMABLE`과 `originalTransactionId`만 허용했다. 이 계약을
그대로 쓰면 반복 구매가 불가능하거나, App Store 소모성 재구매가 같은 주문으로 합쳐질 수 있다.

## 결정

1. IAP 카탈로그 항목에 서버 신뢰 필드 `type`을 둔다. 지원 값은 `non_consumable`과
   `consumable`이다. 기존 카탈로그의 빈 값은 `non_consumable`로 해석해 기존 앱의 동작을
   유지한다. 클라이언트는 상품 유형을 결정할 수 없다.
2. Google Play 비소모성은 `purchases.productsv2`로 검증하고 `acknowledge`한다. 소모성은
   상품 ID가 포함된 `purchases.products.get`으로 검증해 미소비 구매만 `consume`한다.
   구매 토큰은 두 유형 모두 주문 멱등키의 canonical ID다. 마켓의 다중 수량 구매는 현재
   원장의 source당 단위 모델과 다르므로 1개를 초과하면 지급하지 않는다.
3. App Store는 카탈로그 유형과 서명된 거래의 `type`이 일치해야 한다. 비소모성 canonical ID는
   복원에도 유지되는 `originalTransactionId`, 소모성 canonical ID는 재구매마다 달라지는
   `transactionId`다. 구독 유형은 계속 거부한다. production과 sandbox 자동 fallback 금지는
   유지한다.
4. 지급을 원장에 먼저 커밋하고 마켓 완료 처리를 수행한다. `consume`, `acknowledge`,
   `finishTransaction` 실패는 지급을 롤백하지 않고 기존 completion outbox에서 재시도한다.
5. 운글 심화 권한의 deep key는 `flow:{year}`다. 같은 `readingKey`에서 광고 보상 1회 또는
   열람권 1단위를 사용하면 해당 연도의 세운과 12개월 월운을 함께 연다.
6. 운글에는 연도별 시즌 패스를 등록하지 않는다. Platform의 범용 시즌 entitlement 기능은
   다른 앱과의 호환을 위해 유지하지만 운글 레지스트리에서는 사용하지 않는다.
7. 실제 마켓 SKU, 광고 unit, 런타임 자격증명, 계정 연결과 실기기 검증이 준비되기 전에는
   운글 레지스트리의 IAP·광고 기능을 활성화하지 않는다.

## 결과

- 기존 비소모성 상품은 카탈로그를 수정하지 않아도 같은 방식으로 검증·완료된다.
- 소모성 구매 한 건은 별도 source가 되어 열람권 단위 차감과 재구매를 지원한다.
- 한 번의 해금으로 세운과 월운이 함께 반환되며, 두 섹션을 따로 차감하지 않는다.
- 코드 merge는 상품 등록, 자격증명 설정, 배포, 실기기 결제 성공을 의미하지 않는다.

ADR 0018의 심화 권한 결정 중 운글의 `deepKey` 단위와 시즌 패스 사용 여부는 이 결정으로
구체화한다.
