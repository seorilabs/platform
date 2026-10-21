# 기간 이용권 원장

사용자가 2026-10-21에 「플랫폼에 30일 수령 원장 추가」를 선택했다. 첫 적용은 Amber Lizards(`merge-lizard`)의 30일 호박(`amber_pass_30d`, SKU `merge_lizard_amber_pass_30d`, 소모성 $3.99, 즉시 300 + 30일 동안 매일 50)이다. 중앙 BM 템플릿의 `monthly_pass` 슬롯(seorilabs/planning#52, DEC-M21)에 맞춰 다른 앱도 카탈로그 설정만으로 쓰게 한다. 결정의 원장은 Obsidian `프로젝트/platform/02-decisions/0036-daily-pass-ledger.md`이고 이 파일은 중앙 개발 검증의 승인 연결점이다.

소모성 구매 → 기존 검증 경로(즉시분은 `granted=true`) → source의 처음 활성 서버 시각 → 카탈로그 `pass` 일정 → `GET /v1/iap/passes` 상태와 `POST /v1/iap/passes/claim` 수령 → 요청 번호별 영구 증거. 하루는 처음 활성 시각부터 24시간, 이용 중 재구매는 끝나는 날만 미루고, 환불이나 취소는 그 구매의 남은 날만 멈춘다. 놓친 날 몫은 기본으로 사라지고 카탈로그 값 하나로 몰아 받기로 바꾼다. 계약은 spec/openapi.yaml에 먼저 적는다.

AC-1 같은 날 두 번, 동시 요청, 재시도는 요청 번호로 한 번만 지급한다. AC-2 재검증과 다른 기기로의 소유권 이전이 남은 날, 받은 날 수, 오늘 수령 기록을 바꾸지 않는다. AC-3 이용 중 재구매는 끝나는 날을 30일 미루고 하루 지급량은 그대로다. AC-4 환불되거나 취소된 구매의 남은 날은 받지 못하고 다른 구매의 날짜는 그대로다. AC-5 `pass`가 없는 카탈로그 항목과 다른 앱의 구매 검증 동작은 바뀌지 않는다. AC-6 판매 중인 일정은 `missed_days` 전환만 병합 도구가 허용한다.

L1 dev/service-runtime. 순수 판정 표 테스트, 카탈로그 해석, 병합 도구, HTTP 경계, 실제 격리 Firestore 트랜잭션(동시 수령, 재시도, 재검증, 이전, 재구매, 환불, 보류 결제)과 실제 TCP HTTP 검증부터 수령, 재시작, 취소까지를 실행한다. go vet, go test -race, golangci-lint, 통합 태그 테스트, regsync dry-run, 스펙 라우트 대조, 배포 경계 검사, node 테스트를 돌린다. 기기와 UI는 해당 없다. 실제 마켓 결제와 클라이언트 연결은 merge-lizard 작업이다. 반영은 서버 배포 → 운영 비밀값 → 새 revision 순서이며(`registry/iap-catalog/README.md`), 비밀값 반영 전 이전 이미지로 되돌릴 수 있다. Version Impact: minor(계약 추가).
