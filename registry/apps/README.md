# App Registry

**이 디렉토리가 앱 레지스트리의 source of truth다.** 콘솔이나 Firestore를 직접 수정하지 않는다.

CI가 스키마를 검증한 뒤 Firestore로 upsert하고, 런타임은 인메모리 캐시로 조회한다.

## 형식

```jsonc
// lizard-tycoon.json
{
  "app_id": "lizard-tycoon",
  "display_name": "도마뱀 테라리움",
  "firebase_project_id": "lizard-tycoon",
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

아직 없음. P1에서 `lizard-tycoon.json`부터 추가한다.
