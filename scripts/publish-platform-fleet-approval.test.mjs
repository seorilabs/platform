import assert from 'node:assert/strict';
import { generateKeyPairSync, sign } from 'node:crypto';
import { mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { describe, it } from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  platformReleaseApprovalBytes,
  platformReleaseApprovalPayload,
} from './platform-fleet-reconciler.mjs';
import { sha256 } from './platform-release-lib.mjs';
import {
  platformFleetPolicyAttestationBytes,
  publishPlatformFleetApproval,
  readPlatformFleetLatest,
  validatePlatformFleetReleaseAssetRedirect,
  verifyPlatformFleetApprovalPublishingInputs,
  verifyPlatformFleetImmutablePolicy,
  verifyPlatformFleetLatestReadback,
} from './publish-platform-fleet-approval.mjs';

const SOURCE_SHA = 'a'.repeat(40);
const TAG_OBJECT_SHA = 'b'.repeat(40);
const TAG = 'v0.7.0';
const REPOSITORY_ID = '1357913579';
const RULESET_ID = '88';
const RULESET_UPDATED_AT = '2026-08-29T00:00:00.000Z';
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');

function jsonResponse(value, status = 200, headers = {}) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
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
      affectedConsumers: {
        cohort: 'backoffice-active-apps',
        resolution: 'reconcile-time',
      },
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
    keyId: 'fixture-fleet-release-key-1',
    payload,
    signature: sign(
      null,
      platformReleaseApprovalBytes(manifestBytes, evidence),
      privateKey,
    ).toString('base64'),
  };
  const approvalBytes = Buffer.from(`${JSON.stringify(approval, null, 2)}\n`, 'utf8');
  const observedAt = new Date(Date.now() - 30 * 1000).toISOString();
  const policyPayload = {
    purpose: 'seorilabs-platform-release-policy-attestation-v1',
    repository: 'seorilabs/platform',
    repositoryId: REPOSITORY_ID,
    releaseTag: TAG,
    sourceSha: SOURCE_SHA,
    approvalSha256: `sha256:${sha256(approvalBytes)}`,
    observedAt,
    expiresAt: new Date(Date.now() + 4 * 60 * 1000).toISOString(),
    immutableReleases: { enabled: true, enforcedByOwner: true },
    ruleset: {
      id: RULESET_ID,
      name: 'Immutable Platform release tags',
      sourceType: 'Organization',
      source: 'seorilabs',
      target: 'tag',
      enforcement: 'active',
      bypassActors: [],
      refName: { include: ['refs/tags/v*'], exclude: [] },
      ruleTypes: ['deletion', 'update'],
      updatedAt: RULESET_UPDATED_AT,
    },
  };
  const createPolicyAttestationBytes = (nextPayload = policyPayload) => Buffer.from(
    `${JSON.stringify({
      schemaVersion: 1,
      algorithm: 'Ed25519',
      keyId: approval.keyId,
      payload: nextPayload,
      signature: sign(
        null,
        platformFleetPolicyAttestationBytes(nextPayload),
        privateKey,
      ).toString('base64'),
    }, null, 2)}\n`,
    'utf8',
  );
  const policyAttestationBytes = createPolicyAttestationBytes();
  const trustRegistryBytes = Buffer.from(`${JSON.stringify({
    schemaVersion: 1,
    keys: [{
      algorithm: 'Ed25519',
      keyId: approval.keyId,
      publicKeyPem: publicKey.export({ format: 'pem', type: 'spki' }),
      status: 'ACTIVE',
    }],
  }, null, 2)}\n`, 'utf8');
  const paths = {
    manifest: join(directory, 'platform-release.json'),
    approval: join(directory, 'fleet-approved.json'),
    policyAttestation: join(directory, 'platform-policy-attestation.json'),
  };
  await Promise.all([
    writeFile(paths.manifest, manifestBytes),
    writeFile(paths.approval, approvalBytes),
    writeFile(paths.policyAttestation, policyAttestationBytes),
  ]);
  const contents = new Map([
    ['platform-release.json', manifestBytes],
    [typescriptName, typescriptBytes],
    [gdscriptName, gdscriptBytes],
    [checksumName, checksumBytes],
  ]);
  const grant = {
    schemaVersion: 1,
    purpose: 'seorilabs-platform-approval-publish-grant-v1',
    grantId: 'grant-fixture-1',
    repository: 'seorilabs/platform',
    releaseTag: TAG,
    sourceSha: SOURCE_SHA,
    manifestSha256: `sha256:${sha256(manifestBytes)}`,
    approvalSha256: `sha256:${sha256(approvalBytes)}`,
    trustedReleaseKeyRegistrySha256: `sha256:${sha256(trustRegistryBytes)}`,
    keyId: approval.keyId,
    maxUses: 1,
    expiresAt: new Date(Date.now() + 4 * 60 * 1000).toISOString(),
  };
  return {
    approval,
    approvalBytes,
    contents,
    createPolicyAttestationBytes,
    directory,
    grant,
    manifest,
    manifestBytes,
    paths,
    policyAttestationBytes,
    policyPayload,
    trustRegistryBytes,
  };
}

