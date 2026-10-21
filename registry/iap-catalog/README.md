# IAP 카탈로그 후보

앱별 후보 원장이다. 운영 비밀값 `iap-catalog`에 병합하는 도구는 앱마다 다르다.

| 파일 | 내용 | 병합 도구 |
| --- | --- | --- |
| `lizard-tycoon.json` | 영구 상품 10종·크리스털 5종 | `scripts/merge-lizard-iap-catalog.mjs` |
| `bloomhand.json` | 친구 상자 1개·5개(소모품, ADR 0029 상자 경제가 개봉 단위로 차감) | `scripts/merge-new-app-iap-catalog.mjs --app bloomhand` |
| `merge-lizard.json` | Amber Lizards 15종. 기존 호박 묶음 5종(상점에서 숨기고 처리 중 구매만 지급)·새 호박 묶음 5종·스타터·30일 호박은 소모품, 햇살 축복·둥지 확장·호박 오아시스는 비소모품 | `scripts/merge-new-app-iap-catalog.mjs --app merge-lizard` |

## Lizard Tycoon

`lizard-tycoon.json`은 기존 영구 상품 10종과 크리스털 5종의 서버 SKU·유형을 함께 검증하는 앱별 후보 원장이다. 크리스털 충전 4종은 `consumable`, 스타터는 `non_consumable`이다. 다른 앱의 상품 항목은 이 파일에 없다.

운영 서버는 Secret Manager의 `iap-catalog`를 `IAP_CATALOG_JSON`으로 읽는다. 이 파일을 비밀값 전체에 그대로 덮어쓰면 다른 앱 상품이 사라질 수 있다. 현재 비밀값의 앱 목록과 버전을 권한 있는 경로에서 읽고, 이 앱의 15종만 병합한 뒤 새 버전을 검증해야 한다. 읽기 권한이 없거나 기존 형식을 확인할 수 없으면 동기화를 중단한다.

권한 있는 운영 경로에서 현재 비밀값을 **출력하지 않고** 권한 `0600`의 로컬 임시 파일에 받은 뒤, 아래 도구로 새 파일을 만든다. 이 도구는 기존 도마뱀 상품의 매핑이 달라졌거나 카탈로그 형식이 예상과 다르면 중단한다. 다른 앱의 항목은 그대로 둔다. 출력 파일이 이미 있으면 덮어쓰지 않는다.

```sh
node scripts/merge-lizard-iap-catalog.mjs --current /secure/current-iap-catalog.json --output /secure/merged-iap-catalog.json
```

두 파일을 Git에 넣지 않는다. 병합된 파일을 새 Secret Manager 버전으로 등록하는 일은 서버 코드 배포와 사전 조건을 확인한 뒤 수행한다. 등록 후 새 비밀 버전의 상품 ID·유형·앱별 개수와 실제 서버 지급 경로를 읽어 검증한다.

## 새 앱 추가

`scripts/merge-new-app-iap-catalog.mjs`는 운영 비밀값에 없는 앱을 더하거나, 이미 있으면 후보와 완전히 같을 때만 통과한다. 다른 앱 항목이 바뀌면 중단하고, 출력 파일이 있으면 덮어쓰지 않는다. 위와 같은 방식으로 현재 비밀값을 출력하지 않고 `0600` 임시 파일에 받아 실행한다.

```sh
node scripts/merge-new-app-iap-catalog.mjs --app bloomhand --current /secure/current-iap-catalog.json --output /secure/merged-iap-catalog.json
```

### 순서: 비밀값, regsync, 배포

`platform-iap`과 `platform-worker`는 부팅할 때 Firestore registry의 IAP 활성 앱마다
`iap.entitlement_ids`가 `IAP_CATALOG_JSON`에 모두 있는지 확인한다(`cmd/platform/iap.go`의
`validateAppCatalog`). 하나라도 없으면 부팅이 실패하고 모든 앱의 결제가 함께 멈춘다.
반대로 registry에만 있고 서비스가 재시작하지 않으면 그 앱의 검증기가 조립되지 않는다.

1. 이 디렉터리의 후보를 운영 `iap-catalog` 새 버전으로 병합하고 readback한다
2. 그 뒤에 해당 앱 registry를 `regsync`한다
3. main 병합으로 시작된 Deploy를 승인해 새 revision이 두 값을 함께 읽게 한다

1보다 2를 먼저 하면 그 사이 cold start하는 인스턴스가 부팅에 실패한다.
