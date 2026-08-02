# Identity

## 세션 교환

```
POST /v1/auth/session
{ "appId": "lizard-tycoon",
  "credential": { "kind": "firebase-id-token" | "ait-login" | "anonymous", "value": "..." } }
→ { "platformToken": "<JWT TTL 1h>", "refreshToken": "<opaque TTL 90d>",
    "platformUserId": "pu_...", "appUserId": "...", "expiresIn": 3600 }
```

이후 모든 호출은 `Authorization: Bearer <platformToken>`.

### Firebase ID 토큰을 매 호출에 직접 쓰지 않는 이유

1. **1시간마다 갱신이 필요하다.** Godot은 매 호출 앞에 `HTTPRequest`가 2회 붙는데, Godot의 `HTTPRequest`는 동시 1요청만 처리하므로 IAP 검증과 큐에서 경합한다
2. **AIT에는 Firebase ID 토큰이 아예 없다.** `appLogin` 또는 `getAnonymousKey`뿐이라 **credential 종류를 추상화할 지점**이 필요하다

## 토큰 검증

### 설계를 단순화한 사실

Firebase ID 토큰의 서명키는 **전 프로젝트 공통**이다.

```
https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com
```

이 키셋 하나가 **모든 Firebase 프로젝트**의 ID 토큰을 서명한다. 프로젝트별 키가 아니다. 따라서 **앱 16개의 자격증명을 하나도 보유하지 않고** 검증할 수 있고, 프로젝트 구분은 서명이 아니라 claim으로 한다.

### 검증 항목 — 전부 강제

| 항목 | 값 |
|---|---|
| `alg` | **`RS256`만.** `none`/`HS256` 혼동 공격 차단 |
| `kid` | 키셋에 존재 |
| 서명 | 유효 |
| `exp` / `iat` | clock skew 60초 |
| `aud` | `registry.firebase_project_id`와 일치 |
| `iss` | `https://securetoken.google.com/{project_id}` |
| `sub` | 비어있지 않고 128자 이하 |

구현은 `golang-jwt/jwt/v5` + JWKS 캐시. **알고리즘·issuer·audience 판정을 라이브러리 옵션에 전부 위임하고 직접 파싱하지 않는다.**

ID token **검증에는 Firebase Admin SDK for Go를 쓰지 않는다.** 프로젝트별 App 인스턴스가 필요해 16개 앱에 부적합하고, 메모리와 콜드스타트가 낭비된다.

## Firebase custom token bridge

```
POST /v1/auth/firebase-custom-token
{ "appId": "babycare", "existingFirebaseIdToken": "선택" }
→ { "firebaseCustomToken": "...", "appUserId": "..." }
```

- 기존 token이 있으면 위 검증 규칙으로 확인한 uid를 그대로 쓴다. Babycare의 기존 Firestore
  소유권을 끊지 않는 migration 경로다.
- token이 없으면 `pb_` + ULID uid를 서버에서 만든다. 클라이언트가 uid를 선택할 수 없다.
- custom token 생성만 IAM Credentials `signJwt`를 쓴다. 앱별 private key나 service account
  JSON은 platform에 두지 않는다.
- 레지스트리 feature와 앱 프로젝트 service account가 모두 있어야 한다.
- 응답 token은 1회용 전달값이다. 로그·Firestore·클라이언트 저장소에 남기지 않는다.

세부 보안 결정은 ADR 0013을 따른다.

### `checkRevoked` — 판단이 필요한 지점

lizard-tycoon은 `verifyIdToken(token, checkRevoked=true)`를 쓴다. 이건 **매 요청 네트워크 호출**이다.

플랫폼은 **세션 교환 시점에만** 적용하고 이후 `platformToken`은 자체 검증한다. TTL 1시간이 revocation 지연의 상한이 된다. 결제 경로에서 즉시성이 필요하다고 판단되면 재검토한다.

## API key를 두지 않는다

클라이언트 바이너리에 박히는 순간 비밀이 아니다. 현재 조직의 `analytics.config.json`의 `api_secret`이 이미 그 상태다.

**진짜 인증은 `aud` 검증이 담당한다.** lizard-tycoon을 사칭하려면 그 Firebase 프로젝트의 유효한 ID 토큰이 필요하다.

`X-Seori-App` 헤더는 **어느 프로젝트로 검증할지 고르는 힌트일 뿐 권한이 아니다.** 헤더를 바꿔도 `aud` 불일치로 즉시 거부된다.

## anonymous 신원 — 반드시 강제

`getAnonymousKey()` 해시는 **bearer 자격증명이 아니다.** 클라이언트가 자기 값으로 아무거나 보낼 수 있다. 즉 `kind: "anonymous"`로 발급된 세션은 **타인 사칭이 가능**하다.

