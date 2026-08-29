import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, it } from 'node:test';

import { canonicalJson, sha256 } from './platform-release-lib.mjs';
import {
  publishPlatformRelease,
  validatePlatformReleaseAssetRedirect,
} from './publish-platform-release.mjs';

async function releaseDirectory(test) {
  const directory = await mkdtemp(join(tmpdir(), 'platform-release-publish-test-'));
  test.after(() => rm(directory, { recursive: true, force: true }));
  const typescriptName = 'seorilabs-platform-sdk-0.4.0.tgz';
  const artifactName = 'seorilabs-platform-gdscript-0.6.5.tar.gz';
  const checksumName = `${artifactName}.sha256`;
  const typescript = Buffer.from('deterministic-typescript-artifact');
  const artifact = Buffer.from('deterministic-gdscript-artifact');
  const checksum = Buffer.from(`${sha256(artifact)}  ${artifactName}\n`);
  const manifest = {
    schemaVersion: 1,
    release: {
      tag: 'v0.6.5',
      sourceSha: 'a'.repeat(40),
      baseSourceSha: 'b'.repeat(40),
    },
    sdk: {
      typescript: {
        package: '@seorilabs/platform-sdk',
        registry: 'https://npm.pkg.github.com',
        version: '0.4.0',
        artifact: {
          name: typescriptName,
          sha256: sha256(typescript),
          size: typescript.length,
        },
      },
      gdscript: {
        version: '0.6.5',
        source: 'https://github.com/seorilabs/platform/releases/download/v0.6.5/seorilabs-platform-gdscript-0.6.5.tar.gz',
        treeChecksum: 'd'.repeat(64),
        artifact: { name: artifactName, sha256: sha256(artifact), size: artifact.length },
        checksumArtifact: { name: checksumName, sha256: sha256(checksum), size: checksum.length },
      },
    },
    contract: {
      affectedCapabilities: ['core'],
      affectedConsumers: {
        cohort: 'backoffice-managed-product-apps',
        resolution: 'reconcile-time',
      },
      affectedTracks: ['gdscript'],
      baseRevision: `sha256:${'d'.repeat(64)}`,
      classification: 'implementation-only',
      revision: `sha256:${'c'.repeat(64)}`,
      supportedApiMajor: 1,
    },
  };
  await writeFile(join(directory, typescriptName), typescript);
  await writeFile(join(directory, artifactName), artifact);
  await writeFile(join(directory, checksumName), checksum);
  await writeFile(join(directory, 'platform-release.json'), canonicalJson(manifest));
  return directory;
}

