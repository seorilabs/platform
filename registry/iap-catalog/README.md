# Lizard Tycoon IAP 카탈로그 후보

`lizard-tycoon.json`은 기존 영구 상품 10종과 크리스털 5종의 서버 SKU·유형을 함께 검증하는 앱별 후보 원장이다. 크리스털 충전 4종은 `consumable`, 스타터는 `non_consumable`이다. 다른 앱의 상품 항목은 이 파일에 없다.

운영 서버는 Secret Manager의 `iap-catalog`를 `IAP_CATALOG_JSON`으로 읽는다. 이 파일을 비밀값 전체에 그대로 덮어쓰면 다른 앱 상품이 사라질 수 있다. 현재 비밀값의 앱 목록과 버전을 권한 있는 경로에서 읽고, 이 앱의 15종만 병합한 뒤 새 버전을 검증해야 한다. 읽기 권한이 없거나 기존 형식을 확인할 수 없으면 동기화를 중단한다.

경제 설정은 일반 판매 `enabled=false`, `launch_at=0`으로 유지한다. 시험 판매 `test_enabled=true`는 관리 API에서 등록한 **실제 연결 계정**에만 적용되며, Play는 검증된 `testPurchase` 주문만 시험 지갑에 지급한다. 서버와 백오피스 배포, 계정 연결 서명 계정, Android OAuth, App Check 등록과 실기기 검증 전에는 registry 동기화나 일반 판매 전환을 완료 상태로 기록하지 않는다.
