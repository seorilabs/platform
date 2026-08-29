#!/usr/bin/env node
import { constants } from 'node:fs';
import { open } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { parseTrustedPlatformReleaseKeys } from './platform-fleet-approval.mjs';
import { verifyPlatformReleaseApproval } from './platform-fleet-reconciler.mjs';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const GITHUB_API_BASE = 'https://api.github.com';
const GITHUB_REPOSITORY = 'seorilabs/platform';
const API_VERSION = '2026-03-10';
const APPROVAL_ASSET_NAME = 'fleet-approved.json';
const MANIFEST_ASSET_NAME = 'platform-release.json';
const BOOTSTRAP_PATH = resolve(repoRoot, '.github/platform-release-bootstrap-base.json');
const TRUST_REGISTRY_PATH = resolve(repoRoot, '.github/platform-fleet-trusted-release-keys.json');
const MAX_JSON_BYTES = 1024 * 1024;
const MAX_RELEASE_ASSET_BYTES = 256 * 1024 * 1024;
const MAX_RELEASE_PAGES = 10;
const RELEASES_PER_PAGE = 100;
const RELEASE_REDIRECT_HOSTS = new Set(['release-assets.githubusercontent.com']);
const SOURCE_SHA_PATTERN = /^[0-9a-f]{40}$/u;
const RELEASE_TAG_PATTERN = /^v\d+\.\d+\.\d+$/u;
const BOOTSTRAP_PURPOSE = 'seorilabs-platform-release-bootstrap-base-v1';

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

function requiredString(value, label, pattern) {
  if (typeof value !== 'string' || value.length === 0 || (pattern && !pattern.test(value))) {
    throw new Error(`${label} 값이 올바르지 않습니다.`);
  }
  return value;
}

function parseJson(bytes, label) {
  try {
    return JSON.parse(bytes.toString('utf8'));
  } catch (error) {
    throw new Error(`${label} JSON을 해석하지 못했습니다.`, { cause: error });
  }
}

