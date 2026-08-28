#!/usr/bin/env node
import { readFileSync } from 'node:fs';
import { lstat, readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';

import { createPlatformReleaseApproval } from './platform-fleet-approval.mjs';

const MAX_MANIFEST_BYTES = 1024 * 1024;

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
  const required = ['--key-id', '--manifest', '--output', '--private-key-fd'];
  if (
    Object.keys(options).some((key) => !required.includes(key))
    || required.some((key) => !options[key])
  ) {
    throw new Error('Fleet approval signer 필수 인자가 없거나 알 수 없는 인자가 있습니다.');
  }
  const fd = Number.parseInt(options['--private-key-fd'], 10);
  if (!Number.isSafeInteger(fd) || fd < 3 || String(fd) !== options['--private-key-fd']) {
    throw new Error('private key는 3 이상의 상속된 file descriptor로만 받을 수 있습니다.');
  }
  return { ...options, fd };
}

async function readManifest(path) {
  const absolute = resolve(path);
  const metadata = await lstat(absolute);
  if (!metadata.isFile() || metadata.isSymbolicLink() || metadata.size > MAX_MANIFEST_BYTES) {
    throw new Error('platform-release.json은 1MiB 이하 일반 파일이어야 합니다.');
  }
  return readFile(absolute);
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const manifestContent = await readManifest(options['--manifest']);
  let privateKeyBytes;
  try {
    privateKeyBytes = readFileSync(options.fd);
    if (privateKeyBytes.length === 0 || privateKeyBytes.length > 64 * 1024) {
      throw new Error('Fleet approval private key 입력 크기가 올바르지 않습니다.');
    }
    const approval = createPlatformReleaseApproval({
      keyId: options['--key-id'],
      manifestContent,
      privateKey: privateKeyBytes,
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
