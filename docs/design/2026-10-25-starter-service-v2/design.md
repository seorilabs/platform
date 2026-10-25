# Starter 서비스 실행 계획 연결

제품 설계 정본은 [Starter 승인 설계](https://github.com/seorilabs/starter-template-game/blob/f28dfb430a7fac7a807ce6783276f0d837bfd480/docs/design/2026-10-25-live-platform-game/design.md)와 Obsidian `프로젝트/platform/03-architecture/adr/0037-starter-wallet.md`, `0038-starter-account-save-deletion.md`다. 이 문서는 중앙 검사가 읽을 서비스 실행 계획의 연결점이다.

Platform PR #257의 Starter 등록, 테스트 구매 제한, 서버 지갑, 계정 삭제와 운영 이벤트 및 SDK 1.1.0에 대해 L3 전체 서비스 회귀를 적용한다. 외부 공급자 경계는 대역으로 구분하고 실제 서비스 코드, TCP HTTP와 격리 Firestore의 모든 구현 기능을 다른 검수자 또는 CI가 실행한다. 실패, skip, 누락 또는 오래된 근거는 통과로 표시하지 않는다.

결과는 중앙 v2 실행 근거와 보관 readback으로 확인한다. 운영 적용은 기존 production reviewer와 6개 역할의 같은 이미지 및 readiness readback을 유지한다. 모바일 앱 공개 출시, 심사 제출, 실기기 및 시뮬레이터 사용은 포함하지 않는다. 앱의 배포 끝 지점은 Play와 TestFlight 내부 테스트다.

이 계획은 오늘 승인 범위의 실행 조건을 문서로 결속한다. 기존 10월 13일 승인에 없던 test-plan.json을 소급해 승인된 파일로 표시하지 않는다.

인수조건은 AC-SERVICE-RUNTIME(전체 서비스 실제 실행), AC-EVIDENCE(v2 형식과 후보, 시각, 지문 일치), AC-RELEASE-GATE(통과 전 빌드 및 운영 배포 차단)다.
