# ADR 0013: platform이 앱 Firebase custom token을 원격 서명한다

## Status

Accepted

## Context

Babycare는 Firebase `signInAnonymously`가 직접 만든 uid를 돌봄 기록의 소유권으로 쓴다.
인증 진입점을 Seorilabs platform으로 통일하되 기존 uid가 바뀌면 그룹·멤버십·기록 접근이
끊긴다. 반대로 platform의 기존 `kind: anonymous`는 클라이언트가 값을 고를 수 있어
Firebase 권한을 주는 근거로 사용할 수 없다.

custom token은 앱 Firebase 프로젝트의 service account로 서명해야 한다. private key를
platform에 저장하면 공통 인프라 침해가 각 앱 Firebase로 번지므로 허용하지 않는다.

## Decision

`platform-api`에 `POST /v1/auth/firebase-custom-token`과
`DELETE /v1/auth/firebase-account`를 둔다.

- 기존 Firebase ID token이 있으면 현재 검증기에서 `aud`, `iss`, 서명, 만료를 확인하고
  같은 uid로 custom token을 만든다.
- 기존 token이 없으면 uid를 platform 서버의 암호학적 난수로 생성한다. 클라이언트가
  uid를 지정하는 필드는 두지 않는다.
- custom token에는 `seori_app_id`와 `seori_guest` developer claim을 서명한다. 기존
  Firebase uid 마이그레이션은 `seori_guest=false`, platform이 새로 만든 `pb_` uid만
  `seori_guest=true`다. 앱 backend는 UID 접두사만으로 게스트 권한을 판단하지 않는다.
- 앱 레지스트리에서 `firebase_custom_token_bridge` feature와 앱 프로젝트 service account를
  함께 명시해야만 발급한다.
- custom token은 IAM Credentials `signJwt`로 원격 서명한다. service account JSON과 private
  key는 저장·마운트하지 않는다.
- `platform-api@seorilabs-platform`에는 레지스트리에 지정된 앱 service account의
  `iam.serviceAccounts.signJwt`만 허용한다. `platform-iap`, `platform-admin`, worker에는 주지 않는다.
- 응답 token은 `Cache-Control: no-store`로 전달하고 서버·클라이언트 저장소와 로그에 남기지 않는다.
- 앱 레지스트리의 `require_app_check`가 켜지면 두 공개 경로 모두 Firebase App Check
  token을 해당 앱의 `firebase_project_id`에 묶어 검증한다. 구버전 호환을 위해 앱별로
  관측 후 전환하며, 검증기 배포만으로 기존 클라이언트를 차단하지 않는다.
- 계정 삭제는 유효한 Firebase ID token의 uid에 대응하는 Platform identity 매핑과
  사용자 문서만 지운다. 매핑이 이미 없으면 성공으로 처리하고 새 매핑을 만들지 않는다.
  IAP 감사 원장은 기존 삭제 계약대로 보존한다.

## Consequences

- 기존 Babycare 익명 사용자는 uid와 Firestore 소유권을 유지한 채 custom provider 세션으로
  전환할 수 있다.
- 신규 사용자의 uid는 platform이 만들지만 Firebase Auth와 데이터는 계속 앱 프로젝트가
  소유한다. ADR 0002와 ADR 0005의 프로젝트·PII 경계는 유지된다.
- `platform-api`가 Babycare 인증 가용성 경로에 들어온다. IAM Credentials 또는 Cloud Run 장애
  시 새 인증과 legacy migration은 실패하며 새 uid로 우회하지 않는다.
- 앱별 service account 생성, IAM Credentials API 활성화, resource-level Token Creator 부여가
  배포 선행 조건이다.
- 공개 bootstrap endpoint의 App Check 검증 경계를 제공한다. 실제 강제는 앱별
  `require_app_check` 전환과 새 클라이언트 실기기 검증 뒤 수행한다. bridge를 활성화한
  앱만 호출할 수 있고 Firebase uid 사칭은 허용하지 않는다.
