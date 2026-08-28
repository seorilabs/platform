# ADR 0020 — Platform Fleet release를 불변 manifest로 배포한다

## 상태

승인

## 맥락

TypeScript SDK는 GitHub Packages로 발행하지만 GDScript SDK는 `main`의 파일을
복사하는 방식이었다. 소비자가 기록한 `SOURCE`도 floating branch를 가리켜 같은
버전이 시간이 지나면 다른 소스를 의미할 수 있었다. OpenAPI와 conformance vector가
함께 SDK 계약을 이루지만, 어느 계약 revision을 탑재했는지 기계가 판정할 공통
release 단위도 없었다.

개별 앱에 최신 SDK를 자동으로 반영하려면 중앙 producer가 먼저 버전, source SHA,
artifact digest, 계약 변경 성격과 영향 범위를 하나의 불변 입력으로 제공해야 한다.

## 결정

- `v<gdscript-version>` tag에서만 Platform Fleet GitHub Release를 만든다. tag는
  `sdk-gdscript/VERSION`과 정확히 같아야 한다.
- 같은 Release에 다음 세 asset을 원자적으로 올린 뒤 draft를 공개한다.
  - `seorilabs-platform-gdscript-<version>.tar.gz`
  - 위 파일의 `.sha256`
  - `platform-release.json`
- GDScript archive 안의 `SOURCE`는 해당 Release asset의 고정 URL이다. 새 release는
  `main`, branch, `latest` URL을 provenance로 만들지 않는다.
- `platform-release.json`은 생성 시각을 넣지 않고 다음을 고정한다.
  - release tag, source SHA, base source SHA
  - TypeScript와 GDScript 버전 및 artifact SHA-256
  - OpenAPI와 `spec/conformance/*.json`을 합친 현재/base contract revision
  - `implementation-only`, `contract-additive`, `contract-breaking` 분류
  - 영향받는 SDK track, capability, 지원 API major
- OpenAPI 분류는 checksum으로 고정한 `oasdiff`를 사용한다. conformance는 기존
  구조와 값이 모두 남은 추가만 additive로 보고, 제거·변경은 breaking으로 본다.
  도구 출력이나 JSON을 해석할 수 없으면 분류를 추측하지 않고 release를 중단한다.
- 계약 변경은 TypeScript와 GDScript 두 track 모두에 영향을 준다. 구현 변경은 실제
  변경 경로와 이번에 발행한 GDScript track을 기록한다. 첫 Fleet release처럼 비교할
  이전 GDScript Release가 없으면 `gdscript`와 `core`를 최소 영향으로 남긴다.
- 기존 TypeScript GitHub Packages 발행은 유지하되 같은 generator를 dry-run하여
  두 발행 경로가 하나의 release 계약을 검증하게 한다.
- producer는 consumer 이슈 생성, SDK 자동 반영, platform 배포를 수행하지 않는다.
  그 단계는 이 manifest를 입력으로 받는 별도 Fleet controller의 책임이다.

## 결과

- 앱은 version 문자열 대신 source SHA와 digest가 고정된 SDK release를 소비할 수 있다.
- 같은 입력은 byte-identical GDScript archive와 manifest를 만든다.
- 공개된 Release asset이 없거나 다르면 publisher는 덮어쓰지 않고 실패한다.
- 첫 Fleet release 이전까지 기존 source-copy 방식은 개발 편의용으로만 남는다.
- 계약 변경 후 소비자 PR과 P1 이슈 fan-out은 후속 controller가 구현해야 한다.

## 검토한 대안

- `main` archive를 계속 사용: 버전과 실제 source가 고정되지 않아 배제한다.
- GDScript를 별도 저장소에 복제: 원본과 계약 revision이 다시 갈라지므로 배제한다.
- 분류가 불명확할 때 additive로 간주: 구버전 앱을 깨뜨릴 수 있어 배제한다.
