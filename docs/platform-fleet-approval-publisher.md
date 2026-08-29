# Platform Fleet 승인 게시 계약

`fleet-approved.json`은 Platform SDK release를 fleet 전체에 적용해도 된다는 공개 서명 증거다.
승인 서명과 GitHub Release 게시 권한은 서로 분리한다.

## 실행 순서

1. canary readback을 검증한 signer가 `sign-platform-fleet-release.mjs`로 승인 파일을 만든다.
2. base publisher가 네 asset을 GitHub draft release에 올리고 공개하지 않은 채 종료한다.
3. trusted mutation adapter가 다음 세 공개 파일을 일반 파일로 전달한다.
   - 발행된 `platform-release.json`
   - 서명된 `fleet-approved.json`
   - 조직 정책 observer가 같은 pinned key로 서명한 `platform-policy-attestation.json`
4. adapter는 broker가 1회용으로 소진해야 하는 grant와 GitHub App token을 서로 다른
   상속 FD에만 주입한다.
   공개키 registry는 저장소에 고정된 파일과 publisher의 SHA-256 pin을 함께 사용한다.

```bash
node scripts/publish-platform-fleet-approval.mjs \
  --manifest /trusted-input/platform-release.json \
  --approval /trusted-input/fleet-approved.json \
  --policy-attestation /trusted-input/platform-policy-attestation.json \
  --grant-fd 3 \
  --token-fd 4
```

실행 환경의 `GITHUB_REPOSITORY`는 `seorilabs/platform`이어야 한다. token과 grant는
argv, 일반 파일, 환경변수, stdout에 전달하지 않는다. grant는 repository, release tag,
source SHA, manifest·approval·trusted registry digest, approval key ID, 1회 사용과 5분 이하
TTL을 결합한다.

## 불변 조건

- adapter가 전달하는 세 파일과 publisher 내부의 tracked registry는 symbolic link가 아닌
  1MiB 이하 일반 파일이어야 한다.
- approval의 Ed25519 서명은 tracked registry와 publisher에 함께 고정된 ACTIVE 공개 키로
  검증되어야 한다. worker나 CLI caller는 registry 경로를 선택할 수 없다.
- repository readback의 immutable releases 설정은 `enabled=true`와
  `enforced_by_owner=true`여야 한다.
- policy attestation은 repository numeric ID, release tag, source SHA, approval digest,
  immutable 설정, 조직 ruleset ID·revision·전체 규칙을 묶고 5분 안에 만료되어야 한다.
  approval과 같은 pinned ACTIVE key의 Ed25519 서명이 없으면 거부한다.
- repository에 실제 적용되는 ruleset 목록은 attestation과 같은 ID·`updated_at`을
  가리켜야 하며 `source_type=Organization`, `source=seorilabs`, active여야 한다.
  repository 소유 ruleset은 조직 정책 증거로 인정하지 않는다.
- release tag가 가리키는 commit은 승인 payload의 exact source SHA여야 한다.
- 최초 release는 exact source의 non-prerelease draft여야 한다.
- `platform-release.json`과 TypeScript, GDScript, checksum asset 네 개를 모두 내려받아
  size, SHA-256, checksum 원문을 검증한다.
- 허용되지 않은 asset이나 중복 이름이 하나라도 있으면 중단한다.
- 기존 `fleet-approved.json`이 동일 byte이면 성공으로 끝내고, 다르면 삭제하거나
  덮어쓰지 않는다.
- 승인 파일이 없을 때만 draft에 한 번 upload하고, 전체 release와 보호 정책을 다시
  readback한다.
- 동시 upload가 422로 충돌하면 기존 파일이 동일 byte일 때만 성공으로 인정한다.
- tag·ruleset·setting·다섯 asset이 그대로일 때만 draft를 공개하며, 최종
  `immutable=true`와 tag commit, 다섯 asset을 다시 검증한다.
- draft 공개 요청은 `make_latest=true`를 명시한다. 게시 후 `GET /releases/latest`가 같은
  release ID, tag, source SHA와 다섯 asset identity를 반환해야만 `fleet-approved latest`로
  인정한다. GitHub의 latest 표시는 단독 승인 근거가 아니며 `fleet-approved.json`이 없으면
  fail-closed한다.
- asset download redirect는 GitHub release asset host allowlist만 token 없이 따르고,
  signed size를 넘는 streaming response는 즉시 중단한다.
- GitHub 오류 응답 body는 로그나 예외에 포함하지 않는다.

GitHub는 조직 ruleset의 `bypass_actors`를 ruleset write 권한 보유자에게만 반환한다.
따라서 publisher token에 조직 정책 변경 권한을 주지 않는다. 별도 policy observer가
bypass 없음과 상세 규칙을 읽고 attestation을 서명하며, publisher는 read-only repository
identity·immutable setting·inherited ruleset readback과 서명 원문을 대조한다.

raw JSON을 `workflow_dispatch` 입력으로 받거나 공개된 release에 asset을 뒤늦게 붙이거나
사람이 GitHub UI에서 asset을 교체하는 경로는 이 계약에 포함되지 않는다.

## 변경 분류와 Fleet 동작

- `implementation-only`는 계약 호환 release다. 뒤처진 official SDK를 exact version·digest로
  올리는 PR intent를 repo별 하나만 만든다.
- `contract-additive`와 `contract-breaking`은 계약 변경 release다. 두 SDK track의 영향을
  고정하고 `P1`, `autopilot`, `platform`, `platform-contract` Issue intent를 repo별 하나만
  만든다.
- GitHub latest와 signed approval readback이 맞아도 Backoffice의 현재 latest generation CAS와
  consumer observation 재확인이 끝나기 전에는 PR이나 Issue를 생성하지 않는다.

## 남은 P6 broker gate

publisher는 grant의 exact digest, TTL, `maxUses=1`을 검증한다. `grantId`의 durable CAS 소비와
재사용 거부는 P6 Auth Broker 계약에서 확정해야 한다. 그 계약이 연결되기 전에는 이 필드만으로
1회 사용이 증명되었다고 간주하지 않는다.
