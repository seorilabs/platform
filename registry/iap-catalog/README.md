# IAP 카탈로그 후보

앱별 후보 원장이다. 운영 비밀값 `iap-catalog`에 병합하는 도구는 앱마다 다르다.

| 파일 | 내용 | 병합 도구 |
| --- | --- | --- |
| `lizard-tycoon.json` | 영구 상품 10종·크리스털 5종 | `scripts/merge-lizard-iap-catalog.mjs` |
| `bloomhand.json` | 친구 상자 1개·5개(소모품, ADR 0029 상자 경제가 개봉 단위로 차감) | `scripts/merge-new-app-iap-catalog.mjs --app bloomhand` |
| `merge-lizard.json` | Amber Lizards 15종. 기존 호박 묶음 5종(상점에서 숨기고 처리 중 구매만 지급)·새 호박 묶음 5종·스타터·30일 호박은 소모품, 햇살 축복·둥지 확장·호박 오아시스는 비소모품. 30일 호박은 기간 이용권 일정(`pass`)을 가진다 | `scripts/merge-new-app-iap-catalog.mjs --app merge-lizard` (일정만 더할 때 `--update-pass`) |

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

## 기간 이용권 일정

소모품 항목에 `pass`를 두면 구매 한 건이 "즉시분 + N일 동안 하루 한 번 받는 몫"이 된다
(ADR 0036, `GET /v1/iap/passes`, `POST /v1/iap/passes/claim`). `pass`가 없는 항목과 다른
앱 항목은 전과 똑같이 해석한다. 비소모품에는 둘 수 없다.

```json
"amber_pass_30d": {
  "type": "consumable",
  "google_play": "merge_lizard_amber_pass_30d",
  "app_store": "merge_lizard_amber_pass_30d",
  "pass": {"immediate_amount": 300, "daily_amount": 50, "days": 30, "missed_days": "forfeit"}
}
```

| 필드 | 뜻 |
| --- | --- |
| `immediate_amount` | 구매 검증(`granted=true`) 때 앱이 바로 주는 양. 서버는 상태 조회로 안내만 한다 |
| `daily_amount` | 하루 몫 |
| `days` | 구매 한 건이 주는 날 수. 하루는 서버가 구매를 처음 활성으로 확정한 시각부터 24시간 |
| `missed_days` | `forfeit`는 그날 안에 받지 않은 몫이 사라진다. `catch_up`은 지난 몫을 다음 수령 때 한 번에 준다 |

일정은 이미 판 이용권에도 바로 적용된다. 판매 중인 일정의 `immediate_amount`,
`daily_amount`, `days`는 바꾸지 않고 새 SKU로 낸다. `missed_days` 전환만 기존 이용권에
적용하는 것을 전제로 한다. `--update-pass`는 일정 추가와 `missed_days` 전환만 통과시키고
그 밖의 변경과 일정 삭제는 거부한다.

### 순서: 서버 배포, 비밀값, 새 revision

**`pass` 형식은 이 기능 이전 서버가 읽지 못한다.** 카탈로그 해석이 모르는 필드를
거부하므로(`DisallowUnknownFields`) 이전 이미지는 `json: unknown field "pass"`로 부팅에
실패한다. 운영 비밀값은 `iap-catalog:latest`로 붙어 있어 새 인스턴스와 5분마다 도는
`platform-worker`가 시작할 때 최신 버전을 읽는다. 그래서 위의 "비밀값, regsync, 배포"와
순서가 다르다.

1. `pass`를 해석하는 서버를 main 병합과 Deploy 승인으로 모든 role(`platform-api`,
   `platform-iap`, `platform-admin`, `platform-ads`, `platform-ingest`, `platform-worker`)에
   배포하고, 모든 대상이 같은 이미지인지 확인한다
2. 현재 비밀값을 출력하지 않고 `0600` 임시 파일로 받아 병합한다. 운영 비밀값에
   merge-lizard가 없으면 `--app merge-lizard`, 이미 일정 없이 있으면
   `--app merge-lizard --update-pass`다. 새 Secret Manager 버전을 만들고 앱별 상품 수와
   `pass` 필드만 readback한다
3. merge-lizard가 레지스트리에 아직 없으면 그 뒤에 `regsync`한다
4. `platform-iap`과 `platform-admin`에 같은 이미지로 새 revision을 만들어 모든 인스턴스가
   새 버전을 읽게 한다. worker는 다음 실행부터 읽는다
5. 이용권 API까지 배포된 서버에서 결제 세션으로 `GET /v1/iap/passes`가 `amber_pass_30d`를
   돌려주는지 확인한 뒤에 앱의 판매 스위치(`amber_pass_enabled`)를 켠다

2와 4 사이에는 옛 비밀값으로 뜬 인스턴스가 `product_not_allowed`를 줄 수 있다. 구매
검증은 일정과 무관하게 같은 소모품 경로라 지급은 어긋나지 않는다. **1 이후 이미지를
이 기능 이전으로 되돌리려면 먼저 `pass` 없는 비밀값 버전을 최신으로 만든다.** 그렇지
않으면 되돌린 revision이 부팅하지 못하고 모든 앱 결제가 멈춘다.