function jsonResponse(value, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('GitHub Release immutable draft publisher', () => {
  it('release asset redirect는 기본 HTTPS 포트의 허용 host만 따른다', () => {
    assert.equal(
      validatePlatformReleaseAssetRedirect(
        'https://release-assets.githubusercontent.com/path/to/asset',
        'fixture asset',
      ),
      'https://release-assets.githubusercontent.com/path/to/asset',
    );
    assert.throws(
      () => validatePlatformReleaseAssetRedirect(
        'https://release-assets.githubusercontent.com:8443/path/to/asset',
        'fixture asset',
      ),
      /redirect origin/u,
    );
  });

  it('TypeScript를 포함한 네 asset을 검증한 approval 대기 draft로만 준비한다', async (test) => {
    const directory = await releaseDirectory(test);
    const calls = [];
    const uploaded = [];
    const fetchImpl = async (url, options = {}) => {
      calls.push({ url, options });
      if (url.endsWith('/releases/tags/v0.6.5')) {
        return jsonResponse({ message: 'Not Found' }, 404);
      }
      if (url.endsWith('/releases') && options.method === 'POST') {
        return jsonResponse({
          id: 42,
          tag_name: 'v0.6.5',
          target_commitish: 'a'.repeat(40),
          draft: true,
          prerelease: false,
          assets: [],
          upload_url: 'https://uploads.github.com/repos/seorilabs/platform/releases/42/assets{?name,label}',
        }, 201);
      }
      if (url.startsWith('https://uploads.github.com/')) {
        const name = new URL(url).searchParams.get('name');
        const content = Buffer.from(options.body);
        const remote = {
          id: uploaded.length + 1,
          name,
          size: content.length,
          url: `https://api.github.com/repos/seorilabs/platform/releases/assets/${uploaded.length + 1}`,
          content,
        };
        uploaded.push(remote);
        return jsonResponse(remote, 201);
      }
      if (url.endsWith('/releases/42') && !options.method) {
        return jsonResponse({
          id: 42,
          tag_name: 'v0.6.5',
          target_commitish: 'a'.repeat(40),
          draft: true,
          prerelease: false,
          assets: uploaded.map(({ content: _content, ...asset }) => asset),
        });
      }
      if (url.startsWith('https://api.github.com/repos/seorilabs/platform/releases/assets/')) {
        const id = Number.parseInt(url.split('/').at(-1), 10);
        return new Response(uploaded.find((asset) => asset.id === id).content);
      }
      throw new Error(`예상하지 않은 요청: ${options.method ?? 'GET'} ${url}`);
    };

    const result = await publishPlatformRelease({
      apiBase: 'https://api.github.com',
      directory,
      fetchImpl,
      repository: 'seorilabs/platform',
      sourceSha: 'a'.repeat(40),
      tag: 'v0.6.5',
      token: 'test-token',
    });
    assert.deepEqual(result, {
      releaseId: 42,
      state: 'AWAITING_FLEET_APPROVAL',
      tag: 'v0.6.5',
    });
    assert.equal(calls[0].url.endsWith('/releases/tags/v0.6.5'), true);
    assert.equal(calls.some(({ url }) => url.includes('/releases?')), false);
    assert.equal(calls.filter(({ url }) => url.startsWith('https://uploads.github.com/')).length, 4);
    assert.equal(calls.some(({ options }) => options.method === 'PATCH'), false);
  });

  it('TypeScript artifact가 없으면 API 호출 전에 중단한다', async (test) => {
    const directory = await releaseDirectory(test);
    await rm(join(directory, 'seorilabs-platform-sdk-0.4.0.tgz'));
    let calls = 0;
    const fetchImpl = async () => {
      calls += 1;
      return jsonResponse([]);
    };
    await assert.rejects(
      publishPlatformRelease({
        apiBase: 'https://api.github.com',
        directory,
        fetchImpl,
        repository: 'seorilabs/platform',
        sourceSha: 'a'.repeat(40),
        tag: 'v0.6.5',
        token: 'test-token',
      }),
      /ENOENT/u,
    );
    assert.equal(calls, 0);
  });

  it('TypeScript artifact size가 manifest와 다르면 API 호출 전에 중단한다', async (test) => {
    const directory = await releaseDirectory(test);
    const manifestPath = join(directory, 'platform-release.json');
    const manifest = JSON.parse(await readFile(manifestPath, 'utf8'));
    manifest.sdk.typescript.artifact.size += 1;
    await writeFile(manifestPath, canonicalJson(manifest));
    let calls = 0;
    const fetchImpl = async () => {
      calls += 1;
      return jsonResponse([]);
    };
    await assert.rejects(
      publishPlatformRelease({
        apiBase: 'https://api.github.com',
        directory,
        fetchImpl,
        repository: 'seorilabs/platform',
        sourceSha: 'a'.repeat(40),
        tag: 'v0.6.5',
        token: 'test-token',
      }),
      /size가 manifest와 다릅니다/u,
    );
    assert.equal(calls, 0);
  });

  it('이미 공개된 release는 base publisher가 수정하지 않는다', async (test) => {
    const directory = await releaseDirectory(test);
    const fetchImpl = async () => jsonResponse({
      id: 42,
      tag_name: 'v0.6.5',
      target_commitish: 'a'.repeat(40),
      draft: false,
      prerelease: false,
      assets: [],
      upload_url: 'https://uploads.github.com/repos/seorilabs/platform/releases/42/assets{?name,label}',
    });
    await assert.rejects(
      publishPlatformRelease({
        apiBase: 'https://api.github.com',
        directory,
        fetchImpl,
        repository: 'seorilabs/platform',
        sourceSha: 'a'.repeat(40),
        tag: 'v0.6.5',
        token: 'test-token',
      }),
      /approval 대기 draft/u,
    );
  });

  it('예상하지 않은 기존 asset이 있으면 변경하지 않고 중단한다', async (test) => {
    const directory = await releaseDirectory(test);
    const fetchImpl = async () => jsonResponse({
      id: 42,
      tag_name: 'v0.6.5',
      target_commitish: 'a'.repeat(40),
      draft: true,
      prerelease: false,
      assets: [{ id: 1, name: 'unexpected.zip', size: 1 }],
      upload_url: 'https://uploads.github.com/repos/seorilabs/platform/releases/42/assets{?name,label}',
    });
    await assert.rejects(
      publishPlatformRelease({
        apiBase: 'https://api.github.com',
        directory,
        fetchImpl,
        repository: 'seorilabs/platform',
        sourceSha: 'a'.repeat(40),
        tag: 'v0.6.5',
        token: 'test-token',
      }),
      /예상하지 않은 asset/u,
    );
  });

  it('manifest source SHA나 release repository가 실행 경계와 다르면 API를 호출하지 않는다', async (test) => {
    const directory = await releaseDirectory(test);
    let calls = 0;
    const fetchImpl = async () => {
      calls += 1;
      return jsonResponse([]);
    };
    await assert.rejects(
      publishPlatformRelease({
        apiBase: 'https://api.github.com',
        directory,
        fetchImpl,
        repository: 'seorilabs/platform',
        sourceSha: 'f'.repeat(40),
        tag: 'v0.6.5',
        token: 'test-token',
      }),
      /sourceSha/u,
    );
    await assert.rejects(
      publishPlatformRelease({
        apiBase: 'https://api.github.com',
        directory,
        fetchImpl,
        repository: 'fork/platform',
        sourceSha: 'a'.repeat(40),
        tag: 'v0.6.5',
        token: 'test-token',
      }),
      /repository/u,
    );
    assert.equal(calls, 0);
  });
});
