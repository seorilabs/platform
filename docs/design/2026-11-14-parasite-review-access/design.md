# My Little Parasite 앱 전용 체험 인증

사용자가 게임 저장소 review-access-options.md A안을 승인했다. 기존 identity 패키지 안에서 앱별 체험 코드를 검증해 별도 Firebase UID를 발급한다. 새 서비스·키·Google 계정을 만들지 않는다. API 계약은 spec/openapi.yaml의 createReviewAccess다. 전체 흐름과 저장 분리는 [게임 상세 설계](https://github.com/seorilabs/my-little-parasite/tree/feat/review-access-code/docs/design/2026-11-14-review-access)에 연결한다.

auth.review_access_code_sha256이 있는 iap:false 앱에서만 열며 App Check, 상수 시간 코드 대조, 앱 단위 분당120요청 제한을 적용한다. 원장 JSON에는 검증용 해시를 준비했으며 Firestore 운영 레지스트리에는 아직 반영하지 않았다. 코드는 160비트 이상 난수를 앱별 자격증명으로 보관하고 서버에는 SHA256만 올린다.

서버가 만든 앱별 review UID만 허용하며 Firebase ID 토큰의 custom 공급자·seori_app_id·seori_guest=false 서명 claim을 함께 확인한다. UID 접두사만으로 권한을 올리지 않는다. 세션과 refresh는 체험 클라우드를 지원하지만 Google 연결·일반 계정 복구를 거부한다. 체험 신원은 AuthType=review로 기록하고 신규 실제 사용자 운영 이벤트에서 제외한다.

Firestore transaction으로 앱별 현재 체험 UID와 분당 요청 수를 유지한다. 동시 진입은 한 UID를 사용한다. 기존 삭제 마커가 있는 체험 UID는 새 UID로 교체하되 원래 삭제·차단·일반 사용자 자료를 지우거나 차단을 해제하지 않는다. 운영자 차단을 우회해 새 UID를 생성하지 않는다. 계정 삭제 worker와 앱 Firestore 소유권 규칙은 그대로 사용한다.

새로운 코드·IAM 권한을 운영에 만들기 전에 자격증명 preflight, 암호화 백업·양쪽 복원 검증을 한다. 필요한 실제 권한은 기존 app signer의 signJwt뿐이다. 구현과 unit·실제 HTTP·Firestore emulator 실행, 독립 L3 검증 뒤 중앙 검증과 정확한 상세 계획 승인 연결을 확인하고 배포한다. 현 승인 A안과 이후에 형식화한 이 상세 계획의 시점을 구분하며, 승인 연결이 없는 gate를 통과로 쓰지 않는다. 실제 사용자 삭제·기기 사용을 포함하지 않는다.
