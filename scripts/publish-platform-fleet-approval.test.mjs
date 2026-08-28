import assert from 'node:assert/strict';
import { generateKeyPairSync, sign } from 'node:crypto';
import { mkdtemp, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, it } from 'node:test';

import {
  platformReleaseApprovalBytes,
  platformReleaseApprovalPayload,
} from './platform-fleet-reconciler.mjs';
import { sha256 } from './platform-release-lib.mjs';
import { publishPlatformFleetApproval } from './publish-platform-fleet-approval.mjs';

const SOURCE_SHA = 'a'.repeat(40);
const TAG_SHA = 'b'.repeat(40);
const TAG = 'v0.7.0';

function jsonResponse(value, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function canaryEvidence() {
  const workflowSourceSha = 'c'.repeat(40);
  const canary = (profile, repositoryId, repositoryFullName, sourceSha, offset) => ({
    profile,
    repositoryId,
    repositoryFullName,
    sourceSha,
    staticRun: {
      runId: String(1000 + offset),
      conclusion: 'success',
      headSha: sourceSha,
      workflowSourceSha,
    },
    buildOnlyRun: {
      runId: String(2000 + offset),
      conclusion: 'success',
      headSha: sourceSha,
      workflowSourceSha,
      cloudBuildId: `build-${offset}`,
      builderImageDigest: `sha256:${String(offset).repeat(64)}`,
      buildConfigDigest: `sha256:${String(offset + 2).repeat(64)}`,
      artifact: {
        name: `${profile}-release.aab`,
        sha256: `sha256:${String(offset + 4).repeat(64)}`,
        size: 1024 + offset,
      },
    },
  });
  return {
    attestationSha256: `sha256:${'8'.repeat(64)}`,
    readbackKeyId: 'canary-readback-1',
    workflowBundle: {
      repository: 'seorilabs/.github',
      sourceSha: workflowSourceSha,
      digest: `sha256:${'9'.repeat(64)}`,
    },
    canaries: [
      canary('godot', '1265192029', 'seorilabs/lizard-tycoon', 'd'.repeat(40), 1),
      canary('react-native', '1250442131', 'seorilabs/happy-farm', 'e'.repeat(40), 2),
    ],
  };
}

async function localFixture(test) {
  const directory = await mkdtemp(join(tmpdir(), 'platform-fleet-approval-publish-'));
  test.after(() => rm(directory, { recursive: true, force: true }));

  const typescriptName = 'seorilabs-platform-sdk-0.7.0.tgz';
  const gdscriptName = 'seorilabs-platform-gdscript-0.7.0.tar.gz';
  const checksumName = `${gdscriptName}.sha256`;
  const typescriptBytes = Buffer.from('typescript-sdk-artifact', 'utf8');
  const gdscriptBytes = Buffer.from('gdscript-sdk-artifact', 'utf8');
  const checksumBytes = Buffer.from(`${sha256(gdscriptBytes)}  ${gdscriptName}\n`, 'utf8');
  const manifest = {
    schemaVersion: 1,
    release: { tag: TAG, sourceSha: SOURCE_SHA, baseSourceSha: 'f'.repeat(40) },
    sdk: {
      typescript: {
        package: '@seorilabs/platform-sdk',
        registry: 'https://npm.pkg.github.com',
        version: '0.7.0',
        artifact: {
          name: typescriptName,
          sha256: sha256(typescriptBytes),
          size: typescriptBytes.length,
        },
      },
      gdscript: {
        version: '0.7.0',
        source: `https://github.com/seorilabs/platform/releases/download/${TAG}/${gdscriptName}`,
        treeChecksum: '7'.repeat(64),
        artifact: {
          name: gdscriptName,
          sha256: sha256(gdscriptBytes),
          size: gdscriptBytes.length,
        },
        checksumArtifact: {
          name: checksumName,
          sha256: sha256(checksumBytes),
          size: checksumBytes.length,
        },
      },
    },
    contract: {
      affectedCapabilities: ['core'],
      affectedTracks: ['gdscript', 'typescript'],
      baseRevision: `sha256:${'1'.repeat(64)}`,
      classification: 'implementation-only',
      revision: `sha256:${'2'.repeat(64)}`,
      supportedApiMajor: 1,
    },
  };
  const manifestBytes = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`, 'utf8');
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const evidence = canaryEvidence();
  const payload = platformReleaseApprovalPayload(manifestBytes, evidence);
  const approval = {
    schemaVersion: 2,
    algorithm: 'Ed25519',
    keyId: 'fleet-release-key-1',
    payload,
    signature: sign(
      null,
      platformReleaseApprovalBytes(manifestBytes, evidence),
      privateKey,
    ).toString('base64'),
  };
  const approvalBytes = Buffer.from(`${JSON.stringify(approval, null, 2)}\n`, 'utf8');
  const trustedKeys = {
    schemaVersion: 1,
    keys: [{
      algorithm: 'Ed25519',
      keyId: 'fleet-release-key-1',
      publicKeyPem: publicKey.export({ format: 'pem', type: 'spki' }),
      status: 'ACTIVE',
    }],
  };
  const paths = {
    manifest: join(directory, 'platform-release.json'),
    approval: join(directory, 'fleet-approved.json'),
    trustedKeys: join(directory, 'trusted-release-keys.json'),
  };
  await Promise.all([
    writeFile(paths.manifest, manifestBytes),
    writeFile(paths.approval, approvalBytes),
    writeFile(paths.trustedKeys, `${JSON.stringify(trustedKeys, null, 2)}\n`),
  ]);

  const contents = new Map([
    ['platform-release.json', manifestBytes],
    [typescriptName, typescriptBytes],
    [gdscriptName, gdscriptBytes],
    [checksumName, checksumBytes],
  ]);
  return { approval, approvalBytes, contents, directory, manifest, manifestBytes, paths };
}

function githubFixture(local, overrides = {}) {
  const calls = [];
  const assets = [...local.contents].map(([name, content], index) => ({
    id: index + 1,
    name,
    size: content.length,
    browser_download_url: `https://github.com/seorilabs/platform/releases/download/${TAG}/${name}`,
    content,
  }));
  if (overrides.existingApproval) {
    assets.push({
      id: assets.length + 1,
      name: 'fleet-approved.json',
      size: overrides.existingApproval.length,
      browser_download_url: `https://github.com/seorilabs/platform/releases/download/${TAG}/fleet-approved.json`,
      content: overrides.existingApproval,
    });
  }
  if (overrides.missingAsset) {
    assets.splice(assets.findIndex((asset) => asset.name === overrides.missingAsset), 1);
  }
  if (overrides.corruptAsset) {
    const asset = assets.find((entry) => entry.name === overrides.corruptAsset);
    asset.content = Buffer.from(asset.content.map((byte, index) => (index === 0 ? byte ^ 1 : byte)));
  }
  if (overrides.unexpectedAsset) {
    assets.push({
      id: 99,
      name: 'unexpected.zip',
      size: 1,
      browser_download_url: `https://github.com/seorilabs/platform/releases/download/${TAG}/unexpected.zip`,
      content: Buffer.from('x'),
    });
  }

  const release = () => ({
    id: 42,
    tag_name: overrides.releaseTag ?? TAG,
    draft: overrides.draft ?? false,
    prerelease: overrides.prerelease ?? false,
    upload_url: 'https://uploads.github.com/repos/seorilabs/platform/releases/42/assets{?name,label}',
    assets: assets.map(({ content: _content, ...asset }) => asset),
  });
  const fetchImpl = async (url, options = {}) => {
    calls.push({ url, options });
    if (url.endsWith(`/releases/tags/${TAG}`)) {
      return jsonResponse(release());
    }
    if (url.endsWith(`/git/ref/tags/${TAG}`)) {
      return jsonResponse({ object: { sha: TAG_SHA, type: 'tag' } });
    }
    if (url.endsWith(`/git/tags/${TAG_SHA}`)) {
      return jsonResponse({
        object: { sha: overrides.tagCommit ?? SOURCE_SHA, type: 'commit' },
      });
    }
    if (url.startsWith(`https://github.com/seorilabs/platform/releases/download/${TAG}/`)) {
      const asset = assets.find((entry) => entry.browser_download_url === url);
      return new Response(asset.content);
    }
    if (url.startsWith('https://uploads.github.com/')) {
      if (overrides.uploadStatus === 422) {
        if (overrides.raceApproval) {
          assets.push({
            id: assets.length + 1,
            name: 'fleet-approved.json',
            size: overrides.raceApproval.length,
            browser_download_url: `https://github.com/seorilabs/platform/releases/download/${TAG}/fleet-approved.json`,
            content: overrides.raceApproval,
          });
        }
        return jsonResponse({ message: 'already_exists' }, 422);
      }
      const content = Buffer.from(options.body);
      assets.push({
        id: assets.length + 1,
        name: 'fleet-approved.json',
        size: content.length,
        browser_download_url: `https://github.com/seorilabs/platform/releases/download/${TAG}/fleet-approved.json`,
        content,
      });
      return jsonResponse({ id: assets.length, name: 'fleet-approved.json' }, 201);
    }
    throw new Error(`예상하지 않은 요청: ${options.method ?? 'GET'} ${url}`);
  };
  return { assets, calls, fetchImpl };
}

