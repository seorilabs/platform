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
    schemaVersion: 2,
    release: {
      tag: 'v0.6.5',
      sourceSha: 'a'.repeat(40),
      baseSourceSha: 'b'.repeat(40),
    },
    sdk: {
      typescript: {
        package: '@seorilabs/platform-sdk',
        registry: 'https://registry.npmjs.org',
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

const SOURCE_SHA = 'a'.repeat(40);
const API = 'https://api.github.com/repos/seorilabs/platform';

// GitHub release API의 상태 전이를 흉내 낸다. 목록은 draft를 포함하고, 공개(PATCH draft:false)
// 순간 조직 설정처럼 immutable이 된다.
function fakeGitHub({ releases = [], immutableOnPublish = true } = {}) {
  const state = { releases: structuredClone(releases), nextAssetId: 100, calls: [] };
  const toProvider = (release) => ({
    ...release,
    assets: release.assets.map(({ content: _content, ...asset }) => asset),
    upload_url: `https://uploads.github.com/repos/seorilabs/platform/releases/${release.id}/assets{?name,label}`,
  });
  const fetchImpl = async (url, options = {}) => {
    const method = options.method ?? 'GET';
    state.calls.push({ method, url, body: options.body });
    if (method === 'GET' && url.startsWith(`${API}/releases?`)) {
      return jsonResponse(state.releases.map(toProvider));
    }
    if (method === 'POST' && url === `${API}/releases`) {
      const body = JSON.parse(options.body);
      const release = {
        id: 42,
        tag_name: body.tag_name,
        target_commitish: body.target_commitish,
        draft: body.draft,
        prerelease: body.prerelease,
        immutable: false,
        assets: [],
      };
      state.releases.push(release);
      return jsonResponse(toProvider(release), 201);
    }
    const releaseMatch = /\/releases\/(\d+)$/u.exec(url);
    if (releaseMatch && url.startsWith(`${API}/releases/`)) {
      const release = state.releases.find(({ id }) => id === Number(releaseMatch[1]));
      if (method === 'PATCH') {
        const body = JSON.parse(options.body);
        release.draft = body.draft;
        release.immutable = immutableOnPublish;
      }
      return jsonResponse(toProvider(release));
    }
    if (method === 'POST' && url.startsWith('https://uploads.github.com/')) {
      const releaseId = Number(/releases\/(\d+)\/assets/u.exec(url)[1]);
      const release = state.releases.find(({ id }) => id === releaseId);
      const content = Buffer.from(options.body);
      const id = state.nextAssetId;
      state.nextAssetId += 1;
      const asset = {
        id,
        name: new URL(url).searchParams.get('name'),
        size: content.length,
        url: `${API}/releases/assets/${id}`,
        content,
      };
      release.assets.push(asset);
      return jsonResponse({ ...asset, content: undefined }, 201);
    }
    if (method === 'GET' && url.startsWith(`${API}/releases/assets/`)) {
      const id = Number(url.split('/').at(-1));
      const asset = state.releases.flatMap(({ assets }) => assets).find((entry) => entry.id === id);
      return new Response(asset.content);
    }
    throw new Error(`예상하지 않은 요청: ${method} ${url}`);
  };
  const writes = () => state.calls.filter(({ method }) => method !== 'GET');
  return { fetchImpl, state, writes };
}

async function localAssets(directory) {
  const names = [
    'seorilabs-platform-sdk-0.4.0.tgz',
    'seorilabs-platform-gdscript-0.6.5.tar.gz',
    'seorilabs-platform-gdscript-0.6.5.tar.gz.sha256',
    'platform-release.json',
  ];
  return Promise.all(names.map(async (name, index) => {
    const content = await readFile(join(directory, name));
    return {
      id: index + 1,
      name,
      size: content.length,
      url: `${API}/releases/assets/${index + 1}`,
      content,
    };
  }));
}

function publish(directory, fetchImpl, overrides = {}) {
  return publishPlatformRelease({
    apiBase: 'https://api.github.com',
    directory,
    fetchImpl,
    repository: 'seorilabs/platform',
    sourceSha: SOURCE_SHA,
    tag: 'v0.6.5',
    token: 'test-token',
    ...overrides,
  });
}

describe('GitHub Release publisher', () => {
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

  it('draft에 네 asset을 모두 올리고 검증한 뒤 latest 공개 release로 끝낸다', async (test) => {
    const directory = await releaseDirectory(test);
    const github = fakeGitHub();

    const result = await publish(directory, github.fetchImpl);

    assert.deepEqual(result, { releaseId: 42, state: 'PUBLISHED', tag: 'v0.6.5' });
    const writes = github.writes();
    const create = writes.find(({ url }) => url === `${API}/releases`);
    assert.equal(JSON.parse(create.body).draft, true);
    const uploads = writes.filter(({ url }) => url.startsWith('https://uploads.github.com/'));
    assert.equal(uploads.length, 4);
    const patches = writes.filter(({ method }) => method === 'PATCH');
    assert.equal(patches.length, 1);
    assert.deepEqual(JSON.parse(patches[0].body), { draft: false, make_latest: 'true' });
    // immutable release는 공개 순간 asset이 잠기므로 업로드가 모두 공개보다 앞서야 한다.
    assert.equal(writes.indexOf(patches[0]), writes.length - 1);
    const [release] = github.state.releases;
    assert.equal(release.draft, false);
    assert.equal(release.immutable, true);
    assert.equal(release.assets.length, 4);
  });

  it('이전 실행이 남긴 draft를 다시 써서 빠진 asset만 올린다', async (test) => {
    const directory = await releaseDirectory(test);
    const assets = await localAssets(directory);
    const github = fakeGitHub({
      releases: [{
        id: 7,
        tag_name: 'v0.6.5',
        target_commitish: SOURCE_SHA,
        draft: true,
        prerelease: false,
        immutable: false,
        assets: assets.slice(0, 2),
      }],
    });

    const result = await publish(directory, github.fetchImpl);

    assert.deepEqual(result, { releaseId: 7, state: 'PUBLISHED', tag: 'v0.6.5' });
    const writes = github.writes();
    assert.equal(writes.some(({ url }) => url === `${API}/releases`), false);
    assert.equal(writes.filter(({ url }) => url.startsWith('https://uploads.github.com/')).length, 2);
    assert.equal(github.state.releases[0].draft, false);
  });

  it('이미 공개된 release는 asset byte가 같은지만 확인하고 쓰지 않는다', async (test) => {
    const directory = await releaseDirectory(test);
    const github = fakeGitHub({
      releases: [{
        id: 9,
        tag_name: 'v0.6.5',
        target_commitish: SOURCE_SHA,
        draft: false,
        prerelease: false,
        immutable: true,
        assets: await localAssets(directory),
      }],
    });

    const result = await publish(directory, github.fetchImpl);

    assert.deepEqual(result, { releaseId: 9, state: 'ALREADY_PUBLISHED', tag: 'v0.6.5' });
    assert.deepEqual(github.writes(), []);
  });

  it('이미 공개된 release의 asset byte가 이번 빌드와 다르면 쓰지 않고 실패한다', async (test) => {
    const directory = await releaseDirectory(test);
    const assets = await localAssets(directory);
    assets[0].content = Buffer.alloc(assets[0].size, 0x78);
    const github = fakeGitHub({
      releases: [{
        id: 9,
        tag_name: 'v0.6.5',
        target_commitish: SOURCE_SHA,
        draft: false,
        prerelease: false,
        immutable: true,
        assets,
      }],
    });

    await assert.rejects(publish(directory, github.fetchImpl), /digest가 다릅니다/u);
    assert.deepEqual(github.writes(), []);
  });

  it('공개 뒤 immutable이 아니면 성공으로 보고하지 않는다', async (test) => {
    const directory = await releaseDirectory(test);
    const github = fakeGitHub({ immutableOnPublish: false });

    await assert.rejects(publish(directory, github.fetchImpl), /immutable이 아닙니다/u);
  });

  it('같은 tag의 draft가 여러 개면 어느 것도 공개하지 않는다', async (test) => {
    const directory = await releaseDirectory(test);
    const draft = (id) => ({
      id,
      tag_name: 'v0.6.5',
      target_commitish: SOURCE_SHA,
      draft: true,
      prerelease: false,
      immutable: false,
      assets: [],
    });
    const github = fakeGitHub({ releases: [draft(7), draft(8)] });

    await assert.rejects(publish(directory, github.fetchImpl), /draft release가 2개/u);
    assert.deepEqual(github.writes(), []);
  });

  it('예상하지 않은 기존 asset이 있으면 변경하지 않고 중단한다', async (test) => {
    const directory = await releaseDirectory(test);
    const github = fakeGitHub({
      releases: [{
        id: 7,
        tag_name: 'v0.6.5',
        target_commitish: SOURCE_SHA,
        draft: true,
        prerelease: false,
        immutable: false,
        assets: [{ id: 1, name: 'unexpected.zip', size: 1, content: Buffer.from('x') }],
      }],
    });

    await assert.rejects(publish(directory, github.fetchImpl), /예상하지 않은 asset/u);
    assert.deepEqual(github.writes(), []);
  });

  it('다른 source로 만든 draft는 이어 쓰지 않는다', async (test) => {
    const directory = await releaseDirectory(test);
    const github = fakeGitHub({
      releases: [{
        id: 7,
        tag_name: 'v0.6.5',
        target_commitish: 'f'.repeat(40),
        draft: true,
        prerelease: false,
        immutable: false,
        assets: [],
      }],
    });

    await assert.rejects(publish(directory, github.fetchImpl), /exact source/u);
    assert.deepEqual(github.writes(), []);
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
