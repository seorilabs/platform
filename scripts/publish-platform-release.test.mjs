import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, it } from 'node:test';

import { canonicalJson, sha256 } from './platform-release-lib.mjs';
import { publishPlatformRelease } from './publish-platform-release.mjs';

async function releaseDirectory(test) {
  const directory = await mkdtemp(join(tmpdir(), 'platform-release-publish-test-'));
  test.after(() => rm(directory, { recursive: true, force: true }));
  const artifactName = 'seorilabs-platform-gdscript-0.6.5.tar.gz';
  const checksumName = `${artifactName}.sha256`;
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
      gdscript: {
        artifact: { name: artifactName, sha256: sha256(artifact), size: artifact.length },
        checksumArtifact: { name: checksumName, sha256: sha256(checksum), size: checksum.length },
      },
    },
    contract: {
      classification: 'implementation-only',
      revision: `sha256:${'c'.repeat(64)}`,
    },
  };
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

describe('GitHub Release immutable publisher', () => {
  it('draft를 만든 뒤 세 asset을 올리고 마지막에만 공개한다', async (test) => {
    const directory = await releaseDirectory(test);
    const calls = [];
    const uploaded = [];
    const fetchImpl = async (url, options = {}) => {
      calls.push({ url, options });
      if (url.endsWith('/releases?per_page=100')) {
        return jsonResponse([]);
      }
      if (url.endsWith('/releases') && options.method === 'POST') {
        return jsonResponse({
          id: 42,
          tag_name: 'v0.6.5',
          draft: true,
          assets: [],
          upload_url: 'https://uploads.github.test/repos/seorilabs/platform/releases/42/assets{?name,label}',
        }, 201);
      }
      if (url.startsWith('https://uploads.github.test/')) {
        const name = new URL(url).searchParams.get('name');
        const content = Buffer.from(options.body);
        const remote = {
          id: uploaded.length + 1,
          name,
          size: content.length,
          url: `https://api.github.test/assets/${uploaded.length + 1}`,
          content,
        };
        uploaded.push(remote);
        return jsonResponse(remote, 201);
      }
      if (url.endsWith('/releases/42') && !options.method) {
        return jsonResponse({
          id: 42,
          tag_name: 'v0.6.5',
          draft: true,
          assets: uploaded.map(({ content: _content, ...asset }) => asset),
        });
      }
      if (url.startsWith('https://api.github.test/assets/')) {
        const id = Number.parseInt(url.split('/').at(-1), 10);
        return new Response(uploaded.find((asset) => asset.id === id).content);
      }
      if (url.endsWith('/releases/42') && options.method === 'PATCH') {
        return jsonResponse({ id: 42, tag_name: 'v0.6.5', draft: false });
      }
      throw new Error(`예상하지 않은 요청: ${options.method ?? 'GET'} ${url}`);
    };

    const result = await publishPlatformRelease({
      apiBase: 'https://api.github.test',
      directory,
      fetchImpl,
      repository: 'seorilabs/platform',
      sourceSha: 'a'.repeat(40),
      tag: 'v0.6.5',
      token: 'test-token',
    });
    assert.deepEqual(result, { releaseId: 42, tag: 'v0.6.5' });
    assert.equal(calls.filter(({ url }) => url.startsWith('https://uploads.github.test/')).length, 3);
    const publish = calls.at(-1);
    assert.equal(publish.options.method, 'PATCH');
    assert.deepEqual(JSON.parse(publish.options.body), { draft: false });
  });

  it('이미 공개된 release에 asset이 빠졌으면 수정하지 않고 중단한다', async (test) => {
    const directory = await releaseDirectory(test);
    const fetchImpl = async () => jsonResponse([{
      id: 42,
      tag_name: 'v0.6.5',
      draft: false,
      assets: [],
      upload_url: 'https://uploads.github.test/releases/42/assets{?name,label}',
    }]);
    await assert.rejects(
      publishPlatformRelease({
        apiBase: 'https://api.github.test',
        directory,
        fetchImpl,
        repository: 'seorilabs/platform',
        sourceSha: 'a'.repeat(40),
        tag: 'v0.6.5',
        token: 'test-token',
      }),
      /immutable release에 asset이 없습니다/u,
    );
  });

  it('예상하지 않은 기존 asset이 있으면 변경하지 않고 중단한다', async (test) => {
    const directory = await releaseDirectory(test);
    const fetchImpl = async () => jsonResponse([{
      id: 42,
      tag_name: 'v0.6.5',
      draft: true,
      assets: [{ id: 1, name: 'unexpected.zip', size: 1 }],
      upload_url: 'https://uploads.github.test/releases/42/assets{?name,label}',
    }]);
    await assert.rejects(
      publishPlatformRelease({
        apiBase: 'https://api.github.test',
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
        apiBase: 'https://api.github.test',
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
        apiBase: 'https://api.github.test',
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
