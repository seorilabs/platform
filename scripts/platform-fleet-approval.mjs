import {
  createHash,
  createPrivateKey,
  createPublicKey,
  sign,
  verify,
} from 'node:crypto';

import {
  platformReleaseIdentity,
  platformReleaseApprovalBytes,
  platformReleaseApprovalPayload,
} from './platform-fleet-reconciler.mjs';

const KEY_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$/u;
const SHA256_REVISION_PATTERN = /^sha256:[0-9a-f]{64}$/u;
const CANARY_EVIDENCE_PURPOSE = 'seorilabs-platform-fleet-canary-readback-v1';
const CANARY_KEY_REGISTRY_PURPOSE = 'seorilabs-platform-fleet-canary-readback-keys-v1';

function isRecord(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function exactKeys(value, expected, label) {
  if (!isRecord(value)) {
    throw new Error(`${label} 형식이 올바르지 않습니다.`);
  }
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  if (JSON.stringify(actual) !== JSON.stringify(wanted)) {
    throw new Error(`${label} 필드가 올바르지 않습니다.`);
  }
}

function validKeyId(value) {
  if (typeof value !== 'string' || !KEY_ID_PATTERN.test(value)) {
    throw new Error('Fleet approval key ID가 올바르지 않습니다.');
  }
  return value;
}

function canonicalize(value) {
  if (Array.isArray(value)) {
    return value.map(canonicalize);
  }
  if (isRecord(value)) {
    return Object.fromEntries(
      Object.keys(value).sort().map((key) => [key, canonicalize(value[key])]),
    );
  }
  return value;
}

function canonicalBytes(value) {
  return Buffer.from(JSON.stringify(canonicalize(value)), 'utf8');
}

function sha256Revision(value) {
  return `sha256:${createHash('sha256').update(value).digest('hex')}`;
}

function privateEd25519Key(value) {
  let key;
  try {
    key = value?.type === 'private' ? value : createPrivateKey(value);
  } catch (error) {
    throw new Error('Fleet approval private key를 해석하지 못했습니다.', { cause: error });
  }
  if (key.type !== 'private' || key.asymmetricKeyType !== 'ed25519') {
    throw new Error('Fleet approval private key는 Ed25519여야 합니다.');
  }
  return key;
}

function publicEd25519Key(value) {
  let key;
  try {
    key = value?.type === 'public' ? value : createPublicKey(value);
  } catch (error) {
    throw new Error('Fleet approval public key를 해석하지 못했습니다.', { cause: error });
  }
  if (key.type !== 'public' || key.asymmetricKeyType !== 'ed25519') {
    throw new Error('Fleet approval public key는 Ed25519여야 합니다.');
  }
  return key;
}

export function platformFleetCanaryEvidenceBytes(payload) {
  exactKeys(
    payload,
    ['canaries', 'platformRelease', 'purpose', 'status', 'workflowBundle'],
    'Fleet canary evidence payload',
  );
  return canonicalBytes(payload);
}

function activeTrustedKey(trustedPublicKeys, keyId, label) {
  if (!(trustedPublicKeys instanceof Map)) {
    throw new Error(`${label} trustedPublicKeys는 명시적인 Map이어야 합니다.`);
  }
  const key = trustedPublicKeys.get(keyId);
  if (!key) {
    throw new Error(`신뢰하지 않는 ${label} key입니다: ${keyId}`);
  }
  return publicEd25519Key(key);
}

export function verifyPlatformFleetCanaryEvidence({
  evidence,
  manifestContent,
  trustedCanaryPublicKeys,
}) {
  exactKeys(
    evidence,
    ['algorithm', 'keyId', 'payload', 'schemaVersion', 'signature'],
    'Fleet canary evidence',
  );
  if (evidence.schemaVersion !== 1 || evidence.algorithm !== 'Ed25519') {
    throw new Error('지원하지 않는 Fleet canary evidence 형식입니다.');
  }
  const keyId = validKeyId(evidence.keyId);
  if (
    typeof evidence.signature !== 'string'
    || !/^[A-Za-z0-9+/]+={0,2}$/u.test(evidence.signature)
  ) {
    throw new Error('Fleet canary evidence signature가 올바르지 않습니다.');
  }
  const bytes = platformFleetCanaryEvidenceBytes(evidence.payload);
  if (
    evidence.payload.purpose !== CANARY_EVIDENCE_PURPOSE
    || evidence.payload.status !== 'passed'
  ) {
    throw new Error('Fleet canary evidence purpose 또는 status가 올바르지 않습니다.');
  }
  const expectedRelease = platformReleaseIdentity(manifestContent);
  if (
    JSON.stringify(canonicalize(evidence.payload.platformRelease))
    !== JSON.stringify(canonicalize(expectedRelease))
  ) {
    throw new Error('Fleet canary evidence가 platform release와 일치하지 않습니다.');
  }
  let signature;
  try {
    signature = Buffer.from(evidence.signature, 'base64');
  } catch (error) {
    throw new Error('Fleet canary evidence signature를 해석하지 못했습니다.', { cause: error });
  }
  if (
    signature.length !== 64
    || !verify(
      null,
      bytes,
      activeTrustedKey(trustedCanaryPublicKeys, keyId, 'Fleet canary readback'),
      signature,
    )
  ) {
    throw new Error('Fleet canary evidence 서명이 올바르지 않습니다.');
  }
  const summary = {
    attestationSha256: sha256Revision(canonicalBytes(evidence)),
    readbackKeyId: keyId,
    workflowBundle: evidence.payload.workflowBundle,
    canaries: evidence.payload.canaries,
  };
  // Approval payload validator가 RN/Godot 성공 conclusion, exact source SHA와
  // AAB checksum을 재검사하므로 signed evidence의 의미도 fail-closed한다.
  platformReleaseApprovalPayload(manifestContent, summary);
  return Object.freeze(structuredClone(summary));
}

export function createPlatformReleaseApproval({
  canaryEvidence,
  keyId,
  manifestContent,
  privateKey,
  trustedCanaryPublicKeys,
}) {
  const normalizedKeyId = validKeyId(keyId);
  const evidence = verifyPlatformFleetCanaryEvidence({
    evidence: canaryEvidence,
    manifestContent,
    trustedCanaryPublicKeys,
  });
  const payload = platformReleaseApprovalPayload(manifestContent, evidence);
  const signature = sign(
    null,
    platformReleaseApprovalBytes(manifestContent, evidence),
    privateEd25519Key(privateKey),
  );
  if (signature.length !== 64) {
    throw new Error('Fleet approval signature 길이가 올바르지 않습니다.');
  }
  return Object.freeze({
    schemaVersion: 2,
    algorithm: 'Ed25519',
    keyId: normalizedKeyId,
    payload,
    signature: signature.toString('base64'),
  });
}

export function parseTrustedPlatformReleaseKeys(registry) {
  exactKeys(registry, ['keys', 'schemaVersion'], 'trusted key registry');
  if (registry.schemaVersion !== 1 || !Array.isArray(registry.keys) || registry.keys.length === 0) {
    throw new Error('trusted key registry version 또는 keys가 올바르지 않습니다.');
  }

  const trusted = new Map();
  const seen = new Set();
  for (const entry of registry.keys) {
    exactKeys(
      entry,
      ['algorithm', 'keyId', 'publicKeyPem', 'status'],
      'trusted key registry entry',
    );
    const keyId = validKeyId(entry.keyId);
    if (seen.has(keyId)) {
      throw new Error(`Fleet approval key ID가 중복되었습니다: ${keyId}`);
    }
    seen.add(keyId);
    if (entry.algorithm !== 'Ed25519' || !['ACTIVE', 'REVOKED'].includes(entry.status)) {
      throw new Error(`Fleet approval key ${keyId}의 algorithm 또는 status가 올바르지 않습니다.`);
    }
    const key = publicEd25519Key(entry.publicKeyPem);
    if (entry.status === 'ACTIVE') {
      trusted.set(keyId, key);
    }
  }
  if (trusted.size === 0) {
    throw new Error('ACTIVE Fleet approval public key가 없습니다.');
  }
  return trusted;
}

export function parseTrustedPlatformCanaryKeys(registry) {
  exactKeys(registry, ['keys', 'purpose', 'schemaVersion'], 'trusted canary key registry');
  if (registry.purpose !== CANARY_KEY_REGISTRY_PURPOSE) {
    throw new Error('trusted canary key registry purpose가 올바르지 않습니다.');
  }
  return parseTrustedPlatformReleaseKeys({
    schemaVersion: registry.schemaVersion,
    keys: registry.keys,
  });
}
