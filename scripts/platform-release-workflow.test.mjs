import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it } from 'node:test';

import {
  OASDIFF_DARWIN_ALL_SHA256,
  OASDIFF_LINUX_AMD64_SHA256,
  OASDIFF_LINUX_ARM64_SHA256,
  OASDIFF_VERSION,
} from './install-oasdiff.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const workflow = (name) => readFile(resolve(root, '.github/workflows', name), 'utf8');
const ACTION_SHA = '[0-9a-f]{40}';

describe('Platform release workflow 계약', () => {
  it('GDScript 발행 job은 npm 스코프 인증을 설정하지 않는다', async () => {
    const source = await workflow('publish-sdk-gdscript.yml');
    // 이 job 은 npm 에 발행하지 않고 이미 공개된 패키지를 읽기만 한다.
    // setup-node 에 registry-url/scope 를 주면 NODE_AUTH_TOKEN 을 요구하는
    // .npmrc 가 생겨 공개 패키지 조회가 401 로 실패한다.
    assert.doesNotMatch(source, /registry-url:/u);
    assert.doesNotMatch(source, /scope: "@seorilabs"/u);
    assert.doesNotMatch(source, /NODE_AUTH_TOKEN/u);
    // 실제 npm 발행 workflow 는 그대로 인증을 유지해야 한다.
    const publisher = await workflow('publish-sdk-ts.yml');
    assert.match(publisher, /registry-url: https:\/\/registry\.npmjs\.org/u);
  });

  it('GDScript asset 발행은 version tag에서만 실행되고 배포 명령을 포함하지 않는다', async () => {
    const source = await workflow('publish-sdk-gdscript.yml');
    assert.match(source, /tags:\s*\n\s+- "v\*\.\*\.\*"/u);
    assert.doesNotMatch(source, /workflow_dispatch/u);
    assert.match(source, /permissions:\s*\n\s+contents: read/u);
    assert.match(source, /publish:[\s\S]*permissions:\s*\n\s+contents: write/u);
    assert.match(source, new RegExp(`uses: actions/checkout@${ACTION_SHA}`, 'u'));
    assert.match(source, new RegExp(`uses: actions/setup-node@${ACTION_SHA}`, 'u'));
    // 불변 release를 만드는 job은 self-hosted 러너에서 돌리지 않는다.
    // ARC는 집 RPI 클러스터에 있어서 그 호스트가 곧 release 산출물의
    // 신뢰 기반이 된다. GitHub-hosted로 고정한다.
    assert.match(source, /runs-on: ubuntu-latest/u);
    assert.doesNotMatch(source, /runs-on: seorilabs-/u);
    assert.doesNotMatch(source, /runs_on: seorilabs-/u);
    assert.match(source, /build-platform-release\.mjs/u);
    assert.match(source, /resolve-platform-release-base\.mjs/u);
    assert.match(source, /typescript-registry-artifact\.mjs fetch/u);
    // 공개 npm registry에서 받으므로 packages 권한과 registry 토큰이 없어야 한다.
    assert.doesNotMatch(source, /packages: read/u);
    assert.doesNotMatch(source, /NODE_AUTH_TOKEN/u);
    assert.match(source, /--typescript-registry-integrity/u);
    assert.doesNotMatch(source, /npm pack \.\/packages\/sdk-ts/u);
    assert.match(source, /publish-platform-release\.mjs/u);
    assert.match(source, /needs: sdk/u);
    assert.doesNotMatch(source, /\b(?:gcloud|kubectl|firebase)\b/u);
  });

  it('vX.Y.Z tag는 별도 승인 없이 GitHub Release를 latest로 공개한다', async () => {
    const [workflowSource, publisherSource] = await Promise.all([
      workflow('publish-sdk-gdscript.yml'),
      readFile(resolve(root, 'scripts/publish-platform-release.mjs'), 'utf8'),
    ]);
    assert.match(workflowSource, /name: Platform SDK GitHub Release 공개/u);
    assert.match(workflowSource, /name: Publish immutable GitHub Release/u);
    assert.match(publisherSource, /JSON\.stringify\(\{ draft: false, make_latest: 'true' \}\)/u);
    // by-tag 조회는 draft를 돌려주지 않아 재실행 때 draft를 중복 생성한다.
    assert.doesNotMatch(publisherSource, /releases\/tags\/\$\{/u);
  });

  it('SDK release 경로에 은퇴한 승인 체계가 남지 않는다', async () => {
    const files = [
      '.github/workflows/checks-platform-release.yml',
      '.github/workflows/publish-sdk-gdscript.yml',
      '.github/workflows/publish-sdk-ts.yml',
      'scripts/build-platform-release.mjs',
      'scripts/platform-release-lib.mjs',
      'scripts/publish-platform-release.mjs',
      'scripts/resolve-platform-release-base.mjs',
      'README.md',
    ];
    for (const name of files) {
      const source = await readFile(resolve(root, name), 'utf8');
      assert.doesNotMatch(source, /fleet|canary/iu, name);
    }
  });

  it('PR gate는 generator를 두 번 실행해 byte 차이를 검사한다', async () => {
    const source = await workflow('checks-platform-release.yml');
    assert.match(source, /for output in first second/u);
    assert.match(source, /diff -rq/u);
    assert.match(source, /resolve-platform-release-base\.mjs/u);
    assert.match(source, /--base-ref "\$\{\{ steps\.release-base\.outputs\.sha \}\}"/u);
    assert.match(source, /--typescript-registry-integrity "\$typescript_integrity"/u);
    assert.match(source, /fetch-depth: 0/u);
    assert.match(source, new RegExp(`uses: actions/checkout@${ACTION_SHA}`, 'u'));
    assert.match(source, new RegExp(`uses: actions/setup-node@${ACTION_SHA}`, 'u'));
  });

  it('기존 TypeScript publish도 공통 generator를 검증한다', async () => {
    const source = await workflow('publish-sdk-ts.yml');
    assert.match(source, /sdk-ts-v\*\.\*\.\*/u);
    assert.match(source, /build-platform-release\.mjs/u);
    assert.match(source, /install-oasdiff\.mjs/u);
    assert.match(source, /resolve-platform-release-base\.mjs/u);
    assert.match(source, /typescript-registry-artifact\.mjs integrity/u);
    assert.match(source, /--typescript-registry-integrity/u);
    assert.match(source, /fetch-depth: 0/u);
    assert.match(source, /npm publish "\$\{\{ steps\.pack\.outputs\.tarball \}\}"/u);
  });

  it('release builder는 tag 추론 없이 resolver가 고른 직전 공개 release만 base로 받는다', async () => {
    const builderSource = await readFile(resolve(root, 'scripts/build-platform-release.mjs'), 'utf8');
    assert.doesNotMatch(builderSource, /git['"], \['describe'/u);
    assert.match(builderSource, /'--base-ref'/u);
    assert.match(builderSource, /직전 공개 release의 base revision/u);
    // 지금 만드는 tag를 resolver에 넘겨야 같은 tag의 재실행이 자기 release를 base로 삼지 않는다.
    for (const name of ['checks-platform-release.yml', 'publish-sdk-gdscript.yml', 'publish-sdk-ts.yml']) {
      const source = await workflow(name);
      assert.match(source, /resolve-platform-release-base\.mjs "\$(?:release_tag|GITHUB_REF_NAME)"/u, name);
    }
  });

  it('tracked GDScript SOURCE는 현재 VERSION의 immutable Release asset을 가리킨다', async () => {
    const [versionText, sourceText, vendorScript] = await Promise.all([
      readFile(resolve(root, 'sdk-gdscript/VERSION'), 'utf8'),
      readFile(resolve(root, 'sdk-gdscript/addons/seorilabs_platform/SOURCE'), 'utf8'),
      readFile(resolve(root, 'scripts/vendor_sdk_gdscript.sh'), 'utf8'),
    ]);
    const version = versionText.trim();
    assert.equal(
      sourceText.trim(),
      `https://github.com/seorilabs/platform/releases/download/v${version}/seorilabs-platform-gdscript-${version}.tar.gz`,
    );
    assert.doesNotMatch(sourceText, /\/tree\/(?:main|master)\//u);
    assert.match(vendorScript, /git -C "\$repo_root" rev-parse HEAD/u);
    assert.doesNotMatch(vendorScript, /\/tree\/(?:main|master)\//u);
  });

  it('oasdiff 버전과 모든 허용 실행환경 archive digest를 고정한다', () => {
    assert.equal(OASDIFF_VERSION, '1.29.1');
    for (const digest of [
      OASDIFF_LINUX_ARM64_SHA256,
      OASDIFF_LINUX_AMD64_SHA256,
      OASDIFF_DARWIN_ALL_SHA256,
    ]) {
      assert.match(digest, /^[0-9a-f]{64}$/u);
    }
  });

  it('모든 workflow action과 재사용 workflow를 full SHA로 고정한다', async () => {
    const names = [
      'checks-go.yml',
      'checks-platform-release.yml',
      'checks-sdk.yml',
      'deploy-staging.yml',
      'deploy.yml',
      'presence-edge.yml',
      'publish-sdk-gdscript.yml',
      'publish-sdk-ts.yml',
    ];
    for (const name of names) {
      const source = await workflow(name);
      for (const match of source.matchAll(/uses:\s*([^@\s]+)@([^\s#]+)/gu)) {
        // 조직 중앙 재사용 워크플로(seorilabs/.github)는 SHA 대신 main을 따른다(#156). 중앙에서 한 번
        // 고치면 전 저장소에 반영되고, 되돌릴 때도 중앙 revert 한 번으로 끝나게 하려는 조직 결정이다.
        // 그 밖의 action과 재사용 workflow는 계속 full SHA로 고정한다.
        if (match[1].startsWith('seorilabs/.github/.github/workflows/') && match[2] === 'main') {
          continue;
        }
        assert.match(match[2], /^[0-9a-f]{40}$/u, `${name}: ${match[0]}`);
      }
      assert.doesNotMatch(source, /secrets:\s*inherit/u, name);
    }
  });

  it('production image build는 Actions cache 대신 registry inline cache를 쓴다', async () => {
    const source = await workflow('deploy.yml');
    assert.doesNotMatch(source, /cache-(?:from|to):\s*type=gha/u);
    assert.doesNotMatch(source, /cache-to:[^\n]*mode=max/u);
    assert.match(
      source,
      /cache-from: type=registry,ref=\$\{\{ env\.REGISTRY \}\}\/\$\{\{ env\.IMAGE_PATH \}\}:buildcache/u,
    );
    assert.match(source, /cache-to: type=inline/u);
    assert.match(
      source,
      /\$\{\{ env\.REGISTRY \}\}\/\$\{\{ env\.IMAGE_PATH \}\}:buildcache/u,
    );
  });

  it('GA4 Measurement Protocol secret은 ingest role에만 마운트한다', async () => {
    const source = await workflow('deploy.yml');
    assert.match(
      source,
      /platform-ingest[\s\S]*GA4_MEASUREMENT_PROTOCOL_SECRETS_JSON=ga4-measurement-protocol-secrets:latest/u,
    );
    assert.match(source, /name: Assert GA4 secret boundary/u);
    assert.match(source, /\[ "\$actual" = "ga4-measurement-protocol-secrets" \]/u);
  });
});
