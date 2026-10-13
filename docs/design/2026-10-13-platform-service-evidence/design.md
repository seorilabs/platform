# TC-SDK100-SERVICE-EVIDENCE-v1

## 목적과 승인 범위

SDK 1.0.0은 `f58999adf562c67f40a2e2d7d8e9faffc197a4f5`에서 정식 발행됐다. 운영 배포 전 중앙 검증에 필요한 서버 기능 목록과 서비스 실행 근거를 마련한다. 사용자의 “근거 마련하고 중앙검증 통과해 그럼” 요청에 따른 검증 작업이다. 아래 API 오류 수정과 배포 경로 변경은 이 문서의 정확한 버전 승인을 받은 뒤 적용한다.

이 파일은 중앙 개발 계약이 요구하는 승인·인수조건 연결점이다. 운영 원장은 Obsidian `프로젝트/platform/`에 두며 보고서 본문을 이 저장소에 복제하지 않는다.

## 확인된 결함과 수정

격리 Firestore와 실제 HTTP에서 삭제 완료 계정으로 세션을 다시 열면 `EnsureUser`의 `auth_forbidden`이 `StoreRepository.EnsureUser`의 트랜잭션 오류 처리에서 `internal`로 바뀌어 HTTP 500이 된다. `/auth/session` 계약은 이 상태의 403을 이미 제공한다. 계정 재생성을 막는 저장 동작을 유지하면서 이 도메인 오류를 403으로 전달한다. 다른 저장 장애의 500 처리는 유지한다.

- 실패 근거: `TestServiceRuntimeDeletionRequestWorkerRetryAndReceipt`의 최종 재생성 거부 단계. 기존 실제 결과 500, 기대 403. 테스트 설정/예상값 오류와 구분한다.
- 재현 귀속: 기준 제품 소스 `f58999adf562c67f40a2e2d7d8e9faffc197a4f5`, 실행 `runtime-ninth`(2026-10-13 22:05 KST), 격리 `demo-platform-service-e2e`/loopback Firestore. 로컬 원시 결과 `/private/tmp/tc-service-e2e-results/runtime-ninth.log`, SHA-256 `55efd8a2df26bd711cf2e1c14bff3736c593c00cdedfafb2b041d3977a3b46c7`. 실행 테스트는 후속 구현 PR에 등록하고 중앙 보관함에서 공개 가능한 검사 이벤트만 연결한다.
- 사용자 영향: 영구 거부를 일시적인 서버 장애로 안내하거나 재시도할 수 있다.
- 데이터·공개 API 변경: 새 필드, 컬렉션, 마이그레이션 없이 기존 403 의미를 복원한다.

## 실행과 책임

1. `docs/qa/feature-inventory.json`에 서버 구현 기능 전체를 등록한다. HTTP API와 worker·저장·등록·라우팅 경계를 기존 코드와 대조한다.
2. 기존 Go 테스트 패키지에 실제 TCP HTTP와 격리 Firestore 실행을 추가한다. 계정·복구·우편·광고·구매·환불·관리·설정·콘텐츠·이벤트·접속·worker의 정상, 오류, 재시도, 저장, 재시작을 연결한다.
3. 외부 IAM/App Check/OIDC·마켓·GCS·BQ·GA4의 테스트용 경계는 명시한다. 서명·세션·제품 핸들러·실제 저장 트랜잭션은 제품 구현으로 확인한다. 실마켓 구매, 실제 모바일 광고·로그인·클라우드 앱 검증을 수행했다고 표시하지 않는다.
4. loopback 에뮬레이터와 `demo-` 프로젝트, 실행별 prefix만 사용한다. 운영 신원·사용자 데이터에 쓰지 않는다. 결과는 소스 밖의 테스트 이벤트·상태·코드·시간·digest만 담은 로그와 JSON에 저장한다. 요청/응답 전체, 인증·갱신·구매 토큰, 영수증, 실제 사용자 식별자는 기록하지 않는다. 외부 제공자와 사용자 자료는 합성값만 쓰며 보관 전 JWT·개인 키·알려진 비밀값 검사를 통과해야 한다. skip·미실행·실패를 통과로 만들지 않는다.
5. `Deploy`에서 이미지 빌드 전 서비스 E2E와 최신 중앙 검증을 실행한다. 정확한 후보 SHA의 근거를 `development-evidence` draft Release에 보관한다. production 배포 전 다시 읽어 검증하고 기존 required reviewer·6대상 이미지 readback을 유지한다. 정식 SDK v1.0.0 자산과 태그는 그대로 보존한다.

