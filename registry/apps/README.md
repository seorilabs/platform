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
| `lizard-tycoon` | production | 활성 | `sp_galaxy_gecko`, `sp_shootingstar_tokay` |
