#!/usr/bin/env node
import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import { basename, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { parseTrustedPlatformReleaseKeys } from './platform-fleet-approval.mjs';
import { verifyPlatformReleaseApproval } from './platform-fleet-reconciler.mjs';
import { sha256 } from './platform-release-lib.mjs';

const API_VERSION = '2022-11-28';
const GITHUB_API_BASE = 'https://api.github.com';
const GITHUB_UPLOAD_BASE = 'https://uploads.github.com';
const RELEASE_REPOSITORY = 'seorilabs/platform';
const APPROVAL_ASSET_NAME = 'fleet-approved.json';
const MAX_INPUT_BYTES = 1024 * 1024;

function requiredString(value, label, pattern) {
  if (typeof value !== 'string' || value.length === 0 || (pattern && !pattern.test(value))) {
    throw new Error(`${label} 값이 올바르지 않습니다.`);
  }
  return value;
}

function requiredInteger(value, label, minimum = 0) {
  if (!Number.isSafeInteger(value) || value < minimum) {
    throw new Error(`${label} 값이 올바르지 않습니다.`);
  }
  return value;
}

function safeAssetName(value, label) {
  const name = requiredString(value, label, /^[A-Za-z0-9._-]+$/u);
  if (basename(name) !== name) {
    throw new Error(`${label} 값이 안전한 asset 이름이 아닙니다.`);
  }
  return name;
}

async function readBoundedFile(path, label) {
  const absolute = resolve(path);
  let handle;
  try {
    handle = await open(absolute, constants.O_RDONLY | constants.O_NOFOLLOW);
    const metadata = await handle.stat();
    if (!metadata.isFile() || metadata.size < 1 || metadata.size > MAX_INPUT_BYTES) {
      throw new Error(`${label}은 1MiB 이하 비어 있지 않은 일반 파일이어야 합니다.`);
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

async function githubRequest(fetchImpl, url, token, options = {}) {
  return fetchImpl(url, {
    ...options,
    redirect: 'error',
    headers: {
      Accept: options.accept ?? 'application/vnd.github+json',
      Authorization: `Bearer ${token}`,
      'X-GitHub-Api-Version': API_VERSION,
      'User-Agent': 'seorilabs-platform-fleet-approval',
      ...options.headers,
    },
  });
}

async function successfulResponse(fetchImpl, url, token, options = {}) {
  const response = await githubRequest(fetchImpl, url, token, options);
  if (!response.ok) {
    const detail = (await response.text()).slice(0, 1000);
    throw new Error(`GitHub API ${response.status} ${response.statusText}: ${detail}`);
  }
  return response;
}

async function jsonResponse(fetchImpl, url, token, options = {}) {
  const response = await successfulResponse(fetchImpl, url, token, options);
  try {
    return await response.json();
  } catch (error) {
    throw new Error('GitHub API JSON 응답을 해석하지 못했습니다.', { cause: error });
  }
}

function manifestAssets(manifest, manifestBytes) {
  const typescript = manifest.sdk.typescript.artifact;
  const gdscript = manifest.sdk.gdscript;
  return [
    {
      name: 'platform-release.json',
      size: manifestBytes.length,
      digest: sha256(manifestBytes),
      expectedBytes: manifestBytes,
    },
    {
      name: safeAssetName(typescript.name, 'TypeScript SDK asset'),
      size: requiredInteger(typescript.size, 'TypeScript SDK asset size', 1),
      digest: requiredString(typescript.sha256, 'TypeScript SDK asset sha256', /^[0-9a-f]{64}$/u),
    },
    {
      name: safeAssetName(gdscript.artifact.name, 'GDScript SDK asset'),
      size: requiredInteger(gdscript.artifact.size, 'GDScript SDK asset size', 1),
      digest: requiredString(
        gdscript.artifact.sha256,
        'GDScript SDK asset sha256',
        /^[0-9a-f]{64}$/u,
      ),
    },
    {
      name: safeAssetName(gdscript.checksumArtifact.name, 'GDScript checksum asset'),
      size: requiredInteger(gdscript.checksumArtifact.size, 'GDScript checksum asset size', 1),
      digest: requiredString(
        gdscript.checksumArtifact.sha256,
        'GDScript checksum asset sha256',
        /^[0-9a-f]{64}$/u,
      ),
      expectedBytes: Buffer.from(
        `${gdscript.artifact.sha256}  ${gdscript.artifact.name}\n`,
        'utf8',
      ),
    },
  ];
}

function validateReleaseShape(release, tag) {
  if (
    release === null
    || typeof release !== 'object'
    || Array.isArray(release)
    || !Number.isSafeInteger(release.id)
    || release.tag_name !== tag
    || release.draft !== false
    || release.prerelease !== false
    || !Array.isArray(release.assets)
  ) {
    throw new Error('GitHub Release가 발행된 exact tag 경계와 일치하지 않습니다.');
  }
  const names = release.assets.map((asset) => asset?.name);
  if (names.some((name) => typeof name !== 'string') || new Set(names).size !== names.length) {
    throw new Error('GitHub Release asset 이름이 없거나 중복되었습니다.');
  }
  return release;
}

function indexReleaseAssets(release, expectedBaseNames) {
  const allowed = new Set([...expectedBaseNames, APPROVAL_ASSET_NAME]);
  const unexpected = release.assets.filter((asset) => !allowed.has(asset.name));
  if (unexpected.length > 0) {
    throw new Error(
      `release에 예상하지 않은 asset이 있습니다: ${unexpected.map((asset) => asset.name).join(', ')}`,
    );
  }
  const indexed = new Map(release.assets.map((asset) => [asset.name, asset]));
  for (const name of expectedBaseNames) {
    if (!indexed.has(name)) {
      throw new Error(`release에 필수 base asset이 없습니다: ${name}`);
    }
  }
  return indexed;
}

async function downloadAsset(fetchImpl, asset, label, tag) {
  const expectedUrl = `https://github.com/${RELEASE_REPOSITORY}/releases/download/${tag}/${encodeURIComponent(label)}`;
  const url = requiredString(asset?.browser_download_url, `${label} download URL`, /^https:\/\//u);
  if (url !== expectedUrl) {
    throw new Error(`${label} download URL이 exact GitHub release 경계와 다릅니다.`);
  }
  const expectedSize = requiredInteger(asset?.size, `${label} remote size`, 1);
  const response = await fetchImpl(url, {
    redirect: 'follow',
    headers: {
      Accept: 'application/octet-stream',
      'User-Agent': 'seorilabs-platform-fleet-approval',
    },
  });
  if (!response.ok) {
    throw new Error(`공개 release asset 다운로드가 실패했습니다: ${label} (${response.status})`);
  }
  const bytes = Buffer.from(await response.arrayBuffer());
  if (bytes.length !== expectedSize) {
    throw new Error(`${label} 다운로드 크기가 GitHub readback과 다릅니다.`);
  }
  return bytes;
}

async function verifyBaseAssets(fetchImpl, release, expectedAssets, tag) {
  const indexed = indexReleaseAssets(release, expectedAssets.map((asset) => asset.name));
  for (const expected of expectedAssets) {
    const remote = indexed.get(expected.name);
    if (remote.size !== expected.size) {
      throw new Error(`${expected.name} 크기가 manifest와 다릅니다.`);
    }
    const bytes = await downloadAsset(fetchImpl, remote, expected.name, tag);
    if (sha256(bytes) !== expected.digest) {
      throw new Error(`${expected.name} digest가 manifest와 다릅니다.`);
    }
    if (expected.expectedBytes && !bytes.equals(expected.expectedBytes)) {
      throw new Error(`${expected.name} 원문이 release 계약과 다릅니다.`);
    }
  }
  return indexed;
}

async function resolveTagCommit(fetchImpl, apiBase, repository, token, tag) {
  let object = (await jsonResponse(
    fetchImpl,
    `${apiBase}/repos/${repository}/git/ref/tags/${encodeURIComponent(tag)}`,
    token,
  )).object;
  const visited = new Set();
  for (let depth = 0; depth < 5; depth += 1) {
    const sha = requiredString(object?.sha, 'release tag object SHA', /^[0-9a-f]{40}$/u);
    const type = requiredString(object?.type, 'release tag object type', /^(commit|tag)$/u);
    if (type === 'commit') {
      return sha;
    }
    if (visited.has(sha)) {
      throw new Error('release tag object 순환 참조를 발견했습니다.');
    }
    visited.add(sha);
    object = (await jsonResponse(
      fetchImpl,
      `${apiBase}/repos/${repository}/git/tags/${sha}`,
      token,
    )).object;
  }
  throw new Error('release tag가 5단계 안에 commit으로 해석되지 않습니다.');
}

async function getRelease(fetchImpl, apiBase, repository, token, tag) {
  const release = await jsonResponse(
    fetchImpl,
    `${apiBase}/repos/${repository}/releases/tags/${encodeURIComponent(tag)}`,
    token,
  );
  return validateReleaseShape(release, tag);
}

async function verifyApprovalAsset(fetchImpl, asset, approvalBytes, tag) {
  if (asset.size !== approvalBytes.length) {
    throw new Error('기존 fleet-approved.json 크기가 승인 원문과 다릅니다.');
  }
  const bytes = await downloadAsset(fetchImpl, asset, APPROVAL_ASSET_NAME, tag);
  if (sha256(bytes) !== sha256(approvalBytes) || !bytes.equals(approvalBytes)) {
    throw new Error('기존 fleet-approved.json은 다른 승인입니다. 덮어쓰지 않습니다.');
  }
}

async function uploadApproval(fetchImpl, release, token, approvalBytes) {
  const uploadUrl = requiredString(release.upload_url, 'release upload URL', /^https:\/\//u)
    .replace(/\{.*$/u, '');
  const expectedUrl = `${GITHUB_UPLOAD_BASE}/repos/${RELEASE_REPOSITORY}/releases/${release.id}/assets`;
  if (uploadUrl !== expectedUrl) {
    throw new Error('release upload URL이 exact GitHub upload 경계와 다릅니다.');
  }
  return githubRequest(
    fetchImpl,
    `${uploadUrl}?name=${encodeURIComponent(APPROVAL_ASSET_NAME)}`,
    token,
    {
      method: 'POST',
      accept: 'application/vnd.github+json',
      headers: {
        'Content-Type': 'application/json',
        'Content-Length': String(approvalBytes.length),
      },
      body: approvalBytes,
    },
  );
}

export async function publishPlatformFleetApproval({
  apiBase = GITHUB_API_BASE,
  approvalPath,
  fetchImpl = fetch,
  manifestPath,
  repository,
  token,
  trustedKeysPath,
}) {
  if (repository !== RELEASE_REPOSITORY) {
    throw new Error(`release repository가 올바르지 않습니다: ${repository}`);
  }
  if (apiBase !== GITHUB_API_BASE) {
    throw new Error(`GitHub API origin이 올바르지 않습니다: ${apiBase}`);
  }

  const [manifestBytes, approvalBytes, trustedKeysBytes] = await Promise.all([
    readBoundedFile(manifestPath, 'platform-release.json'),
    readBoundedFile(approvalPath, APPROVAL_ASSET_NAME),
    readBoundedFile(trustedKeysPath, 'trusted release key registry'),
  ]);
  const manifest = parseJson(manifestBytes, 'platform-release.json');
  const approval = parseJson(approvalBytes, APPROVAL_ASSET_NAME);
  const trustedPublicKeys = parseTrustedPlatformReleaseKeys(
    parseJson(trustedKeysBytes, 'trusted release key registry'),
  );
  const approvalPayload = verifyPlatformReleaseApproval(
    manifestBytes,
    approval,
    trustedPublicKeys,
  );
  const tag = requiredString(approvalPayload.releaseTag, 'approved release tag', /^v\d+\.\d+\.\d+$/u);
  const sourceSha = requiredString(
    approvalPayload.sourceSha,
    'approved release source SHA',
    /^[0-9a-f]{40}$/u,
  );
  const expectedAssets = manifestAssets(manifest, manifestBytes);
  if (new Set(expectedAssets.map((asset) => asset.name)).size !== expectedAssets.length) {
    throw new Error('manifest base asset 이름이 중복되었습니다.');
  }
  requiredString(token, 'GitHub mutation adapter token');

  const [release, tagCommit] = await Promise.all([
    getRelease(fetchImpl, apiBase, repository, token, tag),
    resolveTagCommit(fetchImpl, apiBase, repository, token, tag),
  ]);
  if (tagCommit !== sourceSha) {
    throw new Error('release tag commit이 승인된 Platform source SHA와 다릅니다.');
  }
  let indexed = await verifyBaseAssets(fetchImpl, release, expectedAssets, tag);
  const existing = indexed.get(APPROVAL_ASSET_NAME);
  if (existing) {
    await verifyApprovalAsset(fetchImpl, existing, approvalBytes, tag);
    return Object.freeze({
      approvalSha256: `sha256:${sha256(approvalBytes)}`,
      created: false,
      releaseId: release.id,
      sourceSha,
      tag,
    });
  }

  const uploadResponse = await uploadApproval(fetchImpl, release, token, approvalBytes);
  if (!uploadResponse.ok && uploadResponse.status !== 422) {
    const detail = (await uploadResponse.text()).slice(0, 1000);
    throw new Error(`Fleet approval upload ${uploadResponse.status} ${uploadResponse.statusText}: ${detail}`);
  }

  const persistedRelease = await getRelease(fetchImpl, apiBase, repository, token, tag);
  if (persistedRelease.id !== release.id) {
    throw new Error('Fleet approval 게시 후 release identity가 변경되었습니다.');
  }
  indexed = await verifyBaseAssets(fetchImpl, persistedRelease, expectedAssets, tag);
  const persistedApproval = indexed.get(APPROVAL_ASSET_NAME);
  if (!persistedApproval) {
    throw new Error('Fleet approval 게시 후 fleet-approved.json을 readback하지 못했습니다.');
  }
  await verifyApprovalAsset(fetchImpl, persistedApproval, approvalBytes, tag);
  return Object.freeze({
    approvalSha256: `sha256:${sha256(approvalBytes)}`,
    created: uploadResponse.ok,
    releaseId: persistedRelease.id,
    sourceSha,
    tag,
  });
}

function parseArguments(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!key?.startsWith('--') || value === undefined || Object.hasOwn(options, key)) {
      throw new Error('Fleet approval publisher 인자가 올바르지 않습니다.');
    }
    options[key] = value;
  }
  const required = ['--approval', '--manifest', '--trusted-keys'];
  if (
    Object.keys(options).some((key) => !required.includes(key))
    || required.some((key) => !options[key])
  ) {
    throw new Error('Fleet approval publisher 필수 인자가 없거나 알 수 없는 인자가 있습니다.');
  }
  return options;
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const result = await publishPlatformFleetApproval({
    apiBase: process.env.GITHUB_API_URL ?? 'https://api.github.com',
    approvalPath: options['--approval'],
    manifestPath: options['--manifest'],
    repository: process.env.GITHUB_REPOSITORY ?? '',
    token: process.env.GITHUB_TOKEN ?? '',
    trustedKeysPath: options['--trusted-keys'],
  });
  process.stdout.write(
    `Fleet approval 게시 확인: ${result.tag} (${result.created ? 'created' : 'already-exists'})\n`,
  );
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
