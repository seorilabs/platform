import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { generateKeyPairSync } from 'node:crypto';
import {
  mkdtemp,
  open,
  readFile,
  rm,
  writeFile,
} from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { describe, it } from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  createPlatformReleaseApproval,
  parseTrustedPlatformReleaseKeys,
} from './platform-fleet-approval.mjs';
import { evaluatePlatformReleaseGate } from './platform-fleet-gate.mjs';

const NOW = '2026-08-28T00:00:00.000Z';
const SOURCE_SHA = 'a'.repeat(40);
const BASE_SHA = 'b'.repeat(40);
const REPOSITORY_SOURCE_SHA = 'c'.repeat(40);
const CONTRACT_REVISION = `sha256:${'d'.repeat(64)}`;
const BASE_CONTRACT_REVISION = `sha256:${'e'.repeat(64)}`;
const TS_ARTIFACT_SHA = '1'.repeat(64);
const GD_ARTIFACT_SHA = '2'.repeat(64);
const GD_TREE_SHA = '3'.repeat(64);
const GD_CHECKSUM_SHA = '4'.repeat(64);
const scriptsDirectory = dirname(fileURLToPath(import.meta.url));

function releaseManifest() {
  const version = '0.7.0';
  const gdscriptArtifact = `seorilabs-platform-gdscript-${version}.tar.gz`;
  return `${JSON.stringify({
    schemaVersion: 1,
    release: { tag: `v${version}`, sourceSha: SOURCE_SHA, baseSourceSha: BASE_SHA },
    sdk: {
      typescript: {
        package: '@seorilabs/platform-sdk',
        version: '0.5.0',
        registry: 'https://npm.pkg.github.com',
        artifact: {
          name: 'seorilabs-platform-sdk-0.5.0.tgz',
          sha256: TS_ARTIFACT_SHA,
          size: 100,
        },
      },
      gdscript: {
        version,
        source: `https://github.com/seorilabs/platform/releases/download/v${version}/${gdscriptArtifact}`,
        treeChecksum: GD_TREE_SHA,
        artifact: { name: gdscriptArtifact, sha256: GD_ARTIFACT_SHA, size: 200 },
        checksumArtifact: {
          name: `${gdscriptArtifact}.sha256`,
          sha256: GD_CHECKSUM_SHA,
          size: 96,
        },
      },
    },
    contract: {
      revision: CONTRACT_REVISION,
      baseRevision: BASE_CONTRACT_REVISION,
      classification: 'implementation-only',
      supportedApiMajor: 1,
      affectedTracks: ['gdscript', 'typescript'],
      affectedCapabilities: ['core'],
    },
  }, null, 2)}\n`;
}

function keyMaterial() {
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const publicKeyPem = publicKey.export({ type: 'spki', format: 'pem' }).toString();
  const trustedPublicKeys = parseTrustedPlatformReleaseKeys({
    schemaVersion: 1,
    keys: [{
      keyId: 'fleet-test-1',
      algorithm: 'Ed25519',
      status: 'ACTIVE',
      publicKeyPem,
    }],
  });
  return { privateKey, publicKeyPem, trustedPublicKeys };
}

function currentObservation() {
  return {
    observationId: 'observation-1',
    repositoryId: '101',
    repositoryFullName: 'seorilabs/example',
    configRevision: 'config-7',
    configState: 'ACTIVE',
    sourceSha: REPOSITORY_SOURCE_SHA,
    snapshotDigest: `sha256:${'f'.repeat(64)}`,
    sourceType: 'backoffice',
    observedAt: '2026-08-27T23:59:00.000Z',
    evidence: {
      officialSdks: [{
        track: 'typescript',
        version: '0.5.0',
        artifactSha256: TS_ARTIFACT_SHA,
        contractRevision: CONTRACT_REVISION,
      }],
      customHttpTracks: [],
      unmanagedTracks: [],
    },
    exception: null,
  };
}

function gateInput() {
  const manifestContent = releaseManifest();
  const keys = keyMaterial();
  return {
    manifestContent,
    approval: createPlatformReleaseApproval({
      keyId: 'fleet-test-1',
      manifestContent,
      privateKey: keys.privateKey,
    }),
    trustedPublicKeys: keys.trustedPublicKeys,
    expectedConsumer: {
      repositoryId: '101',
      repositoryFullName: 'seorilabs/example',
      tracks: ['typescript'],
    },
    observation: currentObservation(),
    existingWorkItems: [],
    now: NOW,
    repositoryId: '101',
    sourceSha: REPOSITORY_SOURCE_SHA,
    configRevision: 'config-7',
  };
}

describe('Platform Fleet approval signer', () => {
  it('broker가 전달한 Ed25519 key로 core가 검증하는 approval을 만든다', () => {
    const input = gateInput();
    const receipt = evaluatePlatformReleaseGate(input);
    assert.equal(receipt.status, 'PASS');
    assert.equal(receipt.repositoryId, '101');
    assert.equal(receipt.sourceSha, REPOSITORY_SOURCE_SHA);
    assert.equal(receipt.configRevision, 'config-7');
    assert.match(receipt.receiptDigest, /^sha256:[0-9a-f]{64}$/u);
    assert.equal(receipt.sdkBindings[0].artifactSha256, TS_ARTIFACT_SHA);
  });

  it('revoked key만 있는 registry와 중복 key ID를 거부한다', () => {
    const { publicKeyPem } = keyMaterial();
    assert.throws(() => parseTrustedPlatformReleaseKeys({
      schemaVersion: 1,
      keys: [{
        keyId: 'revoked', algorithm: 'Ed25519', status: 'REVOKED', publicKeyPem,
      }],
    }), /ACTIVE/u);
    assert.throws(() => parseTrustedPlatformReleaseKeys({
      schemaVersion: 1,
      keys: [
        { keyId: 'same', algorithm: 'Ed25519', status: 'ACTIVE', publicKeyPem },
        { keyId: 'same', algorithm: 'Ed25519', status: 'REVOKED', publicKeyPem },
      ],
    }), /중복/u);
  });

  it('CLI는 private key를 inherited FD로만 받고 argv, env, 출력에 남기지 않는다', async (test) => {
    const directory = await mkdtemp(join(tmpdir(), 'platform-fleet-signer-'));
    test.after(() => rm(directory, { recursive: true, force: true }));
    const manifestPath = join(directory, 'platform-release.json');
    const privateKeyPath = join(directory, 'approval-private.pem');
    const approvalPath = join(directory, 'fleet-approved.json');
    const { privateKey } = generateKeyPairSync('ed25519');
    const privatePem = privateKey.export({ type: 'pkcs8', format: 'pem' }).toString();
    await writeFile(manifestPath, releaseManifest());
    await writeFile(privateKeyPath, privatePem, { mode: 0o600 });
    const keyFile = await open(privateKeyPath, 'r');
    try {
      const result = spawnSync(process.execPath, [
        resolve(scriptsDirectory, 'sign-platform-fleet-release.mjs'),
        '--manifest', manifestPath,
        '--key-id', 'fleet-test-cli',
        '--private-key-fd', '3',
        '--output', approvalPath,
      ], {
        encoding: 'utf8',
        env: { PATH: process.env.PATH },
        stdio: ['ignore', 'pipe', 'pipe', keyFile.fd],
      });
      assert.equal(result.status, 0, result.stderr);
      assert.doesNotMatch(result.stdout, /PRIVATE KEY/u);
      assert.doesNotMatch(result.stderr, /PRIVATE KEY/u);
      assert.equal(result.stdout.includes(privatePem), false);
      assert.equal(result.stderr.includes(privatePem), false);
      const approval = JSON.parse(await readFile(approvalPath, 'utf8'));
      assert.equal(approval.keyId, 'fleet-test-cli');
      assert.equal(Object.hasOwn(approval, 'privateKey'), false);
    } finally {
      await keyFile.close();
    }
  });
});

describe('Platform release build gate', () => {
  it('CLI가 Backoffice snapshot과 public key registry에서 receipt를 만든다', async (test) => {
    const directory = await mkdtemp(join(tmpdir(), 'platform-fleet-gate-'));
    test.after(() => rm(directory, { recursive: true, force: true }));
    const manifestContent = releaseManifest();
    const { privateKey, publicKeyPem } = keyMaterial();
    const approval = createPlatformReleaseApproval({
      keyId: 'fleet-test-cli-gate',
      manifestContent,
      privateKey,
    });
    const files = {
      manifest: join(directory, 'platform-release.json'),
      approval: join(directory, 'fleet-approved.json'),
      keys: join(directory, 'trusted-keys.json'),
      snapshot: join(directory, 'snapshot.json'),
      output: join(directory, 'receipt.json'),
    };
    await Promise.all([
      writeFile(files.manifest, manifestContent),
      writeFile(files.approval, JSON.stringify(approval)),
      writeFile(files.keys, JSON.stringify({
        schemaVersion: 1,
        keys: [{
          keyId: 'fleet-test-cli-gate',
          algorithm: 'Ed25519',
          status: 'ACTIVE',
          publicKeyPem,
        }],
      })),
      writeFile(files.snapshot, JSON.stringify({
        schemaVersion: 1,
        now: NOW,
        expectedConsumers: [{
          repositoryId: '101',
          repositoryFullName: 'seorilabs/example',
          tracks: ['typescript'],
        }],
        observations: [currentObservation()],
        existingWorkItems: [],
      })),
    ]);
    const result = spawnSync(process.execPath, [
      resolve(scriptsDirectory, 'platform-fleet-gate.mjs'),
      '--manifest', files.manifest,
      '--approval', files.approval,
      '--trusted-keys', files.keys,
      '--snapshot', files.snapshot,
      '--repository-id', '101',
      '--source-sha', REPOSITORY_SOURCE_SHA,
      '--config-revision', 'config-7',
      '--output', files.output,
    ], { encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /Platform release gate PASS/u);
    const receipt = JSON.parse(await readFile(files.output, 'utf8'));
    assert.equal(receipt.status, 'PASS');
    assert.equal(receipt.repositoryId, '101');
    assert.match(receipt.receiptDigest, /^sha256:[0-9a-f]{64}$/u);
  });

  it('repo SHA나 ACTIVE config revision이 다르면 승인된 SDK여도 중단한다', () => {
    const input = gateInput();
    assert.throws(
      () => evaluatePlatformReleaseGate({ ...input, sourceSha: '9'.repeat(40) }),
      /source SHA/u,
    );
    assert.throws(
      () => evaluatePlatformReleaseGate({ ...input, configRevision: 'config-8' }),
      /config revision/u,
    );
  });

  it('artifact 또는 contract가 stale이면 receipt를 만들지 않는다', () => {
    const input = gateInput();
    const stale = structuredClone(input.observation);
    stale.evidence.officialSdks[0].artifactSha256 = '9'.repeat(64);
    assert.throws(
      () => evaluatePlatformReleaseGate({ ...input, observation: stale }),
      /차단/u,
    );
  });

  it('fixture observation은 release gate write 경계에서 거부한다', () => {
    const input = gateInput();
    const fixture = structuredClone(input.observation);
    fixture.sourceType = 'fixture';
    // Core plan 자체는 fixture dry-run을 지원하지만 release receipt는 Backoffice readback만 허용한다.
    assert.throws(
      () => evaluatePlatformReleaseGate({ ...input, observation: fixture }),
      /Backoffice/u,
    );
  });
});
