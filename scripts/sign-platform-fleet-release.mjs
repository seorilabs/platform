#!/usr/bin/env node
import { readFileSync } from 'node:fs';
import { lstat, readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';

import {
  createPlatformReleaseApproval,
  parseTrustedPlatformCanaryKeys,
} from './platform-fleet-approval.mjs';

const MAX_MANIFEST_BYTES = 1024 * 1024;
const MAX_INHERITED_INPUT_BYTES = 1024 * 1024;

function parseInheritedFileDescriptor(value, label) {
  const fd = Number.parseInt(value, 10);
  if (!Number.isSafeInteger(fd) || fd < 3 || String(fd) !== value) {
    throw new Error(`${label}는 3 이상의 상속된 file descriptor로만 받을 수 있습니다.`);
  }
  return fd;
}

function parseArguments(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!key?.startsWith('--') || value === undefined || Object.hasOwn(options, key)) {
      throw new Error('Fleet approval signer 인자가 올바르지 않습니다.');
    }
    options[key] = value;
  }
  const required = [
    '--canary-evidence-fd',
    '--key-id',
    '--manifest',
    '--output',
    '--private-key-fd',
    '--trusted-canary-keys-fd',
  ];
  if (
    Object.keys(options).some((key) => !required.includes(key))
    || required.some((key) => !options[key])
  ) {
    throw new Error('Fleet approval signer 필수 인자가 없거나 알 수 없는 인자가 있습니다.');
  }
  const privateKeyFd = parseInheritedFileDescriptor(options['--private-key-fd'], 'private key');
  const canaryEvidenceFd = parseInheritedFileDescriptor(
    options['--canary-evidence-fd'],
    'canary evidence',
  );
  const trustedCanaryKeysFd = parseInheritedFileDescriptor(
    options['--trusted-canary-keys-fd'],
    'trusted canary key registry',
  );
  if (new Set([privateKeyFd, canaryEvidenceFd, trustedCanaryKeysFd]).size !== 3) {
    throw new Error('Fleet approval signer의 상속된 file descriptor는 서로 달라야 합니다.');
  }
  return { ...options, privateKeyFd, canaryEvidenceFd, trustedCanaryKeysFd };
}

async function readManifest(path) {
  const absolute = resolve(path);
  const metadata = await lstat(absolute);
  if (!metadata.isFile() || metadata.isSymbolicLink() || metadata.size > MAX_MANIFEST_BYTES) {
    throw new Error('platform-release.json은 1MiB 이하 일반 파일이어야 합니다.');
  }
  return readFile(absolute);
}

function readInheritedInput(fd, label) {
  const bytes = readFileSync(fd);
  if (bytes.length === 0 || bytes.length > MAX_INHERITED_INPUT_BYTES) {
    throw new Error(`${label} 입력 크기가 올바르지 않습니다.`);
  }
  return bytes;
}

function parseInheritedJson(fd, label) {
  const bytes = readInheritedInput(fd, label);
  try {
    return JSON.parse(bytes.toString('utf8'));
  } catch (error) {
    throw new Error(`${label} JSON을 해석하지 못했습니다.`, { cause: error });
  }
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const manifestContent = await readManifest(options['--manifest']);
  let privateKeyBytes;
  try {
    privateKeyBytes = readInheritedInput(options.privateKeyFd, 'Fleet approval private key');
    if (privateKeyBytes.length === 0 || privateKeyBytes.length > 64 * 1024) {
      throw new Error('Fleet approval private key 입력 크기가 올바르지 않습니다.');
    }
    const canaryEvidence = parseInheritedJson(
      options.canaryEvidenceFd,
      'Fleet canary evidence',
    );
    const trustedCanaryPublicKeys = parseTrustedPlatformCanaryKeys(
      parseInheritedJson(
        options.trustedCanaryKeysFd,
        'trusted canary key registry',
      ),
    );
    const approval = createPlatformReleaseApproval({
      canaryEvidence,
      keyId: options['--key-id'],
      manifestContent,
      privateKey: privateKeyBytes,
      trustedCanaryPublicKeys,
    });
    await writeFile(resolve(options['--output']), `${JSON.stringify(approval, null, 2)}\n`, {
      encoding: 'utf8',
      flag: 'wx',
      mode: 0o600,
    });
    process.stdout.write(`Fleet approval 생성 완료: ${approval.keyId}\n`);
  } finally {
    privateKeyBytes?.fill(0);
  }
}

await main();
