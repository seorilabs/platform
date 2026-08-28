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
  it('GDScript asset 발행은 version tag에서만 실행되고 배포 명령을 포함하지 않는다', async () => {
    const source = await workflow('publish-sdk-gdscript.yml');
    assert.match(source, /tags:\s*\n\s+- "v\*\.\*\.\*"/u);
    assert.doesNotMatch(source, /workflow_dispatch/u);
    assert.match(source, /permissions:\s*\n\s+contents: read/u);
    assert.match(source, /publish:[\s\S]*permissions:\s*\n\s+contents: write/u);
    assert.match(source, new RegExp(`uses: actions/checkout@${ACTION_SHA}`, 'u'));
    assert.match(source, new RegExp(`uses: actions/setup-node@${ACTION_SHA}`, 'u'));
    assert.match(source, /runs-on: seorilabs-rpi-arm64/u);
    assert.match(source, /build-platform-release\.mjs/u);
    assert.match(source, /publish-platform-release\.mjs/u);
    assert.match(source, /needs: sdk/u);
    assert.doesNotMatch(source, /\b(?:gcloud|kubectl|firebase)\b/u);
  });

  it('PR gate는 generator를 두 번 실행해 byte 차이를 검사한다', async () => {
    const source = await workflow('checks-platform-release.yml');
    assert.match(source, /for output in first second/u);
    assert.match(source, /diff -rq/u);
    assert.match(source, /fetch-depth: 0/u);
    assert.match(source, new RegExp(`uses: actions/checkout@${ACTION_SHA}`, 'u'));
    assert.match(source, new RegExp(`uses: actions/setup-node@${ACTION_SHA}`, 'u'));
  });

  it('기존 TypeScript publish도 공통 generator를 검증한다', async () => {
    const source = await workflow('publish-sdk-ts.yml');
    assert.match(source, /sdk-ts-v\*\.\*\.\*/u);
    assert.match(source, /build-platform-release\.mjs/u);
    assert.match(source, /install-oasdiff\.mjs/u);
    assert.match(source, /fetch-depth: 0/u);
    assert.match(source, /npm publish "\$\{\{ steps\.pack\.outputs\.tarball \}\}"/u);
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
      for (const match of source.matchAll(/uses:\s*[^@\s]+@([^\s#]+)/gu)) {
        assert.match(match[1], /^[0-9a-f]{40}$/u, `${name}: ${match[0]}`);
      }
      assert.doesNotMatch(source, /secrets:\s*inherit/u, name);
    }
  });
});
