# Platform SDK release

Platform Fleet release는 앱 저장소가 자동 갱신에 사용할 불변 SDK 입력이다. 이
절차는 SDK asset을 발행할 뿐 platform 서비스 배포나 소비자 저장소 변경을 하지 않는다.

## Release 계약

`v<gdscript-version>` GitHub Release에는 정확히 다음 asset이 있다.

| asset | 역할 |
|---|---|
| `seorilabs-platform-gdscript-<version>.tar.gz` | `seorilabs_platform/` addon의 고정 배포본 |
| 위 파일의 `.sha256` | 다운로드 byte 검증 |
| `platform-release.json` | SDK와 계약 revision의 machine-readable 원장 |

manifest의 핵심 필드는 다음과 같다.

```json
{
  "schemaVersion": 1,
  "release": {
    "tag": "v0.6.5",
    "sourceSha": "<40자리 SHA>",
    "baseSourceSha": "<이전 SDK release SHA>"
  },
  "sdk": {
    "typescript": { "version": "0.4.0", "artifact": { "sha256": "<SHA-256>" } },
    "gdscript": { "version": "0.6.5", "artifact": { "sha256": "<SHA-256>" } }
  },
  "contract": {
    "revision": "sha256:<OpenAPI와 conformance 통합 hash>",
    "baseRevision": "sha256:<이전 통합 hash>",
    "classification": "implementation-only",
    "supportedApiMajor": 1,
    "affectedTracks": ["gdscript"],
    "affectedCapabilities": ["presence"]
  }
}
```

생성 시각은 넣지 않는다. source SHA, base SHA, TypeScript npm tarball이 같으면
manifest와 GDScript archive도 byte-identical해야 한다.
첫 Fleet release도 실제로 발행한 `gdscript` track과 `core` capability를 남긴다.

## 계약 분류

- `implementation-only`: `oasdiff`가 OpenAPI 의미 변경을 보고하지 않고
  conformance 규약도 추가·변경되지 않았다.
- `contract-additive`: OpenAPI 항목이나 conformance 규약이 기존 계약을 보존한 채
  추가되었다.
- `contract-breaking`: 지원 API major 변경, OpenAPI breaking change 또는 기존
  conformance 구조·값의 제거와 변경이 있다.

`oasdiff` 실행 실패, 알 수 없는 출력, 잘못된 conformance JSON은 release 실패다.
분류 실패를 `implementation-only`나 `contract-additive`로 낮추지 않는다.

## 검증과 발행

PR과 `main`의 `Checks (Platform Release)`는 패키지 test/typecheck/build 후 같은
입력으로 release를 두 번 생성하고 디렉터리 byte를 비교한다. TypeScript SDK tag도
GitHub Packages에 올리기 전에 같은 generator를 dry-run한다.

GDScript release가 필요할 때 `sdk-gdscript/VERSION`과 같은 `vX.Y.Z` tag를 검증된
`main` commit에 만든다. `Publish GDScript SDK`가 다음 순서로 처리한다.

1. GDScript 검증과 전체 package test/typecheck/build
2. checksum 고정 `oasdiff` 설치
3. npm tarball과 GDScript archive, manifest 생성
4. draft GitHub Release에 세 asset 업로드
5. 기존 asset과 digest가 모두 맞을 때만 Release 공개

공개 Release는 immutable이다. 같은 이름의 asset이 다르거나 asset이 빠진 공개
Release를 발견해도 자동으로 덮어쓰지 않는다.

로컬 dry-run은 실제 Release를 만들지 않는다.

```bash
pnpm install --frozen-lockfile
pnpm test && pnpm typecheck && pnpm build
node scripts/install-oasdiff.mjs /tmp/oasdiff
typescript_artifact="/tmp/$(npm pack ./packages/sdk-ts --pack-destination /tmp --silent)"
node scripts/build-platform-release.mjs \
  --release-tag "v$(tr -d '[:space:]' < sdk-gdscript/VERSION)" \
  --source-sha "$(git rev-parse HEAD)" \
  --typescript-artifact "$typescript_artifact" \
  --oasdiff /tmp/oasdiff \
  --output-dir /tmp/platform-release
```