async function readBoundedFile(path, label) {
  let handle;
  try {
    handle = await open(resolve(path), constants.O_RDONLY | constants.O_NOFOLLOW);
    const metadata = await handle.stat();
    if (!metadata.isFile() || metadata.size < 1 || metadata.size > MAX_JSON_BYTES) {
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

function validateBootstrapBase(value) {
  exactKeys(
    value,
    ['purpose', 'releaseTag', 'schemaVersion', 'sourceSha'],
    'Platform release bootstrap base',
  );
  if (value.schemaVersion !== 1 || value.purpose !== BOOTSTRAP_PURPOSE) {
    throw new Error('Platform release bootstrap base version 또는 purpose가 올바르지 않습니다.');
  }
  requiredString(value.releaseTag, 'bootstrap releaseTag', RELEASE_TAG_PATTERN);
  requiredString(value.sourceSha, 'bootstrap sourceSha', SOURCE_SHA_PATTERN);
  return Object.freeze(structuredClone(value));
}

async function readResponseBounded(response, maximum, label) {
  const declaredLength = response.headers.get('content-length');
  if (declaredLength !== null) {
    const parsed = Number.parseInt(declaredLength, 10);
    if (!Number.isSafeInteger(parsed) || parsed < 1 || parsed > maximum) {
      throw new Error(`${label} Content-Length가 허용 범위를 벗어났습니다.`);
    }
  }
  if (!response.body) {
    throw new Error(`${label} 응답 본문이 없습니다.`);
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
        throw new Error(`${label} 응답이 허용 크기를 초과했습니다.`);
      }
      chunks.push(Buffer.from(value));
    }
  } finally {
    reader.releaseLock();
  }
  if (total < 1) {
    throw new Error(`${label} 응답이 비어 있습니다.`);
  }
  return Buffer.concat(chunks, total);
}

async function githubJson(fetchImpl, url, token) {
  const headers = {
    Accept: 'application/vnd.github+json',
    'X-GitHub-Api-Version': API_VERSION,
    'User-Agent': 'seorilabs-platform-release-base',
  };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const response = await fetchImpl(url, { headers, redirect: 'error' });
  if (!response.ok) {
    throw new Error(`GitHub release 목록 조회 실패: status=${response.status}`);
  }
  return parseJson(
    await readResponseBounded(response, MAX_JSON_BYTES, 'GitHub release 목록'),
    'GitHub release 목록',
  );
}

async function listReleases(fetchImpl, token) {
  const releases = [];
  for (let page = 1; page <= MAX_RELEASE_PAGES; page += 1) {
    const url = `${GITHUB_API_BASE}/repos/${GITHUB_REPOSITORY}/releases`
      + `?per_page=${RELEASES_PER_PAGE}&page=${page}`;
    const batch = await githubJson(fetchImpl, url, token);
    if (!Array.isArray(batch)) {
      throw new Error('GitHub release 목록 형식이 올바르지 않습니다.');
    }
    releases.push(...batch);
    if (batch.length < RELEASES_PER_PAGE) {
      return releases;
    }
  }
  throw new Error('Fleet 승인 release 탐색 범위를 초과했습니다.');
}

function indexAssets(release) {
  if (!Array.isArray(release.assets)) {
    throw new Error('Fleet 승인 release asset 목록이 올바르지 않습니다.');
  }
  const indexed = new Map();
  for (const asset of release.assets) {
    if (
      !isRecord(asset)
      || !Number.isSafeInteger(asset.id)
      || asset.id < 1
      || typeof asset.name !== 'string'
      || !Number.isSafeInteger(asset.size)
      || asset.size < 1
      || asset.size > MAX_RELEASE_ASSET_BYTES
      || typeof asset.browser_download_url !== 'string'
    ) {
      throw new Error('Fleet 승인 release asset identity가 올바르지 않습니다.');
    }
    if (indexed.has(asset.name)) {
      throw new Error(`Fleet 승인 release asset 이름이 중복되었습니다: ${asset.name}`);
    }
    indexed.set(asset.name, asset);
  }
  return indexed;
}

function validateApprovedReleaseCandidate(release) {
  if (
    !isRecord(release)
    || !Number.isSafeInteger(release.id)
    || release.id < 1
    || release.draft !== false
    || release.prerelease !== false
    || release.immutable !== true
  ) {
    throw new Error('Fleet 승인 release가 published immutable 상태가 아닙니다.');
  }
  const tag = requiredString(release.tag_name, 'Fleet 승인 release tag', RELEASE_TAG_PATTERN);
  const sourceSha = requiredString(
    release.target_commitish,
    'Fleet 승인 release source SHA',
    SOURCE_SHA_PATTERN,
  );
  const publishedAt = Date.parse(release.published_at);
  if (!Number.isFinite(publishedAt)) {
    throw new Error('Fleet 승인 release published_at이 올바르지 않습니다.');
  }
  const assets = indexAssets(release);
  const manifest = assets.get(MANIFEST_ASSET_NAME);
  const approval = assets.get(APPROVAL_ASSET_NAME);
  if (!manifest || !approval) {
    throw new Error('Fleet 승인 release에 manifest 또는 approval asset이 없습니다.');
  }
  if (manifest.size > MAX_JSON_BYTES || approval.size > MAX_JSON_BYTES) {
    throw new Error('Fleet 승인 manifest 또는 approval asset이 1MiB를 초과했습니다.');
  }
  return { approval, assets, manifest, publishedAt, sourceSha, tag };
}

function validateReleaseAssetUrl(asset, tag) {
  const expected = `https://github.com/${GITHUB_REPOSITORY}/releases/download/${tag}/${asset.name}`;
  if (asset.browser_download_url !== expected) {
    throw new Error(`${asset.name} download URL이 exact release 경계와 다릅니다.`);
  }
  return expected;
}

function validateRedirect(location) {
  let redirect;
  try {
    redirect = new URL(location);
  } catch (error) {
    throw new Error('GitHub release asset redirect URL이 올바르지 않습니다.', { cause: error });
  }
  if (
    redirect.protocol !== 'https:'
    || redirect.username !== ''
    || redirect.password !== ''
    || !RELEASE_REDIRECT_HOSTS.has(redirect.hostname)
  ) {
    throw new Error('GitHub release asset redirect origin을 신뢰할 수 없습니다.');
  }
  return redirect.href;
}

async function downloadAsset(fetchImpl, asset, tag) {
  const url = validateReleaseAssetUrl(asset, tag);
  let response = await fetchImpl(url, {
    headers: {
      Accept: 'application/octet-stream',
      'User-Agent': 'seorilabs-platform-release-base',
    },
    redirect: 'manual',
  });
  if ([301, 302, 303, 307, 308].includes(response.status)) {
    const redirect = validateRedirect(response.headers.get('location'));
    response = await fetchImpl(redirect, {
      headers: {
        Accept: 'application/octet-stream',
        'User-Agent': 'seorilabs-platform-release-base',
      },
      redirect: 'error',
    });
  }
  if (!response.ok) {
    throw new Error(`${asset.name} download 실패: status=${response.status}`);
  }
  const bytes = await readResponseBounded(response, asset.size, `${asset.name} download`);
  if (bytes.length !== asset.size) {
    throw new Error(`${asset.name} download 크기가 provider readback과 다릅니다.`);
  }
  return bytes;
}

function approvedCandidates(releases) {
  return releases
    .filter((release) => (
      release?.draft === false
      && release?.prerelease === false
      && release?.immutable === true
      && Array.isArray(release.assets)
      && release.assets.some((asset) => asset?.name === APPROVAL_ASSET_NAME)
    ))
    .map((release) => ({ release, ...validateApprovedReleaseCandidate(release) }))
    .sort((left, right) => {
      const byPublishedAt = right.publishedAt - left.publishedAt;
      return byPublishedAt || right.release.id - left.release.id;
    });
}

export async function resolvePlatformReleaseBase({
  bootstrap,
  fetchImpl = fetch,
  token = '',
  trustedPublicKeys,
  verifyApprovalImpl = verifyPlatformReleaseApproval,
}) {
  const normalizedBootstrap = validateBootstrapBase(bootstrap);
  if (typeof fetchImpl !== 'function' || typeof verifyApprovalImpl !== 'function') {
    throw new Error('Platform release base read adapter가 올바르지 않습니다.');
  }
  const releases = await listReleases(fetchImpl, token);
  const candidates = approvedCandidates(releases);
  if (candidates.length === 0) {
    return Object.freeze({
      kind: 'bootstrap',
      releaseTag: normalizedBootstrap.releaseTag,
      sourceSha: normalizedBootstrap.sourceSha,
    });
  }

  const candidate = candidates[0];
  const release = candidate.release;
  const [manifestBytes, approvalBytes] = await Promise.all([
    downloadAsset(fetchImpl, candidate.manifest, candidate.tag),
    downloadAsset(fetchImpl, candidate.approval, candidate.tag),
  ]);
  const approval = parseJson(approvalBytes, APPROVAL_ASSET_NAME);
  const payload = verifyApprovalImpl(manifestBytes, approval, trustedPublicKeys);
  if (payload.releaseTag !== candidate.tag || payload.sourceSha !== candidate.sourceSha) {
    throw new Error('Fleet approval payload가 release provider identity와 다릅니다.');
  }
  return Object.freeze({
    kind: 'fleet-approved',
    releaseId: release.id,
    releaseTag: candidate.tag,
    sourceSha: candidate.sourceSha,
  });
}

async function main() {
  const token = process.env.GITHUB_TOKEN;
  if (!token) {
    throw new Error('GITHUB_TOKEN이 필요합니다.');
  }
  const [bootstrapBytes, trustRegistryBytes] = await Promise.all([
    readBoundedFile(BOOTSTRAP_PATH, 'Platform release bootstrap base'),
    readBoundedFile(TRUST_REGISTRY_PATH, 'Fleet approval trusted key registry'),
  ]);
  const result = await resolvePlatformReleaseBase({
    bootstrap: parseJson(bootstrapBytes, 'Platform release bootstrap base'),
    token,
    trustedPublicKeys: parseTrustedPlatformReleaseKeys(
      parseJson(trustRegistryBytes, 'Fleet approval trusted key registry'),
    ),
  });
  process.stderr.write(
    `Platform release base: ${result.kind} ${result.releaseTag} ${result.sourceSha}\n`,
  );
  process.stdout.write(`${result.sourceSha}\n`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