async function publish(local, remote) {
  return publishPlatformFleetApproval({
    apiBase: 'https://api.github.com',
    approvalPath: local.paths.approval,
    fetchImpl: remote.fetchImpl,
    manifestPath: local.paths.manifest,
    repository: 'seorilabs/platform',
    token: 'test-token',
    trustedKeysPath: local.paths.trustedKeys,
  });
}

describe('Platform Fleet approval create-once publisher', () => {
  it('공개 release와 네 base asset을 검증한 뒤 approval을 한 번 올리고 readback한다', async (test) => {
    const local = await localFixture(test);
    const remote = githubFixture(local);
    const result = await publish(local, remote);
    assert.deepEqual(result, {
      approvalSha256: `sha256:${sha256(local.approvalBytes)}`,
      created: true,
      releaseId: 42,
      sourceSha: SOURCE_SHA,
      tag: TAG,
    });
    assert.equal(remote.calls.filter(({ url }) => url.startsWith('https://uploads.github.com/')).length, 1);
    assert.equal(remote.assets.filter(({ name }) => name === 'fleet-approved.json').length, 1);
    const publicDownloads = remote.calls.filter(({ url }) => url.startsWith('https://github.com/'));
    assert.ok(publicDownloads.length > 0);
    assert.equal(
      publicDownloads.some(({ options }) => Object.hasOwn(options.headers, 'Authorization')),
      false,
    );
  });

  it('동일 approval이 이미 있으면 mutation 없이 idempotent success로 끝낸다', async (test) => {
    const local = await localFixture(test);
    const remote = githubFixture(local, { existingApproval: local.approvalBytes });
    const result = await publish(local, remote);
    assert.equal(result.created, false);
    assert.equal(remote.calls.some(({ url }) => url.startsWith('https://uploads.github.com/')), false);
  });

  it('다른 approval이 이미 있으면 삭제하거나 덮어쓰지 않는다', async (test) => {
    const local = await localFixture(test);
    const different = Buffer.from(local.approvalBytes);
    different[different.length - 2] ^= 1;
    const remote = githubFixture(local, { existingApproval: different });
    await assert.rejects(publish(local, remote), /다른 승인/u);
    assert.equal(remote.calls.some(({ url }) => url.startsWith('https://uploads.github.com/')), false);
  });

  it('동시 create의 422는 exact readback일 때만 idempotent success로 인정한다', async (test) => {
    const local = await localFixture(test);
    const remote = githubFixture(local, {
      raceApproval: local.approvalBytes,
      uploadStatus: 422,
    });
    const result = await publish(local, remote);
    assert.equal(result.created, false);
    assert.equal(remote.assets.filter(({ name }) => name === 'fleet-approved.json').length, 1);
  });

  it('서명 변조와 symbolic-link 입력은 API를 호출하기 전에 거부한다', async (test) => {
    const local = await localFixture(test);
    const changed = structuredClone(local.approval);
    changed.signature = `${changed.signature.slice(0, -2)}AA`;
    await writeFile(local.paths.approval, `${JSON.stringify(changed)}\n`);
    let calls = 0;
    const fetchImpl = async () => {
      calls += 1;
      return jsonResponse({});
    };
    await assert.rejects(
      publishPlatformFleetApproval({
        apiBase: 'https://api.github.com',
        approvalPath: local.paths.approval,
        fetchImpl,
        manifestPath: local.paths.manifest,
        repository: 'seorilabs/platform',
        token: 'test-token',
        trustedKeysPath: local.paths.trustedKeys,
      }),
      /서명이 올바르지 않습니다/u,
    );
    assert.equal(calls, 0);

    const linkPath = join(local.directory, 'manifest-link.json');
    await symlink(local.paths.manifest, linkPath);
    await assert.rejects(
      publishPlatformFleetApproval({
        apiBase: 'https://api.github.com',
        approvalPath: local.paths.approval,
        fetchImpl,
        manifestPath: linkPath,
        repository: 'seorilabs/platform',
        token: 'test-token',
        trustedKeysPath: local.paths.trustedKeys,
      }),
      /symbolic link/u,
    );
    assert.equal(calls, 0);
  });

  for (const scenario of [
    ['다른 release tag', { releaseTag: 'v0.7.1' }, /exact tag/u],
    ['다른 tag source SHA', { tagCommit: '0'.repeat(40) }, /source SHA/u],
    ['draft release', { draft: true }, /exact tag/u],
    ['prerelease', { prerelease: true }, /exact tag/u],
    ['예상하지 않은 asset', { unexpectedAsset: true }, /예상하지 않은 asset/u],
    ['누락된 base asset', { missingAsset: 'seorilabs-platform-sdk-0.7.0.tgz' }, /필수 base asset/u],
    ['변조된 base asset', { corruptAsset: 'seorilabs-platform-gdscript-0.7.0.tar.gz' }, /digest/u],
  ]) {
    it(`${scenario[0]}에서는 approval mutation을 만들지 않는다`, async (test) => {
      const local = await localFixture(test);
      const remote = githubFixture(local, scenario[1]);
      await assert.rejects(publish(local, remote), scenario[2]);
      assert.equal(remote.calls.some(({ url }) => url.startsWith('https://uploads.github.com/')), false);
    });
  }
});
