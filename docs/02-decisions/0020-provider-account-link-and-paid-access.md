# ADR 0020: 외부 계정은 OIDC 어댑터로 연결하고 유료 접근은 연결 계정에 묶는다

- 상태: Accepted
- 날짜: 2026-08-23

## 맥락

운글은 결과 조회 전 로그인을 강제하지 않는 guest-first 앱이다. 무료 명식과 해설은 바로 볼 수
있어야 하지만 소모성 열람권은 재설치, 기기 변경, 결제 오류 때 서버 원장을 다시 찾을 신뢰 가능한
계정이 필요하다. Firebase anonymous uid만으로는 설치가 바뀌었을 때 같은 사용자를 복원할 수 없다.

한국 사용자에게 카카오 로그인이 가장 익숙하지만 특정 SDK나 공급자 subject를 도메인과 원장에
직접 넣으면 iOS의 Sign in with Apple, AppsInToss 로그인과 다른 신뢰 규칙이 유스케이스에 섞인다.
이메일이나 전화번호를 받아 계정을 찾는 방식은 ADR 0005의 PII 비저장 원칙에도 맞지 않는다.

## 결정

1. 계정 연결 유스케이스는 `AccountProvider`와 `AccountRepository` 포트만 안다. 카카오와 Apple의
   issuer, JWKS, audience, nonce 형식은 `identity/providers/oidc` 어댑터 안에 둔다.
2. 연결 시작 전에 Platform이 256-bit nonce를 만들고 5분 challenge로 앱, 현재
   `platform_user_id`, provider에 묶는다. challenge와 완료 요청은 검증된 Platform 세션과
   앱별 Firebase App Check를 모두 요구한다. 클라이언트가 고를 수 있는 `kind=anonymous`
   세션은 거부한다.
3. 공급자 ID token은 RS256, 서명, `kid`, 고정 issuer, 레지스트리 audience, `exp`, `iat`,
   `sub`, challenge nonce를 검증한다. 카카오는 raw nonce, Apple은 raw nonce의 SHA-256 hex를
   token claim과 대조한다. 처음 보는 `kid`는 유효한 캐시가 남아 있어도 JWKS를 한 번 갱신한다.
4. provider subject 원문, 이메일, 이름, 전화번호는 저장하지 않는다. 저장소 어댑터가 subject를
   즉시 SHA-256 처리하고 `(app_id, provider, subject_hash)`를 `platform_user_id`에 원자적으로
   연결한다.
5. 공급자 매핑이 없으면 현재 guest Platform 사용자에 연결한다. 이미 같은 사용자에 연결됐으면
   멱등 성공한다. 새 설치의 아직 연결되지 않은 guest가 기존 매핑을 증명하면 기존 Platform
   사용자와 Firebase uid를 복원한다. 현재 사용자와 대상이 각각 다른 공급자 계정을 이미 가진
   경우 자동 병합하지 않고 `account_link_conflict`로 종료한다.
6. 연결 완료 후 `isLinkedAccount=true`인 새 Platform access/refresh session과 대상 Firebase
   uid의 custom token을 발급한다. 오래된 guest session은 연결 권한으로 승격하지 않는다.
7. 앱 레지스트리의 `iap.require_linked_account=true`이면 구매 검증, entitlement 복원,
   마켓 account reference 발급을 모두 연결 세션에만 허용한다. 기본값은 false라 기존 앱 계약은
   유지한다.
8. AppsInToss는 별도 provider OIDC 연결을 하지 않는다. mTLS로 교환한 Toss Login 세션 자체를
   연결 계정으로 본다. AppsInToss의 결제는 계속 AIT IAP만 사용한다.
9. 운글 native는 카카오를 주 경로로, iOS에서는 Sign in with Apple을 동등한 선택지로 제공한다.
   네이버는 초기 범위에서 제외한다.
10. 실제 provider audience 등록, Firebase service account 권한, App Check 강제, 앱 SDK 설정,
    Apple authorization code 교환과 계정 삭제 시 token 철회, 개인정보 처리방침 갱신, 실기기
    복원 검증이 끝나기 전에는 운글 레지스트리에서 provider와 IAP를 활성화하지 않는다.

## 결과

- 무료 사용은 계정 연결 없이 유지되고, 결제 시점에만 복원 가능한 계정을 요구한다.
- 공급자별 SDK와 token 형식은 어댑터에 격리되고 도메인 원장은 동일한
  `platform_user_id`만 사용한다.
- 앱 삭제나 재설치 후 같은 provider를 증명하면 소모성 잔여 열람권을 기존 원장에서 찾을 수 있다.
- 자동 계정 병합을 하지 않으므로 두 계정의 결제 이력을 잘못 합치는 위험을 피한다. 필요한 병합은
  별도의 본인 확인과 운영 정책이 마련되기 전까지 지원하지 않는다.
- 코드 merge는 카카오·Apple 콘솔 등록, Apple 철회 경로, 배포, 실기기 로그인 성공을 의미하지 않는다.
