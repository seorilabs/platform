import assert from 'node:assert/strict';
import { generateKeyPairSync, verify } from 'node:crypto';
import { describe, it } from 'node:test';

import { parseTrustedPlatformReleaseKeys } from './platform-fleet-approval.mjs';
import { platformReleaseIdentity } from './platform-fleet-reconciler.mjs';
import { platformFleetPolicyAttestationBytes } from './publish-platform-fleet-approval.mjs';
import { createPlatformFleetCanaryEvidence } from './sign-platform-fleet-canary-evidence.mjs';
import { createPlatformFleetPolicyAttestation } from './sign-platform-fleet-policy-attestation.mjs';

const PLATFORM_SOURCE_SHA = 'a'.repeat(40);
const WORKFLOW_SOURCE_SHA = 'b'.repeat(40);

function manifestContent() {
  const manifest = {
    schemaVersion: 1,
    release: {
      tag: 'v0.7.0',
      sourceSha: PLATFORM_SOURCE_SHA,
      baseSourceSha: 'c'.repeat(40),
    },
    sdk: {
      typescript: {
        package: '@seorilabs/platform-sdk',
        registry: 'https://registry.npmjs.org',
        version: '0.7.0',
        artifact: { name: 'seorilabs-platform-sdk-0.7.0.tgz', sha256: 'd'.repeat(64), size: 100 },
      },
      gdscript: {
        version: '0.7.0',
        source: 'https://github.com/seorilabs/platform/releases/download/v0.7.0/seorilabs-platform-gdscript-0.7.0.tar.gz',
        treeChecksum: 'e'.repeat(64),
        artifact: { name: 'seorilabs-platform-gdscript-0.7.0.tar.gz', sha256: 'f'.repeat(64), size: 200 },
        checksumArtifact: { name: 'seorilabs-platform-gdscript-0.7.0.tar.gz.sha256', sha256: '1'.repeat(64), size: 100 },
      },
    },
    contract: {
      affectedCapabilities: ['core'],
      affectedConsumers: { cohort: 'backoffice-managed-product-apps', resolution: 'reconcile-time' },
      affectedTracks: ['gdscript', 'typescript'],
      baseRevision: `sha256:${'2'.repeat(64)}`,
      classification: 'implementation-only',
      revision: `sha256:${'3'.repeat(64)}`,
      supportedApiMajor: 1,
    },
  };
  return Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`, 'utf8');
}

function canaryPayload(manifest) {
  const canary = (profile, repositoryId, repositoryFullName, sourceSha, number) => ({
    profile,
    repositoryId,
    repositoryFullName,
    sourceSha,
    staticRun: {
      runId: `10${number}`,
      conclusion: 'success',
      headSha: sourceSha,
      workflowSourceSha: WORKFLOW_SOURCE_SHA,
    },
    buildOnlyRun: {
      runId: `20${number}`,
      conclusion: 'success',
      headSha: sourceSha,
      workflowSourceSha: WORKFLOW_SOURCE_SHA,
      cloudBuildId: `cloud-build-${number}`,
      builderImageDigest: `sha256:${number.repeat(64)}`,
      buildConfigDigest: `sha256:${String(Number(number) + 2).repeat(64)}`,
      artifact: {
        name: `${profile}-release.aab`,
        sha256: `sha256:${String(Number(number) + 4).repeat(64)}`,
        size: 4096 + Number(number),
      },
    },
  });
  return {
    purpose: 'seorilabs-platform-fleet-canary-readback-v1',
    status: 'passed',
    platformRelease: platformReleaseIdentity(manifest),
    workflowBundle: {
      repository: 'seorilabs/.github',
      sourceSha: WORKFLOW_SOURCE_SHA,
      digest: `sha256:${'9'.repeat(64)}`,
    },
    canaries: [
      canary('godot', '1265192029', 'seorilabs/lizard-tycoon', '4'.repeat(40), '1'),
      canary('react-native', '1250442131', 'seorilabs/happy-farm', '5'.repeat(40), '2'),
    ],
  };
}

function trustedKeys(keyId, publicKey) {
  return parseTrustedPlatformReleaseKeys({
    schemaVersion: 1,
    keys: [{
      algorithm: 'Ed25519',
      keyId,
      publicKeyPem: publicKey.export({ type: 'spki', format: 'pem' }),
      status: 'ACTIVE',
    }],
  });
}

describe('Platform Fleet attestation signers', () => {
  it('RN과 Godot exact readback payload만 Ed25519 canary evidence로 만든다', () => {
    const manifest = manifestContent();
    const { privateKey } = generateKeyPairSync('ed25519');
    const evidence = createPlatformFleetCanaryEvidence({
      keyId: 'canary-readback-fixture-1',
      manifestContent: manifest,
      payload: canaryPayload(manifest),
      privateKey,
    });
    assert.equal(evidence.payload.canaries.length, 2);
    assert.equal(evidence.signature.length > 80, true);

    const invalid = canaryPayload(manifest);
    invalid.canaries[1].buildOnlyRun.conclusion = 'failure';
    assert.throws(
      () => createPlatformFleetCanaryEvidence({
        keyId: 'canary-readback-fixture-1',
        manifestContent: manifest,
        payload: invalid,
        privateKey,
      }),
      /conclusion은 success/u,
    );
  });

  it('정책 attestation은 승인 key와 live immutable snapshot을 4분에 결합한다', () => {
    const keyId = 'fixture-fleet-release-key-1';
    const { privateKey, publicKey } = generateKeyPairSync('ed25519');
    const approvalBytes = Buffer.from('{"approved":true}\n', 'utf8');
    const now = new Date('2026-09-04T00:00:00.000Z');
    const attestation = createPlatformFleetPolicyAttestation({
      approval: {
        keyId,
        payload: { releaseTag: 'v0.7.0', sourceSha: PLATFORM_SOURCE_SHA },
      },
      approvalBytes,
      immutablePolicy: {
        repositoryId: '1317999271',
        immutableReleases: { enabled: true, enforcedByOwner: true },
        ruleset: {
          id: '21819735',
          name: 'Immutable Platform release tags',
          sourceType: 'Organization',
          source: 'seorilabs',
          target: 'tag',
          enforcement: 'active',
          bypassActors: [],
          refName: { include: ['refs/tags/v*'], exclude: [] },
          ruleTypes: ['deletion', 'update'],
          updatedAt: '2026-09-03T20:02:32.423Z',
        },
      },
      privateKey,
      trustedPublicKeys: trustedKeys(keyId, publicKey),
      now,
    });
    assert.equal(attestation.payload.observedAt, now.toISOString());
    assert.equal(attestation.payload.expiresAt, '2026-09-04T00:04:00.000Z');
    assert.equal(
      verify(
        null,
        platformFleetPolicyAttestationBytes(attestation.payload),
        publicKey,
        Buffer.from(attestation.signature, 'base64'),
      ),
      true,
    );
  });

  it('정책 signer는 다른 private key를 승인 key로 사용할 수 없다', () => {
    const keyId = 'fixture-fleet-release-key-1';
    const trusted = generateKeyPairSync('ed25519');
    const other = generateKeyPairSync('ed25519');
    assert.throws(
      () => createPlatformFleetPolicyAttestation({
        approval: { keyId, payload: { releaseTag: 'v0.7.0', sourceSha: PLATFORM_SOURCE_SHA } },
        approvalBytes: Buffer.from('{}\n', 'utf8'),
        immutablePolicy: {
          repositoryId: '1317999271',
          immutableReleases: { enabled: true, enforcedByOwner: true },
          ruleset: {},
        },
        privateKey: other.privateKey,
        trustedPublicKeys: trustedKeys(keyId, trusted.publicKey),
      }),
      /승인 key ID와 일치하지 않습니다/u,
    );
  });
});
