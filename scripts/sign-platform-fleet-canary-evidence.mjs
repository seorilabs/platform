#!/usr/bin/env node

import {
  createPrivateKey,
  createPublicKey,
  sign,
} from 'node:crypto';
import { constants, readFileSync } from 'node:fs';
import { open, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  platformFleetCanaryEvidenceBytes,
  verifyPlatformFleetCanaryEvidence,
} from './platform-fleet-approval.mjs';

const MAX_INPUT_BYTES = 1024 * 1024;
const KEY_ID = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$/u;

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
      throw new Error('Fleet canary evidence signer 인자가 올바르지 않습니다.');
    }
    options[key] = value;
  }
  const required = ['--key-id', '--manifest', '--output', '--payload', '--private-key-fd'];
  if (
    Object.keys(options).some((key) => !required.includes(key))
    || required.some((key) => !options[key])
    || !KEY_ID.test(options['--key-id'])
  ) {
    throw new Error('Fleet canary evidence signer 필수 인자가 없거나 올바르지 않습니다.');
  }
  return { ...options, privateKeyFd: parseFd(options['--private-key-fd'], 'private key') };
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
    throw new Error('Fleet canary private key를 해석하지 못했습니다.', { cause: error });
  }
  if (key.asymmetricKeyType !== 'ed25519') {
    throw new Error('Fleet canary private key는 Ed25519여야 합니다.');
  }
  return key;
}

export function createPlatformFleetCanaryEvidence({ keyId, manifestContent, payload, privateKey }) {
  if (!KEY_ID.test(keyId ?? '')) {
    throw new Error('Fleet canary key ID가 올바르지 않습니다.');
  }
  const key = privateEd25519Key(privateKey);
  const evidence = Object.freeze({
    schemaVersion: 1,
    algorithm: 'Ed25519',
    keyId,
    payload,
    signature: sign(null, platformFleetCanaryEvidenceBytes(payload), key).toString('base64'),
  });
  verifyPlatformFleetCanaryEvidence({
    evidence,
    manifestContent,
    trustedCanaryPublicKeys: new Map([[keyId, createPublicKey(key)]]),
  });
  return evidence;
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  let privateKeyBytes;
  try {
    privateKeyBytes = readFileSync(options.privateKeyFd);
    if (privateKeyBytes.length < 1 || privateKeyBytes.length > 64 * 1024) {
      throw new Error('Fleet canary private key 입력 크기가 올바르지 않습니다.');
    }
    const [manifestContent, payloadBytes] = await Promise.all([
      readBoundedFile(options['--manifest'], 'platform-release.json'),
      readBoundedFile(options['--payload'], 'Fleet canary payload'),
    ]);
    const evidence = createPlatformFleetCanaryEvidence({
      keyId: options['--key-id'],
      manifestContent,
      payload: parseJson(payloadBytes, 'Fleet canary payload'),
      privateKey: privateKeyBytes,
    });
    await writeFile(resolve(options['--output']), `${JSON.stringify(evidence, null, 2)}\n`, {
      encoding: 'utf8',
      flag: 'wx',
      mode: 0o600,
    });
    process.stdout.write(`Fleet canary evidence 생성 완료: ${evidence.keyId}\n`);
  } finally {
    privateKeyBytes?.fill(0);
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
