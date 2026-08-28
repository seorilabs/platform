# Platform Fleet 승인 게시 계약

`fleet-approved.json`은 Platform SDK release를 fleet 전체에 적용해도 된다는 공개 서명 증거다.
승인 서명과 GitHub Release 게시 권한은 서로 분리한다.

## 실행 순서

1. canary readback을 검증한 signer가 `sign-platform-fleet-release.mjs`로 승인 파일을 만든다.
2. trusted mutation adapter가 다음 세 공개 파일을 일반 파일로 전달한다.
   - 발행된 `platform-release.json`
   - 서명된 `fleet-approved.json`
   - ACTIVE 공개 키만 포함한 trusted release key registry
3. adapter는 GitHub contents-write token을 전용 child process 환경에만 주입하고 아래 명령을 실행한다.

```bash
node scripts/publish-platform-fleet-approval.mjs \
  --manifest /trusted-input/platform-release.json \
  --approval /trusted-input/fleet-approved.json \
  --trusted-keys /trusted-input/trusted-release-keys.json
```

실행 환경의 `GITHUB_REPOSITORY`는 `seorilabs/platform`, `GITHUB_API_URL`은
`https://api.github.com`이어야 한다. token은 argv, 파일, stdout에 전달하지 않는다.

## 불변 조건

- 입력 세 파일은 symbolic link가 아닌 1MiB 이하 일반 파일이어야 한다.
- approval의 Ed25519 서명은 ACTIVE 공개 키로 검증되어야 한다.
- release tag가 가리키는 commit은 승인 payload의 exact source SHA여야 한다.
- release는 이미 공개된 non-draft, non-prerelease 상태여야 한다.
- `platform-release.json`과 TypeScript, GDScript, checksum asset 네 개를 모두 내려받아
  size, SHA-256, checksum 원문을 검증한다.
- 허용되지 않은 asset이나 중복 이름이 하나라도 있으면 중단한다.
- 기존 `fleet-approved.json`이 동일 byte이면 성공으로 끝내고, 다르면 삭제하거나
  덮어쓰지 않는다.
- 승인 파일이 없을 때만 한 번 upload하고, 전체 release와 approval을 다시 readback한다.
- 동시 upload가 422로 충돌하면 기존 파일이 동일 byte일 때만 성공으로 인정한다.

raw JSON을 `workflow_dispatch` 입력으로 받거나 사람이 GitHub UI에서 asset을 교체하는
경로는 이 계약에 포함되지 않는다.