| 기능 | anonymous | 사칭 시 잃는 것 |
|---|---|---|
| RemoteConfig 조회 | 허용 | 무해 |
| 이벤트 로그 | 허용 | GA4 MP도 원래 위조 가능 |
| **IAP 검증·entitlement** | **금지** | **치명적** |

SDK와 서버 **양쪽에서 이중 강제**한다. AIT는 `appLogin()` 성공 신원만 결제할 수 있다.

## platform_user 모델

```
identities/{app_id}__{firebase_uid}   → { platformUserId, signInProvider, firstSeenAt, lastSeenAt }
users/{platform_user_id}              → { appId, createdAt, locale, tz, lastSeenAt }
```

`platform_user_id`는 `pu_` + ULID. **firebase_uid에서 파생하지 않는다** — 나중에 계정 병합이나 이관 시 재지정이 가능해야 하기 때문이다.

### IAP 원장의 소유자 키도 platform_user_id다

lizard-tycoon 원장은 Firebase `uid`를 소유자 키로 쓴다. 플랫폼으로 옮기면서 `platform_user_id`로 바꾼다. **미론칭이라 마이그레이션 부담이 0**이고, 크로스앱 entitlement의 문이 열리는 부수 효과도 있다. → ADR 0008

## 익명 인증의 구조적 한계 — 해결하지 않고 명시

앱 재설치 = 새 Firebase UID = 새 `platform_user`. **비소비성 구매가 끊긴다.**

다만 마켓 복원(`restore_purchases`)이 있어 재검증하면 새 `platform_user`에 다시 지급된다. 단 **cross-uid 자동 이전은 금지**되어 있으므로(불변식 4) **기존 uid가 살아 있으면 409로 충돌**한다.

이건 lizard-tycoon이 이미 안고 있는 제약이고 그대로 승계한다. 근거는 원본 코드의 정책 주석이다.

> 저장된 설치 신원이 있는데 refresh credential만 손상·유실된 경우 새 anonymous uid를 자동 생성하지 않는다. cross-uid 복원은 지원하지 않으므로 운영 복구 대상으로 남긴다.

**CS 화면에 "익명 인증 — 재설치 시 이력 단절" 배지를 상시 표시**해 오해를 원천 차단한다. 근본 해결(계정 연동 + 병합)은 2단계.

orphan은 Firestore TTL로 `lastSeenAt + 400일`에 자동 삭제한다.

## PII 정책

> **플랫폼은 개인정보를 저장하지 않는다.** 갖는 식별자는 `platform_user_id`와 `(app_id, firebase_uid)` 매핑뿐이다. 이메일·이름·전화번호는 각 앱의 Firebase Auth에만 남는다.

마켓 계정 식별자도 원문을 저장하지 않고 **sha256만** 남긴다.

`firebase_uid`가 가명정보에 해당하므로 규제 완전 밖은 아니지만, 이메일·이름을 보관하는 것과는 리스크 차원이 다르다. → ADR 0005

### CS는 support code로

```
앱 설정 화면 → [내 지원 코드 복사] → LT-8F3K2Q9M
유저 문의 시 코드 첨부 → 백오피스에서 코드로 검색
```

이메일 검색은 애초에 성립하지 않는다. 플랫폼이 이메일을 받지 않고, Firestore는 부분 검색이 불가하다. 현재 조직 앱은 대부분 익명 인증이라 **이메일이 존재하지도 않는다.**

### 1단계 필수 항목

| 항목 | 비고 |
|---|---|
| **이벤트 파라미터 PII 키 blocklist** | `email`/`phone`/`name`/`address`/`birth` 등을 SDK가 drop. **개발자가 무심코 `log_event("login", {email})` 하는 게 가장 흔한 사고** |
| `DELETE /v1/users/me` | 앱 계정 삭제 시 플랫폼도 삭제. **PII를 안 갖더라도 삭제 경로는 있어야 한다** |
| Firestore TTL | orphan 400일 |
| Data safety / App Privacy 갱신 | "기기 ID", "앱 활동" 추가 |
| 개인정보 처리방침 갱신 | "플랫폼 서버로 전송" 반영 |
| support code 파생 규칙 | `확정 필요` |

### 2단계 — 지금 만들지 않음

계정 연동으로 이메일이 필요해지면 **저장하지 않는 방식**을 쓴다. 플랫폼이 각 앱 프로젝트에 `roles/firebaseauth.viewer`를 갖고, CS 조회 **시점에만** Admin SDK로 가져와 화면에 표시만 하고 저장·로깅하지 않는다.
