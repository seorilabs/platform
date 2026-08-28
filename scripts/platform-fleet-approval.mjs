import {
  createPrivateKey,
  createPublicKey,
  sign,
} from 'node:crypto';

import {
  platformReleaseApprovalBytes,
  platformReleaseApprovalPayload,
} from './platform-fleet-reconciler.mjs';

const KEY_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$/u;

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

export function createPlatformReleaseApproval({ keyId, manifestContent, privateKey }) {
  const normalizedKeyId = validKeyId(keyId);
  const payload = platformReleaseApprovalPayload(manifestContent);
  const signature = sign(
    null,
    platformReleaseApprovalBytes(manifestContent),
    privateEd25519Key(privateKey),
  );
  if (signature.length !== 64) {
    throw new Error('Fleet approval signature 길이가 올바르지 않습니다.');
  }
  return Object.freeze({
    schemaVersion: 1,
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
