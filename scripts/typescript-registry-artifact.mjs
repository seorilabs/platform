#!/usr/bin/env node
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { constants } from 'node:fs';
import { open, writeFile } from 'node:fs/promises';
import { basename, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { assertTypescriptPackageArtifact, sha256 } from './platform-release-lib.mjs';

const PACKAGE_NAME = '@seorilabs/platform-sdk';
const REGISTRY = 'https://registry.npmjs.org';
const MAX_ARTIFACT_BYTES = 256 * 1024 * 1024;
const VERSION_PATTERN = /^\d+\.\d+\.\d+$/u;
const SHA1_PATTERN = /^[0-9a-f]{40}$/u;
const SHA512_INTEGRITY_PATTERN = /^sha512-[A-Za-z0-9+/]+={0,2}$/u;

function requiredString(value, label, pattern) {
  if (typeof value !== 'string' || value.length === 0 || (pattern && !pattern.test(value))) {
    throw new Error(`${label} 값이 올바르지 않습니다.`);
  }
  return value;
}

export function typescriptArtifactIntegrity(bytes) {
  if (!Buffer.isBuffer(bytes) || bytes.length < 1) {
    throw new Error('TypeScript artifact byte가 필요합니다.');
  }
  return `sha512-${createHash('sha512').update(bytes).digest('base64')}`;
}

export function verifyTypescriptArtifactIntegrity(bytes, expectedIntegrity) {
  requiredString(
    expectedIntegrity,
    'TypeScript registry integrity',
    SHA512_INTEGRITY_PATTERN,
  );
  const actual = typescriptArtifactIntegrity(bytes);
  if (actual !== expectedIntegrity) {
    throw new Error('TypeScript artifact byte가 registry integrity와 다릅니다.');
  }
  return actual;
}

function validateRegistryMetadata(metadata, packageName, version) {
  if (metadata === null || typeof metadata !== 'object' || Array.isArray(metadata)) {
    throw new Error('TypeScript registry dist metadata 형식이 올바르지 않습니다.');
  }
  const integrity = requiredString(
    metadata.integrity,
    'TypeScript registry integrity',
    SHA512_INTEGRITY_PATTERN,
  );
  const shasum = requiredString(metadata.shasum, 'TypeScript registry shasum', SHA1_PATTERN);
  const tarball = requiredString(metadata.tarball, 'TypeScript registry tarball');
  let url;
  try {
    url = new URL(tarball);
  } catch (error) {
    throw new Error('TypeScript registry tarball URL을 해석하지 못했습니다.', { cause: error });
  }
  const expectedPath = `/${packageName}/-/platform-sdk-${version}.tgz`;
  if (
    url.protocol !== 'https:'
    || url.hostname !== 'registry.npmjs.org'
    || url.username !== ''
    || url.password !== ''
    || url.pathname !== expectedPath
    || url.search !== ''
    || url.hash !== ''
  ) {
    throw new Error('TypeScript registry tarball URL이 exact package 경계와 다릅니다.');
  }
  return Object.freeze({ integrity, shasum, tarball: url.href });
}

async function readResponseBounded(response, maximum) {
  const declaredLength = response.headers.get('content-length');
  if (declaredLength !== null) {
    const parsed = Number.parseInt(declaredLength, 10);
    if (!Number.isSafeInteger(parsed) || parsed < 1 || parsed > maximum) {
      throw new Error('TypeScript registry artifact Content-Length가 허용 범위를 벗어났습니다.');
    }
  }
  if (!response.body) {
    throw new Error('TypeScript registry artifact 응답 본문이 없습니다.');
  }
  const reader = response.body.getReader();
  const chunks = [];
  let total = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      total += value.byteLength;
      if (total > maximum) {
        await reader.cancel();
        throw new Error('TypeScript registry artifact가 허용 크기를 초과했습니다.');
      }
      chunks.push(Buffer.from(value));
    }
  } finally {
    reader.releaseLock();
  }
  if (total < 1) {
    throw new Error('TypeScript registry artifact가 비어 있습니다.');
  }
  return Buffer.concat(chunks, total);
}

