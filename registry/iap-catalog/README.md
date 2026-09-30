# Lizard Tycoon IAP 카탈로그 후보

`lizard-tycoon.json`은 기존 영구 상품 10종과 크리스털 5종의 서버 SKU·유형을 함께 검증하는 앱별 후보 원장이다. 크리스털 충전 4종은 `consumable`, 스타터는 `non_consumable`이다. 다른 앱의 상품 항목은 이 파일에 없다.

운영 서버는 Secret Manager의 `iap-catalog`를 `IAP_CATALOG_JSON`으로 읽는다. 이 파일을 비밀값 전체에 그대로 덮어쓰면 다른 앱 상품이 사라질 수 있다. 현재 비밀값의 앱 목록과 버전을 권한 있는 경로에서 읽고, 이 앱의 15종만 병합한 뒤 새 버전을 검증해야 한다. 읽기 권한이 없거나 기존 형식을 확인할 수 없으면 동기화를 중단한다.

권한 있는 운영 경로에서 현재 비밀값을 **출력하지 않고** 권한 `0600`의 로컬 임시 파일에 받은 뒤, 아래 도구로 새 파일을 만든다. 이 도구는 기존 도마뱀 상품의 매핑이 달라졌거나 카탈로그 형식이 예상과 다르면 중단한다. 다른 앱의 항목은 그대로 둔다. 출력 파일이 이미 있으면 덮어쓰지 않는다.

```sh
node scripts/merge-lizard-iap-catalog.mjs --current /secure/current-iap-catalog.json --output /secure/merged-iap-catalog.json
```

두 파일을 Git에 넣지 않는다. 병합된 파일을 새 Secret Manager 버전으로 등록하는 일은 서버 코드 배포와 사전 조건을 확인한 뒤 수행한다. 등록 후 새 비밀 버전의 상품 ID·유형·앱별 개수와 실제 서버 지급 경로를 읽어 검증한다.
