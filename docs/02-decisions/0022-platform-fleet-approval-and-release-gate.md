# ADR 0022 — Fleet 승인은 broker FD로 서명하고 release build는 receipt로 차단한다

## 상태

승인

## 맥락

ADR 0021은 서명된 dry-run 계획을 정의했지만 실제 승인 서명 생성과 앱 release build의
실행 계약은 의도적으로 남겨 두었다. private key를 repository secret이나 agent 환경변수로
전달하면 승인 주체와 producer가 다시 합쳐지고, stdout·환경 dump·child process에 key가
노출될 수 있다. 반대로 version 문자열만 검사하면 같은 버전의 다른 artifact나 오래된
Backoffice 설정으로도 release build가 진행될 수 있다.

## 결정

- `sign-platform-fleet-release.mjs`는 private key 경로, 환경변수, stdin을 받지 않는다.
  Auth Broker가 열어 준 3 이상의 inherited file descriptor에서만 Ed25519 private key를
  읽고, 사용 직후 입력 buffer를 덮어쓴다.
- signer 실행 경계는 Auth Broker native launcher가 `RLIMIT_CORE=0`과 non-dumpable을
  강제해야 한다. 이 조건과 purpose-specific logical credential이 없으면 실제 승인을
  만들지 않는다.
- signer는 agent가 만든 payload를 받지 않는다. 발행된 `platform-release.json` 원문
  byte에서 reconciler가 계산한 canonical approval payload만 서명한다.
- trusted key registry에는 public key와 `ACTIVE`/`REVOKED` 상태만 둔다. 기본 key,
  wildcard key 또는 저장소 내 private key는 두지 않는다. 같은 key ID가 중복되거나
  ACTIVE key가 없으면 fail-closed한다.
- release build는 Backoffice가 정확히 한 repository ID에 대해 반환한 ACTIVE observation,
  source SHA, config revision과 signed manifest를 다시 검증한다.
- gate receipt는 repository ID/full name, source SHA, config revision, manifest/source/contract
  digest, SDK version·artifact·tree·source binding, plan/observation digest를 고정한다.
  `PASS` 또는 유효한 manifest-bound `EXCEPTION`만 receipt를 만들 수 있다.
- fixture, stale SDK, custom/unmanaged/missing SDK, identity mismatch와 복수 observation은
  receipt를 만들지 못한다. static PR check는 shadow plan을 관찰할 수 있지만 release
  build는 receipt가 없으면 시작하지 않는다.

## 결과

- producer workflow와 Fleet approver가 같은 GitHub token을 공유하지 않는다.
- private key는 argv, 환경변수, repository file, stdout으로 전달되지 않는다.
- release build가 어떤 source/config/SDK manifest에 대해 허용됐는지 receipt 하나로
  재구성할 수 있다.
- 실제 key 생성·등록, Auth Broker 실행 복제본, Fleet 승인과 consumer 적용은 각각 별도
  운영 승인과 readback이 필요하다.

## ADR 0021과의 관계

ADR 0021의 "저장소에 서명 생성 CLI를 두지 않는다"는 결정을 이 ADR이 좁게 대체한다.
private key를 해석하거나 export하는 일반 CLI는 계속 금지하며, broker FD 전용 signer만
허용한다.