function readRegistryMetadata(packageName, version) {
  const result = spawnSync(
    'npm',
    [
      'view',
      `${packageName}@${version}`,
      'dist',
      '--json',
      `--registry=${REGISTRY}`,
    ],
    { encoding: 'utf8', maxBuffer: 1024 * 1024 },
  );
  if (result.error) {
    throw new Error('TypeScript registry metadata 조회를 실행하지 못했습니다.', {
      cause: result.error,
    });
  }
  if (result.status !== 0) {
    throw new Error(`TypeScript registry metadata 조회 실패: status=${result.status}`);
  }
  try {
    return JSON.parse(result.stdout);
  } catch (error) {
    throw new Error('TypeScript registry metadata JSON을 해석하지 못했습니다.', { cause: error });
  }
}

async function readBoundedArtifact(path) {
  let handle;
  try {
    handle = await open(resolve(path), constants.O_RDONLY | constants.O_NOFOLLOW);
    const metadata = await handle.stat();
    if (!metadata.isFile() || metadata.size < 1 || metadata.size > MAX_ARTIFACT_BYTES) {
      throw new Error('TypeScript artifact는 허용 크기 이하 일반 파일이어야 합니다.');
    }
    const bytes = await handle.readFile();
    if (bytes.length !== metadata.size) {
      throw new Error('TypeScript artifact 크기가 읽기 도중 변경되었습니다.');
    }
    return bytes;
  } catch (error) {
    if (error?.code === 'ELOOP') {
      throw new Error('TypeScript artifact는 symbolic link일 수 없습니다.', { cause: error });
    }
    throw error;
  } finally {
    await handle?.close();
  }
}

export async function fetchTypescriptRegistryArtifact({
  fetchImpl = fetch,
  metadata,
  outputPath,
  packageName = PACKAGE_NAME,
  token = '',
  version,
}) {
  if (packageName !== PACKAGE_NAME || typeof fetchImpl !== 'function') {
    throw new Error('TypeScript registry artifact adapter 경계가 올바르지 않습니다.');
  }
  requiredString(version, 'TypeScript package version', VERSION_PATTERN);
  requiredString(outputPath, 'TypeScript artifact output path');
  const expectedName = `seorilabs-platform-sdk-${version}.tgz`;
  if (basename(outputPath) !== expectedName) {
    throw new Error(`TypeScript artifact output 파일명은 ${expectedName}이어야 합니다.`);
  }
  const dist = validateRegistryMetadata(metadata, packageName, version);
  const headers = {
    Accept: 'application/octet-stream',
    'User-Agent': 'seorilabs-platform-release',
  };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const response = await fetchImpl(dist.tarball, { headers, redirect: 'error' });
  if (!response.ok) {
    throw new Error(`TypeScript registry artifact download 실패: status=${response.status}`);
  }
  const bytes = await readResponseBounded(response, MAX_ARTIFACT_BYTES);
  verifyTypescriptArtifactIntegrity(bytes, dist.integrity);
  if (createHash('sha1').update(bytes).digest('hex') !== dist.shasum) {
    throw new Error('TypeScript artifact byte가 registry shasum과 다릅니다.');
  }
  assertTypescriptPackageArtifact(bytes, packageName, version);
  await writeFile(resolve(outputPath), bytes, { flag: 'wx', mode: 0o600 });
  return Object.freeze({
    integrity: dist.integrity,
    path: resolve(outputPath),
    sha256: sha256(bytes),
    size: bytes.length,
  });
}

function parseOptions(argv, allowed, required) {
  const options = {};
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!allowed.has(key) || value === undefined || Object.hasOwn(options, key)) {
      throw new Error('TypeScript registry artifact 인자가 올바르지 않습니다.');
    }
    options[key] = value;
  }
  for (const key of required) {
    if (!options[key]) {
      throw new Error(`필수 인자가 없습니다: ${key}`);
    }
  }
  return options;
}

async function main() {
  const [command, ...argv] = process.argv.slice(2);
  if (command === 'integrity') {
    const options = parseOptions(argv, new Set(['--artifact']), ['--artifact']);
    const bytes = await readBoundedArtifact(options['--artifact']);
    process.stdout.write(`${typescriptArtifactIntegrity(bytes)}\n`);
    return;
  }
  if (command === 'fetch') {
    const options = parseOptions(
      argv,
      new Set(['--output', '--version']),
      ['--output', '--version'],
    );
    const token = process.env.NODE_AUTH_TOKEN ?? '';
    const version = options['--version'];
    const result = await fetchTypescriptRegistryArtifact({
      metadata: readRegistryMetadata(PACKAGE_NAME, version),
      outputPath: options['--output'],
      token,
      version,
    });
    process.stdout.write(`${result.integrity}\n`);
    return;
  }
  throw new Error('사용법: typescript-registry-artifact.mjs <fetch|integrity> ...');
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
