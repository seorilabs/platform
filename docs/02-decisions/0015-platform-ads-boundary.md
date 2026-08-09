# ADR 0015: 광고 검증을 `platform-ads` 경계로 분리한다

## 상태

Accepted — 2026-08-09

## 맥락

게임별 보상 광고 검증은 신뢰 수준, replay 방지, 일일 한도, 보상 정산 상태가 서로 달랐다. 운영자 광고 차단과 `ad_free` 구매 권한도 각 앱에서 별도로 구현하면 같은 사용자가 서로 다른 정책을 보게 된다. AdMob SSV는 Google 서명을 검증할 수 있지만 AppsInToss 광고 완료 이벤트는 클라이언트 콜백이므로 같은 의미의 `verified`로 표현할 수 없다.

## 결정

- 공개 광고 API와 AdMob callback은 `PLATFORM_ROLE=ads`인 독립 Cloud Run 서비스에서만 연다.
- 광고 기능은 앱 레지스트리의 `features.ads`가 명시적으로 켜진 앱에서만 동작한다.
- claim 상태와 신뢰 수준을 분리한다.
  - 상태: `accepted -> confirmed -> delivered`, 미완료 claim은 `expired`
  - 신뢰: `pending`, `server_verified`, `client_confirmed`
- AdMob SSV만 `server_verified`가 될 수 있고 AppsInToss는 `client_confirmed`로 보존한다.
- 운영자 차단 또는 active `ad_free` 중 하나라도 있으면 rewarded와 interstitial을 모두 차단한다. 정책을 읽지 못하면 클라이언트는 광고를 load/show하지 않는다.
- 운영자 grant와 revoke는 append-only이고 projection만 현재 상태를 나타낸다. grant는 자동 만료하지 않는다.
- claim은 생성 24시간 후 만료하며 Firestore TTL 대상 `ttlAt`은 생성 90일 후다.
- 원문 transaction ID, SSV query/signature, 구매 token, AppsInToss authorization/access/refresh token과 userKey는 저장하거나 로그에 남기지 않는다.
- `lizard-tycoon`은 `features.ads=false`이며 광고 SDK와 API를 추가하지 않는다. 기존 `/v1/iap/*` 응답 계약은 변경하지 않는다.

## 결과

광고 장애는 IAP와 API 서비스의 IAM·동시성 한도를 소모하지 않는다. 클라이언트 확인과 서버 검증이 운영 화면에서 구분되고, 수동 보상 지급이나 강제 verified 전환은 제공하지 않는다. 레지스트리와 `regsync`가 광고 설정의 유일한 변경 경로다.

