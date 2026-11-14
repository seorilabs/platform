# 체험 코드 적용 조건과 남은 실행 승인

## 이미 구현하고 확인한 결과

Google 연결과 클라우드를 유지한 앱 전용 체험 코드를 구현했다. 서버 runtime 후보30d736519bea2ddca1f3c57b46bb33c83e168ae8의 단위·vet와 실제 HTTP/Firestore 전체18기능955개가 통과했다. 외부 Firebase/App Check/IAM은 대체 구현이다. 게임은 [PR14](https://github.com/seorilabs/my-little-parasite/pull/14)의07f4fe91fd2b9a8dfbb0388d0366933fa125eba2로 병합했으며 원본0727b7b과 tree가 같다. 개인 Google 계정과 비밀번호는 필요하지 않다.

## 승인받을 적용 조건 보완

로컬 코드 리뷰2회차에서, 전체 계정 삭제(account_deletion)가 꺼진 다른 opt-in 앱의 구형 삭제 API는 체험 별칭을 회수하지 않는 경우가 확인됐다. My Little Parasite의 account_deletion은 이미true이므로 현재 앱의 해당 구형 삭제 API는 차단돼 있다. 다른 앱의 구형 삭제를 재설계하는 대신 **체험 코드는 전체 계정 삭제를 지원하는 무료 앱에만 허용**하도록 적용 조건을 좁히는 것을 제안한다.

승인 후 registry.validateAuth, CreateReviewAccess, reviewIdentity의 체험 허용 조건에 account_deletion:true를 요구한다. 코드 해시가 있으나 전체 삭제가 꺼진 구성은 로드 시 거부하고, 주입/구성 변경 경계에서도 발급·서명 세션을 거부한다. 이 경계의 단위 검사와 기존 전체18기능 회귀를 실행한다. My Little Parasite와 다른 앱의 기존 account_deletion 설정, 구형 삭제 동작, 일반 사용자 자료는 바꾸지 않는다. 새 체험 opt-in은 아직 운영에 활성화하지 않았다.

두 차례 모델 리뷰는 모두 보존하며 세 번째 리뷰를 돌리지 않는다. 반복해서 발견된 같은 영역의 결함은 설계 재검토를 사람에게 확인하도록 한 seori-pr-workflow의 3절에 따라 이 조건을 승인받기 전 구현·병합·배포를 진행하지 않는다.

## 함께 승인받을 검수와 서버 연결

게임 [상세 계획과 추가 검수 제안](https://github.com/seorilabs/my-little-parasite/blob/0727b7b8250124c4157e9d2f0c9ff736cae33ada/docs/design/2026-11-14-review-access/additional-review-proposal.md), 같은 커밋의 design.md와 test-plan.json을 승인 대상으로 삼는다. 기존 실패·중단1~8회 원본은 보존하고, 현재20핵심 기능을 개발 환경에서 독립 검수9·10·11회 각1회·최대60분, 총180분 실행한다. 기기·새 유료 이미지/음악 API·회차12·시간 연장은 포함하지 않는다.

서버의 같은 폴더 design.md와 test-plan.json에 따른18기능 L3를 최종 후보의 독립 실행 또는 확인된CI로 검증한다. 코드 수정 뒤 실제 최종 SHA·원본 실패·회귀 결과를 연결하고 중앙 before-deploy가 통과한 **같은 이미지**를 기존6대상에 배포한다. 이 앱의 auth.review_access_code_sha256만 운영 레지스트리에 반영하고 readback한다. 새 키/IAM, 다른 앱의 설정, 실제 일반 사용자 삭제, 운영 우편과 보상 지급은 포함하지 않는다.

중앙 검증의 정확한 승인 커밋 연결, 기존 UI1~8 원본과7일 관찰, 네이티브 광고·인증·위젯 미검증은 면제하지 않는다. 실제 네이티브 항목은 최종 후보가 준비된 뒤 필요한 항목만 따로 기기 사용 승인을 받는다. 지금의 검수·서버 연결 승인도 기기 사용 승인이 아니다. Play 등록9/12, 빌드·업로드·심사·승인·공개 미완료 판정은 그대로다.
