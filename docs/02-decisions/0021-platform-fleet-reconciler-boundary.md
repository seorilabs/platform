# ADR 0021 — Platform Fleet fan-out은 서명된 dry-run 계획에서 시작한다

## 상태

승인

## 맥락

ADR 0020은 SDK asset과 `platform-release.json`을 불변 release로 만들지만, 어느
release를 조직 전체에 적용할지와 앱 저장소별 후속 작업은 결정하지 않는다. manifest를
읽었다는 이유만으로 GitHub PR·Issue를 바로 만들면 미승인 release, 오래된 Backoffice
관찰값, 중복 webhook이 조직 전체에 쓰기를 전파할 수 있다.

Platform Fleet는 계약 변경이 없는 release를 exact SDK update로 배포하고, 계약 변경은
영향 저장소의 P1 적응 작업으로 분리해야 한다. 동시에 release build는 승인 버전과 실제
탑재 버전이 다르면 차단하고, 임시 예외도 대상 manifest와 만료 시각에 묶어야 한다.

## 결정

- `scripts/platform-fleet-reconciler.mjs`는 다음 입력만으로 결정론적 dry-run 계획을 만든다.
  - 발행된 `platform-release.json` 원문 byte
  - 그 byte digest, source SHA, tag에 묶인 Ed25519 `fleet-approved` 서명
  - Backoffice 또는 test fixture에서 온 expected consumer와 source-SHA 관찰값
  - 기존 PR·Issue의 idempotency readback
- 신뢰 키는 호출자가 명시적인 `Map`으로 주입한다. 저장소에는 private key, 기본 key,
  서명 생성 CLI를 두지 않는다. 서명이 없거나 key가 미등록이면 계획 자체를 만들지 않는다.
- manifest schema, 고정 GitHub Release URL, SDK version·artifact digest·tree checksum,
  contract revision·classification·affected track을 모두 검증한다. 알 수 없는 필드나 값은
  다음 schema가 구현되기 전까지 fail-closed한다.
- consumer별 expected track을 아래 상태로 분류한다.
  - `managed`: official SDK의 version, artifact digest, contract revision과 GDScript
    provenance가 명시됨
  - `custom`: 같은 track의 custom HTTP 구현이 탐지됨
  - `unmanaged`: 공식 provenance 없이 vendoring 또는 dependency가 탐지됨
  - `missing`: 증거가 없음
  - `ambiguous`: 한 track에 둘 이상의 상충 증거가 있음
- `implementation-only`이고 managed SDK가 뒤처졌으면 repo별 exact update PR 계획을
  하나만 만든다. TypeScript와 GDScript가 함께 뒤처져도 하나의 PR intent로 합친다.
- `contract-additive` 또는 `contract-breaking`이면 repo별 P1 Issue 계획을 하나만 만들고
  라벨을 `P1`, `autopilot`, `platform`, `platform-contract`로 고정한다. SDK 자동 PR과
  계약 적응 Issue를 한 action으로 합치지 않는다.
- action intent digest를 idempotency key로 쓴다. 같은 key의 기존 work item readback이
  있으면 상태와 무관하게 다시 계획하지 않는다. 같은 key의 내용이 다르면 충돌로 중단한다.
  intent는 consumer repository ID·source SHA·ACTIVE config revision·observation digest와
  Platform release source SHA·manifest digest를 함께 고정한다.
- repo별 자율 PR concurrency key를 별도로 둔다. 다른 release의 SDK PR이 이미 열려 있으면
  두 번째 PR을 만들지 않고 현재 intent를 `deferred`로 남긴다.
- release gate는 승인 manifest의 version 문자열뿐 아니라 artifact·contract provenance가
  모두 같아야 통과한다. stale SDK는 차단하며 예외는 `release-build`, 현재 manifest digest,
  `ACTIVE`, 미래의 UTC 만료 시각이 모두 맞을 때만 `EXCEPTION`으로 통과한다. custom,
  unmanaged, missing, ambiguous 상태는 예외로 우회하지 않는다.
- 동일 repo의 서로 다른 관찰값이나 관찰 누락은 최신값을 추측하지 않고 `needs_input`으로
  남긴다. `needs_input`이 하나라도 있는 plan은 부분 적용하지 않는다.
- `executeFleetPlan`의 기본값은 dry-run이다. write를 명시해도 다음을 모두 충족해야 한다.
  1. release 서명과 consumer observation을 다시 검증한 plan digest가 dry-run과 일치
  2. 모든 관찰 source가 `backoffice`
  3. action 직전 adapter readback의 manifest·observation·plan digest 일치
  4. adapter write 뒤 idempotency key·action digest·repository가 일치하는 persisted readback
- core에는 GitHub SDK, HTTP client, token, provider mutation이 없다. 실제 adapter는
  Backoffice 배포 단계에서 별도로 구현하며, readback 없는 write adapter는 계약을 만족하지
  못한다.

## 결과

- 중복 webhook과 반복 실행은 같은 PR·Issue intent 하나로 수렴한다.
- 승인 release와 정확한 consumer 관찰값이 없으면 조직 fan-out이 시작되지 않는다.
- SDK 최신 여부, custom/unmanaged 탐지, 예외 만료가 하나의 release gate 결과로 남는다.
- 현재 PR은 순수 계획 코어와 fixture 검증까지만 제공한다. Fleet approval signer, trusted
  public-key 배포, Backoffice adapter, GitHub App write, 실제 release blocker 연결은 별도
  배포 gate다.

## 검토한 대안

- GitHub Release 공개 상태만 승인으로 사용: publisher 권한과 Fleet 승인 권한이 합쳐져
  배제한다.
- manifest object를 재직렬화해 서명: 발행된 실제 asset byte가 아닌 다른 표현을 승인할 수
  있어 배제한다.
- 여러 관찰값에서 가장 최근 시각 선택: clock과 ingestion 순서가 실행 의미를 바꿔 배제한다.
- stale 예외를 repo나 version 문자열에만 묶기: 같은 버전의 다른 artifact를 허용할 수 있어
  manifest digest에 고정한다.