function githubFixture(local, overrides = {}) {
  const calls = [];
  const assets = [...local.contents].map(([name, content], index) => ({
    id: index + 1,
    name,
    size: content.length,
    content,
  }));
  if (overrides.existingApproval) {
    assets.push({
      id: assets.length + 1,
      name: 'fleet-approved.json',
      size: overrides.existingApproval.length,
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
    assets.push({ id: 99, name: 'unexpected.zip', size: 1, content: Buffer.from('x') });
  }

  let published = overrides.published ?? false;
  let immutable = overrides.published ? (overrides.immutable ?? true) : false;
  let releaseReads = 0;
  let tagReads = 0;
  let policyReads = 0;
  const currentRelease = () => ({
    id: 42,
    tag_name: overrides.releaseTag ?? TAG,
    target_commitish: overrides.releaseSource ?? SOURCE_SHA,
    draft: !published,
    immutable,
    prerelease: overrides.prerelease ?? false,
    upload_url: 'https://uploads.github.com/repos/seorilabs/platform/releases/42/assets{?name,label}',
    assets: assets.map(({ content: _content, ...asset }) => asset),
  });
  const fetchImpl = async (url, options = {}) => {
    calls.push({ url, options });
    if (url === 'https://api.github.com/repos/seorilabs/platform') {
      return jsonResponse({
        id: Number(overrides.repositoryId ?? REPOSITORY_ID),
        full_name: 'seorilabs/platform',
        owner: { login: 'seorilabs', type: 'Organization' },
        pushed_at: overrides.repositoryVolatile ?? '2026-08-29T00:00:00.000Z',
      });
    }
    if (url.endsWith('/immutable-releases')) {
      policyReads += 1;
      return jsonResponse({
        enabled: overrides.immutableEnabled ?? true,
        enforced_by_owner: overrides.ownerEnforced ?? true,
      });
    }
    if (url.endsWith('/rulesets?includes_parents=true&per_page=100')) {
      return jsonResponse(overrides.missingRuleset ? [] : [{
        id: 88,
        name: 'Immutable Platform release tags',
        enforcement: 'active',
        source_type: overrides.repositoryRuleset ? 'Repository' : 'Organization',
        source: overrides.repositoryRuleset ? 'seorilabs/platform' : 'seorilabs',
        node_id: overrides.rulesetVolatile ?? 'RRS_fixture_one',
        updated_at: overrides.policyChange && policyReads > 1
          ? '2026-08-29T00:01:00.000Z'
          : RULESET_UPDATED_AT,
      }]);
    }
    if (url.endsWith(`/releases/tags/${TAG}`)) {
      releaseReads += 1;
      return jsonResponse(currentRelease());
    }
    if (url.endsWith(`/git/ref/tags/${TAG}`)) {
      tagReads += 1;
      return jsonResponse({ object: { sha: TAG_OBJECT_SHA, type: 'tag' } });
    }
    if (url.endsWith(`/git/tags/${TAG_OBJECT_SHA}`)) {
      const moved = overrides.moveTagAfterUpload
        && assets.some((asset) => asset.name === 'fleet-approved.json')
        && tagReads > 1;
      return jsonResponse({
        object: { sha: moved ? '0'.repeat(40) : (overrides.tagCommit ?? SOURCE_SHA), type: 'commit' },
      });
    }
    if (url.includes('/releases/assets/')) {
      const id = Number.parseInt(url.split('/').at(-1), 10);
      const asset = assets.find((entry) => entry.id === id);
      if (overrides.assetRedirectHost) {
        return new Response(null, {
          status: 302,
          headers: { Location: `https://${overrides.assetRedirectHost}/asset-${id}` },
        });
      }
      return new Response(asset.content);
    }
    if (url.startsWith('https://release-assets.githubusercontent.com/')) {
      const id = Number.parseInt(url.split('-').at(-1), 10);
      return new Response(assets.find((entry) => entry.id === id).content);
    }
    if (url.startsWith('https://uploads.github.com/')) {
      if (overrides.uploadErrorBody) {
        return jsonResponse({ message: overrides.uploadErrorBody }, 500, {
          'x-github-request-id': 'REQ-123',
        });
      }
      if (overrides.uploadStatus === 422) {
        if (overrides.raceApproval) {
          assets.push({
            id: assets.length + 1,
            name: 'fleet-approved.json',
            size: overrides.raceApproval.length,
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
        content,
      });
      return jsonResponse({ id: assets.length, name: 'fleet-approved.json' }, 201);
    }
    if (url.endsWith('/releases/42') && options.method === 'PATCH') {
      published = true;
      immutable = overrides.finalImmutable ?? true;
      return jsonResponse(currentRelease());
    }
    throw new Error(`예상하지 않은 요청: ${options.method ?? 'GET'} ${url}`);
  };
  return { assets, calls, fetchImpl, get releaseReads() { return releaseReads; } };
}

function verifyLocal(local, overrides = {}) {
  return verifyPlatformFleetApprovalPublishingInputs({
    approvalBytes: overrides.approvalBytes ?? local.approvalBytes,
    grant: overrides.grant ?? local.grant,
    manifestBytes: overrides.manifestBytes ?? local.manifestBytes,
    policyAttestationBytes: overrides.policyAttestationBytes ?? local.policyAttestationBytes,
    trustRegistryBytes: overrides.trustRegistryBytes ?? local.trustRegistryBytes,
    trustRegistryExpectedSha256: overrides.trustRegistryExpectedSha256
      ?? sha256(local.trustRegistryBytes),
  });
}

describe('Platform Fleet immutable approval publisher', () => {
  it('release asset redirect는 기본 HTTPS 포트의 허용 host만 따른다', () => {
    assert.equal(
      validatePlatformFleetReleaseAssetRedirect(
        'https://release-assets.githubusercontent.com/path/to/asset',
        'fixture asset',
      ),
      'https://release-assets.githubusercontent.com/path/to/asset',
    );
    assert.throws(
      () => validatePlatformFleetReleaseAssetRedirect(
        'https://release-assets.githubusercontent.com:8443/path/to/asset',
        'fixture asset',
      ),
      /redirect origin/u,
    );
  });

  it('production mutator는 tracked registry와 digest를 내부에서만 고정한다', async (test) => {
    const local = await localFixture(test);
    const [registryBytes, publisherSource] = await Promise.all([
      readFile(resolve(root, '.github/platform-fleet-trusted-release-keys.json')),
      readFile(resolve(root, 'scripts/publish-platform-fleet-approval.mjs'), 'utf8'),
    ]);
    assert.match(publisherSource, new RegExp(sha256(registryBytes), 'u'));
    assert.doesNotMatch(publisherSource, /--trusted-keys/u);
    assert.match(
      publisherSource,
      /export async function publishPlatformFleetApproval\(options\)/u,
    );
    assert.match(publisherSource, /make_latest: 'true'/u);
    assert.match(publisherSource, /releases\/latest/u);
    let calls = 0;
    const fetchImpl = async () => {
      calls += 1;
      return jsonResponse({});
    };
    await assert.rejects(publishPlatformFleetApproval({
      approvalPath: local.paths.approval,
      fetchImpl,
      grant: local.grant,
      manifestPath: local.paths.manifest,
      policyAttestationPath: local.paths.policyAttestation,
      repository: 'seorilabs/platform',
      token: 'credential-canary-token',
      // production mutator는 알 수 없는 dependency와 trust root 입력 자체를 거부한다.
      trustRegistryBytes: local.trustRegistryBytes,
      trustRegistryExpectedSha256: sha256(local.trustRegistryBytes),
    }), /production mutator 필드가 올바르지 않습니다/u);
    assert.equal(calls, 0);
  });

  it('검증된 immutable approval release와 같은 provider resource만 latest로 인정한다', () => {
    const assets = [
      { id: 1, name: 'platform-release.json', size: 100 },
      { id: 2, name: 'seorilabs-platform-sdk-0.7.0.tgz', size: 200 },
      { id: 3, name: 'seorilabs-platform-gdscript-0.7.0.tar.gz', size: 300 },
      { id: 4, name: 'seorilabs-platform-gdscript-0.7.0.tar.gz.sha256', size: 107 },
      { id: 5, name: 'fleet-approved.json', size: 400 },
    ];
    const approvedRelease = {
      id: 42,
      tag_name: TAG,
      target_commitish: SOURCE_SHA,
      draft: false,
      prerelease: false,
      immutable: true,
      assets,
    };
    const latestRelease = structuredClone(approvedRelease);
    latestRelease.assets.reverse();
    assert.deepEqual(
      verifyPlatformFleetLatestReadback({ approvedRelease, latestRelease }),
      { latest: true, releaseId: 42, sourceSha: SOURCE_SHA, tag: TAG },
    );

    for (const changed of [
      { ...latestRelease, id: 43 },
      { ...latestRelease, target_commitish: '0'.repeat(40) },
      { ...latestRelease, immutable: false },
      { ...latestRelease, assets: latestRelease.assets.slice(1) },
    ]) {
      assert.throws(
        () => verifyPlatformFleetLatestReadback({ approvedRelease, latestRelease: changed }),
        /latest release|exact tag|승인 대기 draft/u,
      );
    }
  });

  it('latest projection의 짧은 지연만 제한적으로 재확인한다', async () => {
    const assets = [
      { id: 1, name: 'platform-release.json', size: 100 },
      { id: 2, name: 'seorilabs-platform-sdk-0.7.0.tgz', size: 200 },
      { id: 3, name: 'seorilabs-platform-gdscript-0.7.0.tar.gz', size: 300 },
      { id: 4, name: 'seorilabs-platform-gdscript-0.7.0.tar.gz.sha256', size: 107 },
      { id: 5, name: 'fleet-approved.json', size: 400 },
    ];
    const approvedRelease = {
      id: 42,
      tag_name: TAG,
      target_commitish: SOURCE_SHA,
      draft: false,
      prerelease: false,
      immutable: true,
      assets,
    };
    let reads = 0;
    let waits = 0;
    const result = await readPlatformFleetLatest({
      approvedRelease,
      fetchImpl: async () => {
        reads += 1;
        return jsonResponse(reads === 1 ? { ...approvedRelease, id: 41 } : approvedRelease);
      },
      token: 'credential-canary-token',
      waitImpl: async (milliseconds) => {
        assert.equal(milliseconds, 500);
        waits += 1;
      },
    });
    assert.deepEqual(result, {
      latest: true,
      releaseId: 42,
      sourceSha: SOURCE_SHA,
      tag: TAG,
    });
    assert.equal(reads, 2);
    assert.equal(waits, 1);
  });

  it('승인 asset이 없는 공개 release는 GitHub latest여도 Fleet latest가 아니다', () => {
    const release = {
      id: 42,
      tag_name: TAG,
      target_commitish: SOURCE_SHA,
      draft: false,
      prerelease: false,
      immutable: true,
      assets: [{ id: 1, name: 'platform-release.json', size: 100 }],
    };
    assert.throws(
      () => verifyPlatformFleetLatestReadback({
        approvedRelease: release,
        latestRelease: structuredClone(release),
      }),
      /Fleet approval release/u,
    );
  });

  it('임의 key fixture는 mutation 없는 pure verifier에서만 검증한다', async (test) => {
    const local = await localFixture(test);
    const verified = verifyLocal(local);
    assert.equal(verified.approvalSha256, `sha256:${sha256(local.approvalBytes)}`);
    assert.equal(verified.keyId, local.approval.keyId);
    assert.equal(verified.manifestSha256, `sha256:${sha256(local.manifestBytes)}`);
    assert.equal(verified.policyAttestation.repositoryId, REPOSITORY_ID);
    assert.equal(verified.policyAttestation.rulesetId, RULESET_ID);
    assert.equal(verified.policyAttestation.rulesetUpdatedAt, RULESET_UPDATED_AT);
    assert.equal(verified.sourceSha, SOURCE_SHA);
    assert.equal(verified.tag, TAG);

    const changed = structuredClone(local.approval);
    changed.signature = `${changed.signature.slice(0, -2)}AA`;
    const changedBytes = Buffer.from(`${JSON.stringify(changed)}\n`, 'utf8');
    assert.throws(() => verifyLocal(local, { approvalBytes: changedBytes }), /서명이 올바르지 않습니다/u);
    assert.throws(
      () => verifyLocal(local, { grant: { ...local.grant, maxUses: 2 } }),
      /grant가 exact 실행 경계/u,
    );
    assert.throws(
      () => verifyLocal(local, { trustRegistryExpectedSha256: '0'.repeat(64) }),
      /policy digest/u,
    );
    const bypassPayload = structuredClone(local.policyPayload);
    bypassPayload.ruleset.bypassActors = [{ actorType: 'Team', actorId: '1' }];
    assert.throws(
      () => verifyLocal(local, {
        policyAttestationBytes: local.createPolicyAttestationBytes(bypassPayload),
      }),
      /exact publish 정책 경계/u,
    );
    const expiredPayload = structuredClone(local.policyPayload);
    expiredPayload.observedAt = new Date(Date.now() - 2 * 60 * 1000).toISOString();
    expiredPayload.expiresAt = new Date(Date.now() - 1000).toISOString();
    assert.throws(
      () => verifyLocal(local, {
        policyAttestationBytes: local.createPolicyAttestationBytes(expiredPayload),
      }),
      /exact publish 정책 경계/u,
    );
    const invalidSignature = JSON.parse(local.policyAttestationBytes.toString('utf8'));
    invalidSignature.signature = Buffer.alloc(64).toString('base64');
    assert.throws(
      () => verifyLocal(local, {
        policyAttestationBytes: Buffer.from(`${JSON.stringify(invalidSignature)}\n`, 'utf8'),
      }),
      /attestation 서명이 올바르지 않습니다/u,
    );
  });

  it('production manifest와 policy attestation symlink는 API 전에 거부한다', async (test) => {
    const local = await localFixture(test);
    const link = join(local.directory, 'manifest-link.json');
    await symlink(local.paths.manifest, link);
    await assert.rejects(
      publishPlatformFleetApproval({
        approvalPath: local.paths.approval,
        grant: local.grant,
        manifestPath: link,
        policyAttestationPath: local.paths.policyAttestation,
        repository: 'seorilabs/platform',
        token: 'credential-canary-token',
      }),
      /symbolic link/u,
    );
    const policyLink = join(local.directory, 'policy-link.json');
    await symlink(local.paths.policyAttestation, policyLink);
    await assert.rejects(
      publishPlatformFleetApproval({
        approvalPath: local.paths.approval,
        grant: local.grant,
        manifestPath: local.paths.manifest,
        policyAttestationPath: policyLink,
        repository: 'seorilabs/platform',
        token: 'credential-canary-token',
      }),
      /symbolic link/u,
    );
  });

  it('조직 강제 immutable 설정과 조직 소유 tag ruleset을 read-only로 증명한다', async (test) => {
    const local = await localFixture(test);
    const remote = githubFixture(local);
    const verified = verifyLocal(local);
    const policy = await verifyPlatformFleetImmutablePolicy({
      fetchImpl: remote.fetchImpl,
      policyAttestation: verified.policyAttestation,
      token: 'credential-canary-token',
    });
    assert.equal(policy.enforcedByOwner, true);
    assert.equal(policy.immutableReleasesEnabled, true);
    assert.equal(policy.policyOwner, 'seorilabs');
    assert.equal(policy.rulesetId, 88);
    assert.match(policy.policyDigest, /^sha256:[0-9a-f]{64}$/u);
    assert.equal(remote.calls.some(({ options }) => options.method), false);
    assert.equal(
      remote.calls.some(({ url }) => url.includes('/orgs/seorilabs/rulesets/')),
      false,
    );
  });

  it('정책 digest는 정책과 무관한 GitHub 응답 필드에 영향받지 않는다', async (test) => {
    const local = await localFixture(test);
    const verified = verifyLocal(local);
    const first = await verifyPlatformFleetImmutablePolicy({
      fetchImpl: githubFixture(local, {
        repositoryVolatile: '2026-08-29T00:00:00.000Z',
        rulesetVolatile: 'RRS_fixture_one',
      }).fetchImpl,
      policyAttestation: verified.policyAttestation,
      token: 'credential-canary-token',
    });
    const second = await verifyPlatformFleetImmutablePolicy({
      fetchImpl: githubFixture(local, {
        repositoryVolatile: '2026-08-29T00:01:00.000Z',
        rulesetVolatile: 'RRS_fixture_two',
      }).fetchImpl,
      policyAttestation: verified.policyAttestation,
      token: 'credential-canary-token',
    });
    assert.equal(second.policyDigest, first.policyDigest);
  });

  for (const [name, remoteOptions, pattern] of [
    ['immutable releases 비활성', { immutableEnabled: false }, /조직 소유 immutable/u],
    ['owner 강제 아님', { ownerEnforced: false }, /조직 소유 immutable/u],
    ['tag ruleset 누락', { missingRuleset: true }, /정확히 하나/u],
    ['repository 소유 ruleset', { repositoryRuleset: true }, /정확히 하나/u],
    ['repository identity 불일치', { repositoryId: '999' }, /repository identity/u],
  ]) {
    it(`${name}이면 read-only policy verifier가 fail-closed한다`, async (test) => {
      const local = await localFixture(test);
      const remote = githubFixture(local, remoteOptions);
      const verified = verifyLocal(local);
      await assert.rejects(
        verifyPlatformFleetImmutablePolicy({
          fetchImpl: remote.fetchImpl,
          policyAttestation: verified.policyAttestation,
          token: 'credential-canary-token',
        }),
        pattern,
      );
      assert.equal(remote.calls.some(({ options }) => options.method), false);
    });
  }

  it('서명된 ruleset revision 이후 live readback 변경은 fail-closed한다', async (test) => {
    const local = await localFixture(test);
    const remote = githubFixture(local, { policyChange: true });
    const verified = verifyLocal(local);
    await verifyPlatformFleetImmutablePolicy({
      fetchImpl: remote.fetchImpl,
      policyAttestation: verified.policyAttestation,
      token: 'credential-canary-token',
    });
    await assert.rejects(
      verifyPlatformFleetImmutablePolicy({
        fetchImpl: remote.fetchImpl,
        policyAttestation: verified.policyAttestation,
        token: 'credential-canary-token',
      }),
      /정확히 하나/u,
    );
  });
});
