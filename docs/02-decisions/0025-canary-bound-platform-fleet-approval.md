# ADR 0025 — Fleet approval은 RN·Godot canary readback에 묶는다

## 상태

승인

## 맥락

ADR 0022의 approval v1은 발행된 `platform-release.json` 원문 byte와 Ed25519
approval key를 결합했지만, 해당 release가 실제 RN과 Godot consumer에서 검증됐다는
증거는 approval payload에 없었다. 따라서 broker가 private key를 보호하더라도 잘못된
호출이 canary를 통과하지 않은 release를 `fleet-approved`로 서명할 여지가 있었다.

WorkflowBundle candidate fixture 성공도 실제 consumer build 성공이 아니다. Fleet 최신
승격은 RN·Godot 각각의 독립 static run과 Android build-only run, exact source SHA,
Cloud Build provenance와 AAB checksum을 trusted GitHub readback으로 확인한 뒤에만 가능해야
한다.

## 결정

- Fleet approval envelope를 schema v2와 purpose
  `seorilabs-platform-fleet-approved-release-v2`로 올린다. v1 approval은 더 이상
  reconciler나 release gate가 허용하지 않는다.
- signer는 다음 두 public input도 path, argv payload, 환경변수나 stdin으로 받지 않고
  Auth Broker가 연결한 서로 다른 inherited FD로만 받는다.
  - trusted GitHub readback adapter가 Ed25519로 서명한 canary evidence
  - ACTIVE canary readback public-key registry
- canary evidence는 발행된 manifest SHA-256, Platform source SHA와 tag에 정확히 묶인다.
- evidence에는 `godot`, `react-native` profile이 정확히 하나씩 있어야 하고 각 profile은
  다음 값을 모두 고정한다.
  - repository numeric ID, full name, app source SHA
  - 독립 static run ID와 `success`, app/workflow source SHA
  - 독립 build-only run ID와 `success`, app/workflow source SHA
  - Cloud Build ID, builder image digest, build config digest
  - 단일 AAB 이름, byte size, SHA-256
- 두 run이 같은 ID이거나, profile·repository가 중복되거나, conclusion·source SHA·artifact
  필드가 없거나 다르면 signer는 approval을 만들지 않는다.
- approval payload는 검증한 canary attestation digest, readback key ID, WorkflowBundle
  source/digest와 두 canary 원문을 포함한다. release gate receipt도 canary attestation과
  WorkflowBundle binding을 남긴다.
- canary evidence signer는 Platform 저장소가 제공하지 않는다. `.github`의 trusted
  GitHub readback adapter만 공식 API readback으로 evidence를 만들 수 있다.
- signed approval은 승격 자격 증거다. 어느 release를 `fleet-approved latest`로 가리킬지는
  Backoffice가 현재 latest readback과 CAS를 수행한 뒤 갱신하며, candidate나 실패한 canary를
  수동으로 latest 처리하지 않는다.

## 결과

- fixture 성공, 부분 canary, 실패한 build-only run만으로 Fleet 승인을 만들 수 없다.
- approval consumer는 원 release, canary artifact와 WorkflowBundle exact source를 하나의
  서명된 payload에서 독립적으로 readback할 수 있다.
- 현재 approval v1 consumer와 signer invocation은 v2 evidence 계약으로 함께 갱신해야 한다.
- 중앙 WIF identity, canary caller 또는 trusted readback adapter가 준비되지 않으면 승격은
  fail-closed하며 기존 공개 release asset 자체는 변경하지 않는다.

## ADR 0022와의 관계

private key 비노출, broker FD, core dump 차단과 manifest 원문 서명 원칙은 유지한다. 이 ADR은
approval schema와 signer 입력 계약에 canary readback 불변식을 추가한다.

## 검토한 대안

- GitHub Actions check conclusion 문자열만 입력: repository/source/workflow/artifact binding이
  없어 배제한다.
- candidate fixture를 canary로 인정: 실제 앱 source와 AAB가 실행되지 않아 배제한다.
- signer가 GitHub API를 직접 호출: approval key와 provider token 경계가 합쳐져 배제한다.
- canary evidence path 또는 환경변수 입력: agent가 교체할 수 있는 입력면이 생겨 배제한다.