새 제품 패키지·계층·권한 모델은 도입하지 않는다. UI가 없는 서버이므로 GUI 회차는 0이며 모바일 앱의 기존 UI 검수 근거와 구분한다. CI는 public 저장소의 기존 `ubuntu-latest`를 사용한다. 에뮬레이터·도구는 버전과 원본을 확인하고 실행 뒤 자신의 프로세스와 격리 데이터만 정리한다.

## 인수조건과 검증

| ID | 조건 | 검증 |
|---|---|---|
| AC-DELETE-CODE | 삭제 완료 계정의 재생성은 403 `auth_forbidden`, 실제 저장 장애는 기존 오류 처리 | 현재 HTTP 실패 → 최소 수정 → HTTP/단위 회귀 |
| AC-SERVICE-RUNTIME | 구현 기능 전체의 정상·오류·저장·재시작 경로를 실제 서비스 개발 실행으로 확인 | HTTP 실행·Firestore 통합·외부 HTTP fixture 원시 결과와 기능 대조 |
| AC-EVIDENCE | source SHA·service 대상·실행 시각·설정 지문·격리 데이터·기능 목록이 일치하고 누락/skip이 없음 | 소스 밖 결과 JSON, 최신 중앙 local/원격 검증 |
| AC-RELEASE-GATE | 통과 근거 전 이미지 빌드/운영 배포가 열리지 않고 기존 승인을 유지 | workflow 계약 검사와 실제 Actions 상태 |

검증 도구·보고서 작업은 제품 동작을 바꾸지 않아 TDD 대체 근거로 초기 중앙 `DEVELOPMENT_EVIDENCE_ARCHIVE_MISSING` 실패, 누락 기능/skip/환경 경계 거부 검사, 실제 실행 결과를 연결한다. API 결함은 위 실패 재현을 TDD red로 사용한다. 과거 실패와 시간 초과 기록은 보존한다.

## 대안·운영·복구

단위 테스트·health만으로 전체 서비스 E2E를 대체하거나 중앙 검증을 생략하지 않는다. SDK 정식 Release는 immutable이며 QA 자산은 전용 draft 보관함에만 저장한다. 중앙 형식 변경, 기능 누락, 외부 접근 실패, 시간 초과가 발생하면 빌드/배포를 중단한다.

서버 배포는 승인된 새 후보의 기존 production 파이프라인으로 수행한다. API/IAP/ingest/admin/ads/worker 6개 대상의 동일 이미지 참조와 readiness를 다시 읽는다. 실패하면 파이프라인과 기존 이미지 근거를 보존하고 기존 운영 복구 절차를 따른다. 모바일 앱 운영 활성화·실기기 설치·마켓 업로드/심사는 별도 작업이다.

## 승인·추적

- 승인 요청 버전: `TC-SDK100-SERVICE-EVIDENCE-v1`
- 기준: platform `f58999adf562c67f40a2e2d7d8e9faffc197a4f5`, 중앙 `793b01669e3a4a3af0502559d86c596c6a3bf468`(실행 직전 최신 상태 재확인)
- 승인 전 상태: QA 실행 도구는 작성·검증 중, 제품 오류/Deploy 수정은 미적용.
- 승인 뒤 코드·설정·테스트·중앙 검증·Copilot Balanced 대체 리뷰를 완료한 Ready PR을 준비한다. 이번 보안·릴리스 변경은 교차 리뷰 대체 요청이므로 병합·운영 배포까지의 승인을 명시적으로 연결한다.
