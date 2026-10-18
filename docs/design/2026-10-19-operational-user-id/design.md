# 신규 계정 운영 UID 전달

사용자가 Discord 앱운영이벤트에 UID 표시를 요청하고 「Platform 사용자 ID: 운영 사용자 조회에 쓰는 ID」를 선택했다. 신규 계정 이벤트의 선택 속성에 서버 생성 Platform 사용자 ID만 추가한다. 앱 로그인 UID, 이름, 이메일, 토큰과 결제 식별자는 보내지 않는다. 운영 결정의 원장은 Obsidian `프로젝트/platform/02-decisions/0035-operational-user-id.md`이며 이 파일은 중앙 개발 검증의 승인 연결점이다.

신규 계정 transaction → 동일 outbox의 선택 속성 `platformUserId` → 기존 HMAC sender → 먼저 배포한 Backoffice 허용 목록 → 요청한 Discord 한 줄. 기존 eventId, 도메인 transaction, debug 계정 제외와 재시도는 유지한다. `pu_`와 26자리 ULID만 허용하며 다른 이벤트에는 추가하지 않는다. 과거 outbox와 수신자는 필드 없이 동작한다. 계약은 spec/openapi.yaml에 먼저 명시한다.

AC-1 실제 계정 생성 outbox에 서버가 만든 ID가 포함되고 앱 로그인 UID가 포함되지 않는다. AC-2 허용 목록과 ID 형식은 잘못된 입력을 거부하고 기존 필드 없는 이벤트는 허용한다. AC-3 기존 서명, 저장, 실패 재시도, debug 제외와 도메인 상태를 보존한다.

L1 dev/service-runtime, 변경 패키지 단위와 실제 격리 Firestore 계정 생성 및 outbox 전달을 검증한다. go vet, Go 테스트와 lint를 실행하고 기존 CI 및 production 서비스 실행 검증을 유지한다. 보안과 정보 전달 경계는 로컬 Claude 교차 리뷰를 받는다. UI와 기기는 해당 없다. Backoffice 수신자 반영 뒤 기존 production 파이프라인으로 Platform의 승인된 끝 지점까지 반영하고 실제 source revision과 원본 이벤트를 확인한다. 원장과 기존 ID를 변경하지 않으며 이전 이미지로 되돌릴 수 있다. Version Impact: patch.
