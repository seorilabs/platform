#!/usr/bin/env node

import {
  createPrivateKey,
  sign,
  verify,
} from 'node:crypto';
import { constants, readFileSync } from 'node:fs';
import { open, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { parseTrustedPlatformReleaseKeys } from './platform-fleet-approval.mjs';
import { verifyPlatformReleaseApproval } from './platform-fleet-reconciler.mjs';
import { sha256 } from './platform-release-lib.mjs';
import {
  platformFleetPolicyAttestationBytes,
  readPlatformFleetImmutablePolicy,
} from './publish-platform-fleet-approval.mjs';

const MAX_INPUT_BYTES = 1024 * 1024;
const MAX_TOKEN_BYTES = 64 * 1024;
const ATTESTATION_TTL_MS = 4 * 60 * 1000;
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const trustRegistryPath = resolve(root, '.github/platform-fleet-trusted-release-keys.json');

function parseFd(value, label) {
  const fd = Number.parseInt(value, 10);
  if (!Number.isSafeInteger(fd) || fd < 3 || String(fd) !== value) {
    throw new Error(`${label}는 3 이상의 상속된 file descriptor여야 합니다.`);
  }
  return fd;
}

function parseArguments(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!key?.startsWith('--') || value === undefined || Object.hasOwn(options, key)) {
      throw new Error('Platform policy attestation signer 인자가 올바르지 않습니다.');
    }
    options[key] = value;
  }
  const required = ['--approval', '--manifest', '--output', '--private-key-fd', '--token-fd'];
  if (
    Object.keys(options).some((key) => !required.includes(key))
    || required.some((key) => !options[key])
  ) {
    throw new Error('Platform policy attestation signer 필수 인자가 없습니다.');
  }
  const privateKeyFd = parseFd(options['--private-key-fd'], 'private key');
  const tokenFd = parseFd(options['--token-fd'], 'GitHub token');
  if (privateKeyFd === tokenFd) {
    throw new Error('private key와 GitHub token FD는 달라야 합니다.');
  }
  return { ...options, privateKeyFd, tokenFd };
}

async function readBoundedFile(path, label) {
  const absolute = resolve(path);
  let handle;
  try {
    handle = await open(absolute, constants.O_RDONLY | constants.O_NOFOLLOW);
    const metadata = await handle.stat();
    if (!metadata.isFile() || metadata.size < 1 || metadata.size > MAX_INPUT_BYTES) {
      throw new Error(`${label}은 1MiB 이하의 비어 있지 않은 일반 파일이어야 합니다.`);
    }
    const bytes = await handle.readFile();
    if (bytes.length !== metadata.size) {
      throw new Error(`${label} 크기가 읽기 도중 변경되었습니다.`);
    }
    return bytes;
  } catch (error) {
    if (error?.code === 'ELOOP') {
      throw new Error(`${label}은 symbolic link일 수 없습니다.`, { cause: error });
    }
    throw error;
  } finally {
    await handle?.close();
  }
}

function parseJson(bytes, label) {
  try {
    return JSON.parse(bytes.toString('utf8'));
  } catch (error) {
    throw new Error(`${label} JSON을 해석하지 못했습니다.`, { cause: error });
  }
}

function privateEd25519Key(value) {
  let key;
  try {
    key = value?.type === 'private' ? value : createPrivateKey(value);
  } catch (error) {
    throw new Error('Platform policy private key를 해석하지 못했습니다.', { cause: error });
  }
  if (key.asymmetricKeyType !== 'ed25519') {
    throw new Error('Platform policy private key는 Ed25519여야 합니다.');
  }
  return key;
}

export function createPlatformFleetPolicyAttestation({
  approval,
  approvalBytes,
  immutablePolicy,
  privateKey,
  trustedPublicKeys,
  now = new Date(),
}) {
  if (!Number.isFinite(now.getTime())) {
    throw new Error('Platform policy attestation 시각이 올바르지 않습니다.');
  }
  const key = privateEd25519Key(privateKey);
  const payload = {
    purpose: 'seorilabs-platform-release-policy-attestation-v1',
    repository: 'seorilabs/platform',
    repositoryId: immutablePolicy.repositoryId,
    releaseTag: approval.payload.releaseTag,
    sourceSha: approval.payload.sourceSha,
    approvalSha256: `sha256:${sha256(approvalBytes)}`,
    observedAt: now.toISOString(),
    expiresAt: new Date(now.getTime() + ATTESTATION_TTL_MS).toISOString(),
    immutableReleases: immutablePolicy.immutableReleases,
    ruleset: immutablePolicy.ruleset,
  };
  const signature = sign(null, platformFleetPolicyAttestationBytes(payload), key);
  const trustedKey = trustedPublicKeys.get(approval.keyId);
  if (
    !trustedKey
    || signature.length !== 64
    || !verify(null, platformFleetPolicyAttestationBytes(payload), trustedKey, signature)
  ) {
    throw new Error('Platform policy private key가 승인 key ID와 일치하지 않습니다.');
  }
  return Object.freeze({
    schemaVersion: 1,
    algorithm: 'Ed25519',
    keyId: approval.keyId,
    payload: Object.freeze(payload),
    signature: signature.toString('base64'),
  });
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  let privateKeyBytes;
  let tokenBytes;
  try {
    privateKeyBytes = readFileSync(options.privateKeyFd);
    tokenBytes = readFileSync(options.tokenFd);
    if (privateKeyBytes.length < 1 || privateKeyBytes.length > 64 * 1024) {
      throw new Error('Platform policy private key 입력 크기가 올바르지 않습니다.');
    }
    if (tokenBytes.length < 1 || tokenBytes.length > MAX_TOKEN_BYTES) {
      throw new Error('GitHub token 입력 크기가 올바르지 않습니다.');
    }
    const [approvalBytes, manifestBytes, registryBytes] = await Promise.all([
      readBoundedFile(options['--approval'], 'fleet-approved.json'),
      readBoundedFile(options['--manifest'], 'platform-release.json'),
      readBoundedFile(trustRegistryPath, 'pinned trusted release key registry'),
    ]);
    const approval = parseJson(approvalBytes, 'fleet-approved.json');
    const trustedPublicKeys = parseTrustedPlatformReleaseKeys(
      parseJson(registryBytes, 'pinned trusted release key registry'),
    );
    verifyPlatformReleaseApproval(manifestBytes, approval, trustedPublicKeys);
    const immutablePolicy = await readPlatformFleetImmutablePolicy({
      token: tokenBytes.toString('utf8').trim(),
    });
    const attestation = createPlatformFleetPolicyAttestation({
      approval,
      approvalBytes,
      immutablePolicy,
      privateKey: privateKeyBytes,
      trustedPublicKeys,
    });
    await writeFile(resolve(options['--output']), `${JSON.stringify(attestation, null, 2)}\n`, {
      encoding: 'utf8',
      flag: 'wx',
      mode: 0o600,
    });
    process.stdout.write(`Platform policy attestation 생성 완료: ${attestation.keyId}\n`);
  } finally {
    privateKeyBytes?.fill(0);
    tokenBytes?.fill(0);
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
