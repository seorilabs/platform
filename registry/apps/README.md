# App Registry

**이 디렉토리가 앱 레지스트리의 source of truth다.** 콘솔이나 Firestore를 직접 수정하지 않는다.

CI는 스키마를 검증만 한다. Firestore로 올리는 것은 `cmd/regsync`를 사람이 돌리는 별도 단계다. 런타임은 Firestore를 인메모리 캐시(TTL 60초)로 조회한다.

```bash
cd server
go run ./cmd/regsync --dir=../registry/apps --project=seorilabs-platform --dry-run
go run ./cmd/regsync --dir=../registry/apps --project=seorilabs-platform
```

**파일을 고치는 것만으로는 아무 일도 일어나지 않는다.** 실제로 이 함정을 밟았다 — `features.iap`가 `false`인 채 배포되어 결제는 되는데 백오피스 IAP 관리만 403이었다. 검증 경로가 admin에만 있어 증상이 한쪽에서만 났다.

## 형식

```jsonc
// lizard-tycoon.json
{
  "app_id": "lizard-tycoon",
  "display_name": "도마뱀 테라리움",
  "firebase_project_id": "lizard-tycoon",
  "firebase_custom_token_service_account": "platform-auth@lizard-tycoon.iam.gserviceaccount.com",
  "status": "active",                    // active | paused
  "features": { "iap": true, "events": true, "config": true },
  "require_app_check": false,
  "ga4": { "event_prefix": "" },
  "platform_event_allowlist": ["purchase_verified", "..."],
  "iap": {
    "ledger_environment": "sandbox",     // sandbox | production
    "markets": ["google_play", "app_store", "apps_in_toss"],
    "google_play_package_name": "com.seorilabs.lizardtycoon",
    "entitlement_ids": ["sp_galaxy_gecko"]
  },
  "cors_origins": []
}
```

`features.firebase_custom_token_bridge`가 `true`이면 같은 Firebase 프로젝트의
`firebase_custom_token_service_account`가 필수다. 이 값은 비밀이 아니며, private key는
저장하지 않는다. platform-api가 해당 service account에 대한 IAM `signJwt` 권한만 받는다.

`features.iap`가 `true`이면 `iap.entitlement_ids`는 비어 있을 수 없다.
`IAP_CATALOG_JSON`은 마켓 SKU와 entitlement의 전역 매핑이고, 이 목록은
해당 앱에 운영자 지급할 수 있는 entitlement 경계다. 두 목록의 교집합만
Admin API에 노출하고 지급·회수에 허용한다.

활성 IAP 앱의 `markets`에 `google_play`가 있으면
`iap.google_play_package_name`이 필수이며 앱 사이에 중복될 수 없다. Google Play
RTDN의 package name은 이 값으로만 app ID에 연결한다. 환경변수나 알림 내용만으로
앱을 추측하지 않는다.

## 왜 API key가 없는가

클라이언트 바이너리에 박히는 순간 비밀이 아니다.

**진짜 인증은 `aud` 검증이 담당한다.** 어떤 앱을 사칭하려면 그 Firebase 프로젝트의 유효한 ID 토큰이 필요하다. `X-Seori-App` 헤더는 **어느 프로젝트로 검증할지 고르는 힌트일 뿐 권한이 아니다.**

## status: paused

kill switch다. 해당 앱의 모든 플랫폼 호출이 즉시 403이 된다.

**주의**: 진행 중인 결제도 막힌다. 이미 마켓에서 과금된 구매가 지급되지 않고 pending으로 남는다. 정지 해제 후 클라이언트의 pending proof 복구가 처리하지만 정지가 길수록 CS가 늘어난다.

## 등록된 앱

| app_id | 원장 환경 | IAP | entitlements |
|---|---|---|---|
| `babycare` | 미사용 | 비활성 | — |
| `cycle-pair` | production | 비활성 | — |
| `lizard-tycoon` | sandbox | 활성 | `sp_galaxy_gecko`, `sp_shootingstar_tokay` — App Review 기간 |

## ledger_environment가 서비스와 다르면

**admin 경로가 전부 422 `environment_mismatch`로 막힌다. 결제는 멀쩡하다.**

`LedgerEnvironment` 검사는 `internal/admin/handler.go`에만 있고 verify 경로에는
없다. 그래서 유저 결제는 계속 되고 운영자만 아무것도 못 하는 상태가 된다.
증상이 한쪽에서만 나서 알아채기 어렵다.

**그래서 admin이 스스로 알려준다.** `/v1/admin/health`가 어긋난 앱을 돌려주고
경고 로그를 남긴다. 백오피스 플랫폼 개요 화면에도 뜬다.

```json
{"environment":"production","deadLetterCount":0,
 "environmentMismatches":[
   {"appId":"lizard-tycoon","registry":"sandbox","ledger":"production"}]}
```

로그 기반 알림을 걸려면 이 한 줄을 본다.

```
severity=WARNING  "레지스트리와 원장 환경이 어긋나 조작이 막혔다"
  ledger_environment=production  apps=lizard-tycoon  count=1
```

2026-08-03에 실제로 겪었다. 서비스를 production으로 전환했는데 Firestore
레지스트리가 `sandbox`로 남아 있었다. repo 파일은 이미 production이었고
**regsync를 돌리지 않은 것**이 원인이다.

```
GET /v1/admin/apps/lizard-tycoon/iap/catalog
→ 422 environment_mismatch "앱 레지스트리와 Admin 원장 환경이 달라요"
```

이 엔드포인트가 200을 주는지로 확인한다. 반영은 캐시 TTL 60초 안에 끝난다.

두 원장은 경로가 다르고 서로 보이지 않는다(불변식 9). 환경을 바꾸면 이전
환경의 구매는 앱에서 사라진 것처럼 보인다. 지워진 것이 아니라 다른 공간에 있다.

```
production   processed_orders/...            (루트)
sandbox      iap_environments/sandbox/...
```
