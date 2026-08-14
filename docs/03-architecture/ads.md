# Ads

## 불변식

1. `features.ads=false`인 앱은 광고 claim을 만들 수 없다.
2. claim 소유자는 검증된 Platform 세션의 `(appId, platformUserId)`에서만 정한다.
3. `server_verified`는 AdMob 서명 검증 callback만 만들 수 있다. AppsInToss는 항상 `client_confirmed`다.
4. 같은 provider transaction은 한 번만 claim을 확정한다. 원문 대신 SHA-256 hash를 replay key로 저장한다.
5. `confirmed` 전에는 보상하지 않고, 앱의 로컬 exactly-once 정산 뒤 `ack`해 `delivered`로 바꾼다.
6. 일일 한도와 cooldown은 확정 트랜잭션에서 원자적으로 검사한다.
7. 운영자 차단과 `ad_free`는 독립 원인이다. 어느 하나라도 활성인 동안 모든 광고 형식을 차단한다.
8. 정책 조회 실패는 허용으로 해석하지 않는다. 앱 adapter는 load 직전과 show 직전에 정책을 다시 확인한다.
9. 운영자 grant/revoke 문서는 삭제하거나 덮어쓰지 않는다. 현재 상태는 별도 projection으로만 읽는다.
10. raw token, 영수증, SSV query/signature, Toss userKey와 PII를 저장·표시·로그하지 않는다.

## 서비스 경계

`platform-ads`만 `/v1/ads/*`와 공개 AdMob callback을 연다. `platform-admin`은 동일 Ads 저장소의 Admin 유스케이스만 조립하고 `/v1/admin/ads/*`를 연다. 앱 레지스트리와 identity session verifier는 공유하지만 Ads가 Admin 패키지를 import하지 않는다.

## 상태 전이

```text
accepted -- AdMob SSV --> confirmed server_verified -- app ack --> delivered
accepted -- AIT confirm -> confirmed client_confirmed -- app ack --> delivered
accepted -- 24h --------> expired
```

`assurance`는 delivery 여부가 아니므로 `delivered` 뒤에도 보존한다.

## Firestore

- `ad_reward_claims/{claimId}`: claim과 90일 TTL 필드
- `ad_transaction_replays/{sha256}`: provider transaction replay fence
- `ad_usage/{sha256(appId,puid,placement,date)}`: 일일 횟수와 마지막 확정 시각
- `ad_policy_projections/{sha256(appId,puid)}`: active 운영자 차단 projection
- `ad_suppression_grants/{requestId}`: append-only grant
- `ad_suppression_revocations/{requestId}`: append-only revoke
- `ad_health/current`: SSV와 policy 진단 집계

## 운영

광고 설정 변경은 `registry/apps/*.json` 수정, 리뷰, `regsync`, live readback 순서로만 수행한다. Admin UI는 읽기 전용 설정을 표시하고 직접 수정하지 않는다.

### 멀티앱 SSV 경계

- callback은 `/v1/ads/admob/ssv/{appId}`로 받고 claim의 `appId`, 사용자,
  provider, 광고 unit, reward를 모두 대조한다.
- 같은 앱 안에서는 placement가 AdMob unit을 공유할 수 있지만 서로 다른 앱은
  같은 unit을 등록할 수 없다. `regsync`가 전체 레지스트리를 검사해 부분 반영 전에
  실패하고 런타임도 충돌한 앱을 제외한다.
- 전역 `/v1/admin/ads/health`는 기존 운영 호환을 위해 유지한다. 신규 운영은
  `/v1/admin/apps/{appId}/ads/health`를 사용해 Google Console probe와 실제 보상
  callback 성공을 구분한다. 앱별 stale claim 집계는 `appId + state + createdAt`
  복합 인덱스가 `READY`인 뒤 배포한다.
- 새 Godot 게임은 `sdk-gdscript`의 Firebase identity와 rewarded claim adapter를
  vendoring한다. 게임 코드는 네이티브 광고 표시와 로컬 보상 정산만 소유하고,
  Platform 로그인·정책·claim·SSV 조회·ack 순서를 다시 구현하지 않는다.

`platform-ads` 배포 전에는 전용 runtime service account와 Firestore 권한,
`platform-session-secret` 접근 권한을 준비한다. AppsInToss 로그인은 승인된 mTLS
certificate와 key를 별도 Secret Manager gate로 마운트한 뒤에만 활성화한다. 인증서가
없거나 교환이 실패한 상태에서는 AppsInToss 광고 세션을 발급하지 않는다.

Happy Farm IAP를 활성화하기 전에는 `iap-catalog`을 앱별 v2 형식으로 갱신하고
Google Play와 App Store 상품이 실제로 조회되는지 확인한다. registry 반영이나 코드
배포만으로 상품 생성, 스토어 심사, 공개 출시가 완료된 것은 아니다.
