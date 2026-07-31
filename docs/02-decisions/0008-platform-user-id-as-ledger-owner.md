# ADR 0008: IAP 원장의 소유자 키를 platform_user_id로 한다

## Status

Accepted

## Context

lizard-tycoon의 기존 IAP 원장은 **Firebase `uid`를 소유자 키**로 쓴다. `iap_users/{uid}/entitlements/{entId}` 구조다.

플랫폼으로 옮기면서 이 키를 유지할지, `platform_user_id`로 바꿀지 정해야 한다.

플랫폼 identity 레이어는 이미 `(app_id, firebase_uid) → platform_user_id` 매핑을 갖는다.

## Decision

**`platform_user_id`를 소유자 키로 쓴다.**

```
iap_users/{platform_user_id}/entitlements/{entitlementId}
processed_orders/{orderKey}.puid = platform_user_id
```

### 지금이 바꾸기 가장 싼 시점이다

**lizard-tycoon은 미론칭이다.** 실매출도 실사용자도 없고 Firestore 원장에는 샌드박스 데이터뿐이다. **마이그레이션 부담이 사실상 0**이다.

론칭 후에 바꾸려면 실결제 데이터를 안고 키를 재지정해야 하고, 그건 훨씬 위험하다.

### 왜 firebase_uid를 그대로 쓰지 않는가

- **앱마다 uid 네임스페이스가 다르다.** 플랫폼은 여러 앱의 원장을 한 저장소에 둔다. `uid`만으로는 어느 앱인지 알 수 없어 `(app_id, uid)` 복합키가 필요해진다
- **`platform_user_id`는 재지정이 가능하다.** 계정 병합이나 이관이 필요해질 때 매핑만 바꾸면 원장을 건드리지 않는다. `firebase_uid`에서 파생하면 이 유연성이 사라진다
- **크로스앱 entitlement의 문이 열린다.** 1단계에서 쓰지는 않지만 구조가 막지 않는다

## Consequences

- **불변식 4(cross-uid 자동 이전 금지)의 의미가 바뀐다.** 이제 "cross-platform_user_id 금지"다. 앱 재설치로 새 `firebase_uid`가 생기면 새 `platform_user_id`가 되고, 기존 소유자와 다르므로 409가 난다
  - 마켓 복원이 있어 재검증하면 새 소유자에게 지급되지만, **기존 uid가 살아 있으면 충돌**한다
  - 이건 lizard-tycoon이 이미 안고 있던 제약이고 그대로 승계한다. CS 화면에 "익명 인증 — 재설치 시 이력 단절" 배지를 상시 표시한다
- **identity 레이어가 IAP의 전제 조건이 된다.** P1이 P4보다 먼저여야 하는 구조적 이유다
- **`platform_user_id`가 없으면 결제할 수 없다.** anonymous 신원이 IAP에서 금지되는 것과 같은 맥락이다
- 백오피스 CS 화면의 검색 키가 `firebase_uid`에서 **support code**로 바뀐다. `firebase_uid`로도 찾을 수 있게 identity 매핑을 역조회한다

## Alternatives Considered

- **`(app_id, firebase_uid)` 복합키 유지** — 마이그레이션이 0이지만 복합키가 모든 경로에 퍼지고, 계정 병합 시 원장 전체를 재작성해야 한다
- **론칭 후에 전환** — 실결제 데이터를 안고 키를 재지정해야 한다. **지금보다 명백히 위험하다**
