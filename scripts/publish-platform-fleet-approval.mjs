#!/usr/bin/env node
import { verify as verifySignature } from 'node:crypto';
import { constants, readFileSync } from 'node:fs';
import { open } from 'node:fs/promises';
import { basename, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { parseTrustedPlatformReleaseKeys } from './platform-fleet-approval.mjs';
import { verifyPlatformReleaseApproval } from './platform-fleet-reconciler.mjs';
import { sha256 } from './platform-release-lib.mjs';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const API_VERSION = '2026-03-10';
const GITHUB_API_BASE = 'https://api.github.com';
const GITHUB_UPLOAD_BASE = 'https://uploads.github.com';
const RELEASE_ORGANIZATION = 'seorilabs';
const RELEASE_REPOSITORY = 'seorilabs/platform';
const APPROVAL_ASSET_NAME = 'fleet-approved.json';
const IMMUTABLE_TAG_RULESET_NAME = 'Immutable Platform release tags';
const TRUST_REGISTRY_PATH = resolve(
  repoRoot,
  '.github/platform-fleet-trusted-release-keys.json',
);
const TRUST_REGISTRY_SHA256 = '6df32f25121dc5b72cd366adf3cfdd1979f452c20d96ed925574073529df8a35';
const GRANT_PURPOSE = 'seorilabs-platform-approval-publish-grant-v1';
const POLICY_ATTESTATION_PURPOSE = 'seorilabs-platform-release-policy-attestation-v1';
const MAX_INPUT_BYTES = 1024 * 1024;
const MAX_JSON_RESPONSE_BYTES = 2 * 1024 * 1024;
const MAX_RELEASE_ASSET_BYTES = 256 * 1024 * 1024;
const SHA256_REVISION_PATTERN = /^sha256:[0-9a-f]{64}$/u;
const SOURCE_SHA_PATTERN = /^[0-9a-f]{40}$/u;
const SAFE_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$/u;
const RELEASE_REDIRECT_HOSTS = new Set(['release-assets.githubusercontent.com']);
const productionFetch = globalThis.fetch.bind(globalThis);

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

function canonicalDigest(value) {
  return `sha256:${sha256(Buffer.from(JSON.stringify(canonicalize(value)), 'utf8'))}`;
}

export function platformFleetPolicyAttestationBytes(payload) {
  return Buffer.from(JSON.stringify(canonicalize(payload)), 'utf8');
}

async function readBoundedFile(path, label, maximum = MAX_INPUT_BYTES) {
  const absolute = resolve(path);
  let handle;
  try {
    handle = await open(absolute, constants.O_RDONLY | constants.O_NOFOLLOW);
    const metadata = await handle.stat();
    if (!metadata.isFile() || metadata.size < 1 || metadata.size > maximum) {
      throw new Error(`${label}은 허용 크기 이하 비어 있지 않은 일반 파일이어야 합니다.`);
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

function safeRequestId(response) {
  const value = response.headers.get('x-github-request-id') ?? '';
  return /^[A-Za-z0-9.-]{1,100}$/u.test(value) ? ` requestId=${value}` : '';
}

function requestFailure(label, response) {
  return new Error(`${label} 실패: status=${response.status}${safeRequestId(response)}`);
}

async function readResponseBounded(response, maximum, label) {
  const declaredLength = response.headers.get('content-length');
  if (declaredLength !== null) {
    const parsed = Number.parseInt(declaredLength, 10);
    if (!Number.isSafeInteger(parsed) || parsed < 0 || parsed > maximum) {
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
  return Buffer.concat(chunks, total);
}

async function githubRequest(fetchImpl, url, token, options = {}) {
  const {
    accept = 'application/vnd.github+json',
    headers = {},
    redirect = 'error',
    ...requestOptions
  } = options;
  return fetchImpl(url, {
    ...requestOptions,
    redirect,
    headers: {
      Accept: accept,
      Authorization: `Bearer ${token}`,
      'X-GitHub-Api-Version': API_VERSION,
      'User-Agent': 'seorilabs-platform-fleet-approval',
      ...headers,
    },
  });
}

async function githubJson(fetchImpl, url, token, options = {}) {
  const response = await githubRequest(fetchImpl, url, token, options);
  if (!response.ok) {
    throw requestFailure('GitHub API 요청', response);
  }
  const bytes = await readResponseBounded(response, MAX_JSON_RESPONSE_BYTES, 'GitHub API JSON');
  return { response, value: parseJson(bytes, 'GitHub API response') };
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

function validateReleaseShape(release, { sourceSha, tag }) {
  if (
    !isRecord(release)
    || !Number.isSafeInteger(release.id)
    || release.tag_name !== tag
    || release.target_commitish !== sourceSha
    || release.prerelease !== false
    || !Array.isArray(release.assets)
  ) {
    throw new Error('GitHub Release가 exact tag와 source 경계에 일치하지 않습니다.');
  }
  if (
    !(
      (release.draft === true && release.immutable === false)
      || (release.draft === false && release.immutable === true)
    )
  ) {
    throw new Error('GitHub Release가 승인 대기 draft 또는 immutable published 상태가 아닙니다.');
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

function validateReleaseRedirect(location, label) {
  let url;
  try {
    url = new URL(location);
  } catch (error) {
    throw new Error(`${label} redirect URL을 해석하지 못했습니다.`, { cause: error });
  }
  if (
    url.protocol !== 'https:'
    || url.username
    || url.password
    || url.hash
    || !RELEASE_REDIRECT_HOSTS.has(url.hostname)
  ) {
    throw new Error(`${label} redirect origin이 허용되지 않았습니다.`);
  }
  return url.toString();
}

async function downloadAsset(fetchImpl, token, asset, label) {
  const assetId = requiredInteger(asset?.id, `${label} asset ID`, 1);
  const expectedSize = requiredInteger(asset?.size, `${label} remote size`, 1);
  if (expectedSize > MAX_RELEASE_ASSET_BYTES) {
    throw new Error(`${label} 크기가 publisher 상한을 초과했습니다.`);
  }
  const assetUrl = `${GITHUB_API_BASE}/repos/${RELEASE_REPOSITORY}/releases/assets/${assetId}`;
  let response = await githubRequest(fetchImpl, assetUrl, token, {
    accept: 'application/octet-stream',
    redirect: 'manual',
  });
  if ([301, 302, 303, 307, 308].includes(response.status)) {
    const redirected = validateReleaseRedirect(response.headers.get('location') ?? '', label);
    response = await fetchImpl(redirected, {
      redirect: 'error',
      headers: {
        Accept: 'application/octet-stream',
        'User-Agent': 'seorilabs-platform-fleet-approval',
      },
    });
  }
  if (!response.ok) {
    throw requestFailure(`${label} download`, response);
  }
  const bytes = await readResponseBounded(response, expectedSize, `${label} download`);
  if (bytes.length !== expectedSize) {
    throw new Error(`${label} 다운로드 크기가 GitHub readback과 다릅니다.`);
  }
  return bytes;
}

async function verifyBaseAssets(fetchImpl, token, release, expectedAssets) {
  const indexed = indexReleaseAssets(release, expectedAssets.map((asset) => asset.name));
  for (const expected of expectedAssets) {
    const remote = indexed.get(expected.name);
    if (remote.size !== expected.size) {
      throw new Error(`${expected.name} 크기가 manifest와 다릅니다.`);
    }
    const bytes = await downloadAsset(fetchImpl, token, remote, expected.name);
    if (sha256(bytes) !== expected.digest) {
      throw new Error(`${expected.name} digest가 manifest와 다릅니다.`);
    }
    if (expected.expectedBytes && !bytes.equals(expected.expectedBytes)) {
      throw new Error(`${expected.name} 원문이 release 계약과 다릅니다.`);
    }
  }
  return indexed;
}

async function resolveTagCommit(fetchImpl, token, tag) {
  let object = (await githubJson(
    fetchImpl,
    `${GITHUB_API_BASE}/repos/${RELEASE_REPOSITORY}/git/ref/tags/${encodeURIComponent(tag)}`,
    token,
  )).value.object;
  const visited = new Set();
  for (let depth = 0; depth < 5; depth += 1) {
    const objectSha = requiredString(object?.sha, 'release tag object SHA', SOURCE_SHA_PATTERN);
    const type = requiredString(object?.type, 'release tag object type', /^(commit|tag)$/u);
    if (type === 'commit') {
      return objectSha;
    }
    if (visited.has(objectSha)) {
      throw new Error('release tag object 순환 참조를 발견했습니다.');
    }
    visited.add(objectSha);
    object = (await githubJson(
      fetchImpl,
      `${GITHUB_API_BASE}/repos/${RELEASE_REPOSITORY}/git/tags/${objectSha}`,
      token,
    )).value.object;
  }
  throw new Error('release tag가 5단계 안에 commit으로 해석되지 않습니다.');
}

async function getRelease(fetchImpl, token, tag, sourceSha) {
  const release = (await githubJson(
    fetchImpl,
    `${GITHUB_API_BASE}/repos/${RELEASE_REPOSITORY}/releases/tags/${encodeURIComponent(tag)}`,
    token,
  )).value;
  return validateReleaseShape(release, { sourceSha, tag });
}

function exactIsoTimestamp(value, label) {
  requiredString(value, label);
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp) || new Date(timestamp).toISOString() !== value) {
    throw new Error(`${label} 값이 RFC3339 UTC timestamp가 아닙니다.`);
  }
  return timestamp;
}

function verifyPolicyAttestation({ approval, approvalBytes, attestation, trustedPublicKeys }) {
  exactKeys(
    attestation,
    ['algorithm', 'keyId', 'payload', 'schemaVersion', 'signature'],
    'Platform policy attestation',
  );
  if (attestation.schemaVersion !== 1 || attestation.algorithm !== 'Ed25519') {
    throw new Error('지원하지 않는 Platform policy attestation 형식입니다.');
  }
  requiredString(attestation.keyId, 'policy attestation keyId', SAFE_ID_PATTERN);
  requiredString(attestation.signature, 'policy attestation signature', /^[A-Za-z0-9+/]+={0,2}$/u);
  exactKeys(
    attestation.payload,
    [
      'approvalSha256',
      'expiresAt',
      'immutableReleases',
      'observedAt',
      'purpose',
      'releaseTag',
      'repository',
      'repositoryId',
      'ruleset',
      'sourceSha',
    ],
    'Platform policy attestation payload',
  );
  exactKeys(
    attestation.payload.immutableReleases,
    ['enabled', 'enforcedByOwner'],
    'Platform policy attestation immutable releases',
  );
  exactKeys(
    attestation.payload.ruleset,
    [
      'bypassActors',
      'enforcement',
      'id',
      'name',
      'refName',
      'ruleTypes',
      'source',
      'sourceType',
      'target',
      'updatedAt',
    ],
    'Platform policy attestation ruleset',
  );
  exactKeys(
    attestation.payload.ruleset.refName,
    ['exclude', 'include'],
    'Platform policy attestation ruleset refName',
  );
  const observedAt = exactIsoTimestamp(
    attestation.payload.observedAt,
    'policy attestation observedAt',
  );
  const expiresAt = exactIsoTimestamp(
    attestation.payload.expiresAt,
    'policy attestation expiresAt',
  );
  exactIsoTimestamp(attestation.payload.ruleset.updatedAt, 'policy ruleset updatedAt');
  const now = Date.now();
  if (
    attestation.keyId !== approval.keyId
    || attestation.payload.purpose !== POLICY_ATTESTATION_PURPOSE
    || attestation.payload.repository !== RELEASE_REPOSITORY
    || !/^\d+$/u.test(attestation.payload.repositoryId)
    || attestation.payload.releaseTag !== approval.payload.releaseTag
    || attestation.payload.sourceSha !== approval.payload.sourceSha
    || attestation.payload.approvalSha256 !== `sha256:${sha256(approvalBytes)}`
    || attestation.payload.immutableReleases.enabled !== true
    || attestation.payload.immutableReleases.enforcedByOwner !== true
    || attestation.payload.ruleset.id === '0'
    || !/^\d+$/u.test(attestation.payload.ruleset.id)
    || attestation.payload.ruleset.name !== IMMUTABLE_TAG_RULESET_NAME
    || attestation.payload.ruleset.sourceType !== 'Organization'
    || attestation.payload.ruleset.source !== RELEASE_ORGANIZATION
    || attestation.payload.ruleset.target !== 'tag'
    || attestation.payload.ruleset.enforcement !== 'active'
    || JSON.stringify(attestation.payload.ruleset.bypassActors) !== JSON.stringify([])
    || JSON.stringify(attestation.payload.ruleset.refName.include)
      !== JSON.stringify(['refs/tags/v*'])
    || JSON.stringify(attestation.payload.ruleset.refName.exclude) !== JSON.stringify([])
    || JSON.stringify(attestation.payload.ruleset.ruleTypes)
      !== JSON.stringify(['deletion', 'update'])
    || observedAt > now + 30 * 1000
    || expiresAt <= now
    || expiresAt > now + 5 * 60 * 1000
    || expiresAt <= observedAt
    || expiresAt - observedAt > 5 * 60 * 1000
  ) {
    throw new Error('Platform policy attestation이 exact publish 정책 경계와 다릅니다.');
  }
  const signature = Buffer.from(attestation.signature, 'base64');
  const publicKey = trustedPublicKeys.get(attestation.keyId);
  if (
    !publicKey
    || signature.length !== 64
    || !verifySignature(
      null,
      platformFleetPolicyAttestationBytes(attestation.payload),
      publicKey,
      signature,
    )
  ) {
    throw new Error('Platform policy attestation 서명이 올바르지 않습니다.');
  }
  return Object.freeze({
    attestationSha256: canonicalDigest(attestation),
    expiresAt: attestation.payload.expiresAt,
    repositoryId: attestation.payload.repositoryId,
    rulesetId: attestation.payload.ruleset.id,
    rulesetUpdatedAt: attestation.payload.ruleset.updatedAt,
  });
}

async function verifyImmutablePolicy(fetchImpl, token, policyAttestation) {
  if (Date.parse(policyAttestation.expiresAt) <= Date.now()) {
    throw new Error('Platform policy attestation이 publish 도중 만료되었습니다.');
  }
  const [repository, setting, listed] = await Promise.all([
    githubJson(fetchImpl, `${GITHUB_API_BASE}/repos/${RELEASE_REPOSITORY}`, token),
    githubJson(
      fetchImpl,
      `${GITHUB_API_BASE}/repos/${RELEASE_REPOSITORY}/immutable-releases`,
      token,
    ),
    githubJson(
      fetchImpl,
      `${GITHUB_API_BASE}/repos/${RELEASE_REPOSITORY}/rulesets?includes_parents=true&per_page=100`,
      token,
    ),
  ]);
  if (
    !isRecord(repository.value)
    || !Number.isSafeInteger(repository.value.id)
    || repository.value.id < 1
    || String(repository.value.id) !== policyAttestation.repositoryId
    || repository.value.full_name !== RELEASE_REPOSITORY
    || repository.value.owner?.login !== RELEASE_ORGANIZATION
    || repository.value.owner?.type !== 'Organization'
  ) {
    throw new Error('GitHub repository identity가 policy attestation과 다릅니다.');
  }
  const repositoryIdentity = Object.freeze({
    fullName: repository.value.full_name,
    ownerLogin: repository.value.owner.login,
    ownerType: repository.value.owner.type,
    repositoryId: String(repository.value.id),
  });
  if (
    !isRecord(setting.value)
    || setting.value.enabled !== true
    || setting.value.enforced_by_owner !== true
  ) {
    throw new Error('조직 소유 immutable releases 정책이 강제되지 않았습니다.');
  }
  if (listed.response.headers.get('link')?.includes('rel="next"')) {
    throw new Error('tag ruleset 목록이 100개를 초과해 exact 정책을 확정하지 못했습니다.');
  }
  if (!Array.isArray(listed.value)) {
    throw new Error('GitHub ruleset 목록 형식이 올바르지 않습니다.');
  }
  const candidates = listed.value.filter((entry) => (
    String(entry?.id) === policyAttestation.rulesetId
    && entry?.name === IMMUTABLE_TAG_RULESET_NAME
    && entry?.source_type === 'Organization'
    && entry?.source === RELEASE_ORGANIZATION
  ));
  if (
    candidates.length !== 1
    || !Number.isSafeInteger(candidates[0].id)
    || candidates[0].id < 1
    || candidates[0].enforcement !== 'active'
    || candidates[0].updated_at !== policyAttestation.rulesetUpdatedAt
  ) {
    throw new Error('immutable Platform tag ruleset을 정확히 하나 찾지 못했습니다.');
  }
  const rulesetBinding = Object.freeze({
    enforcement: candidates[0].enforcement,
    id: String(candidates[0].id),
    name: candidates[0].name,
    source: candidates[0].source,
    sourceType: candidates[0].source_type,
    updatedAt: candidates[0].updated_at,
  });
  return Object.freeze({
    enforcedByOwner: true,
    immutableReleasesEnabled: true,
    policyOwner: RELEASE_ORGANIZATION,
    repositoryId: policyAttestation.repositoryId,
    rulesetId: candidates[0].id,
    policyDigest: canonicalDigest({
      attestationSha256: policyAttestation.attestationSha256,
      immutableReleases: {
        enabled: setting.value.enabled,
        enforcedByOwner: setting.value.enforced_by_owner,
      },
      repositoryIdentity,
      rulesetBinding,
    }),
  });
}

// 테스트와 사전 점검이 mutation capability 없이 조직 소유 정책만 검증할 수 있는
// read-only 경계다. 실제 publisher도 같은 내부 검증을 사용한다.
export async function verifyPlatformFleetImmutablePolicy({
  fetchImpl = fetch,
  policyAttestation,
  token,
}) {
  requiredString(token, 'GitHub policy read token');
  if (!isRecord(policyAttestation)) {
    throw new Error('검증된 Platform policy attestation이 필요합니다.');
  }
  return verifyImmutablePolicy(fetchImpl, token, policyAttestation);
}

function validateGrant(grant, { approval, approvalBytes, manifestBytes, registryBytes }) {
  exactKeys(
    grant,
    [
      'approvalSha256',
      'expiresAt',
      'grantId',
      'keyId',
      'manifestSha256',
      'maxUses',
      'purpose',
      'releaseTag',
      'repository',
      'schemaVersion',
      'sourceSha',
      'trustedReleaseKeyRegistrySha256',
    ],
    'Platform approval publish grant',
  );
  requiredString(grant.grantId, 'grantId', SAFE_ID_PATTERN);
  requiredString(grant.expiresAt, 'grant expiresAt');
  const expiresAt = Date.parse(grant.expiresAt);
  if (
    grant.schemaVersion !== 1
    || grant.purpose !== GRANT_PURPOSE
    || grant.repository !== RELEASE_REPOSITORY
    || grant.maxUses !== 1
    || !Number.isFinite(expiresAt)
    || new Date(expiresAt).toISOString() !== grant.expiresAt
    || expiresAt <= Date.now()
    || expiresAt > Date.now() + 5 * 60 * 1000
    || grant.releaseTag !== approval.payload.releaseTag
    || grant.sourceSha !== approval.payload.sourceSha
    || grant.keyId !== approval.keyId
    || grant.manifestSha256 !== `sha256:${sha256(manifestBytes)}`
    || grant.approvalSha256 !== `sha256:${sha256(approvalBytes)}`
    || grant.trustedReleaseKeyRegistrySha256 !== `sha256:${sha256(registryBytes)}`
  ) {
    throw new Error('Platform approval publish grant가 exact 실행 경계와 다릅니다.');
  }
  requiredString(grant.releaseTag, 'grant releaseTag', /^v\d+\.\d+\.\d+$/u);
  requiredString(grant.sourceSha, 'grant sourceSha', SOURCE_SHA_PATTERN);
  requiredString(grant.manifestSha256, 'grant manifestSha256', SHA256_REVISION_PATTERN);
  requiredString(grant.approvalSha256, 'grant approvalSha256', SHA256_REVISION_PATTERN);
  requiredString(
    grant.trustedReleaseKeyRegistrySha256,
    'grant trustedReleaseKeyRegistrySha256',
    SHA256_REVISION_PATTERN,
  );
  return grant;
}

async function verifyApprovalAsset(fetchImpl, token, asset, approvalBytes) {
  if (asset.size !== approvalBytes.length) {
    throw new Error('기존 fleet-approved.json 크기가 승인 원문과 다릅니다.');
  }
  const bytes = await downloadAsset(fetchImpl, token, asset, APPROVAL_ASSET_NAME);
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

async function publishDraft(fetchImpl, token, release) {
  const response = await githubRequest(
    fetchImpl,
    `${GITHUB_API_BASE}/repos/${RELEASE_REPOSITORY}/releases/${release.id}`,
    token,
    {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ draft: false }),
    },
  );
  if (!response.ok && ![409, 422].includes(response.status)) {
    throw requestFailure('immutable release publish', response);
  }
}

export function verifyPlatformFleetApprovalPublishingInputs({
  approvalBytes,
  grant,
  manifestBytes,
  policyAttestationBytes,
  trustRegistryBytes,
  trustRegistryExpectedSha256,
}) {
  if (
    !Buffer.isBuffer(approvalBytes)
    || !Buffer.isBuffer(manifestBytes)
    || !Buffer.isBuffer(policyAttestationBytes)
    || !Buffer.isBuffer(trustRegistryBytes)
    || sha256(trustRegistryBytes) !== trustRegistryExpectedSha256
  ) {
    throw new Error('publisher trust registry가 고정된 policy digest와 다릅니다.');
  }
  const approval = parseJson(approvalBytes, APPROVAL_ASSET_NAME);
  const policyAttestation = parseJson(
    policyAttestationBytes,
    'Platform policy attestation',
  );
  const trustedPublicKeys = parseTrustedPlatformReleaseKeys(
    parseJson(trustRegistryBytes, 'pinned trusted release key registry'),
  );
  const approvalPayload = verifyPlatformReleaseApproval(
    manifestBytes,
    approval,
    trustedPublicKeys,
  );
  validateGrant(grant, {
    approval,
    approvalBytes,
    manifestBytes,
    registryBytes: trustRegistryBytes,
  });
  const verifiedPolicyAttestation = verifyPolicyAttestation({
    approval,
    approvalBytes,
    attestation: policyAttestation,
    trustedPublicKeys,
  });
  return Object.freeze({
    approvalSha256: `sha256:${sha256(approvalBytes)}`,
    keyId: approval.keyId,
    manifestSha256: `sha256:${sha256(manifestBytes)}`,
    policyAttestation: verifiedPolicyAttestation,
    policyAttestationSha256: `sha256:${sha256(policyAttestationBytes)}`,
    sourceSha: approvalPayload.sourceSha,
    tag: approvalPayload.releaseTag,
  });
}

export async function publishPlatformFleetApproval(options) {
  exactKeys(
    options,
    [
      'approvalPath',
      'grant',
      'manifestPath',
      'policyAttestationPath',
      'repository',
      'token',
    ],
    'Platform Fleet approval production mutator',
  );
  const {
    approvalPath,
    grant,
    manifestPath,
    policyAttestationPath,
    repository,
    token,
  } = options;
  if (repository !== RELEASE_REPOSITORY) {
    throw new Error(`release repository가 올바르지 않습니다: ${repository}`);
  }
  requiredString(token, 'GitHub mutation adapter token');
  const fetchImpl = productionFetch;

  const [manifestBytes, approvalBytes, policyAttestationBytes, trustRegistryBytes] = await Promise.all([
    readBoundedFile(manifestPath, 'platform-release.json'),
    readBoundedFile(approvalPath, APPROVAL_ASSET_NAME),
    readBoundedFile(policyAttestationPath, 'Platform policy attestation'),
    readBoundedFile(TRUST_REGISTRY_PATH, 'pinned trust registry'),
  ]);
  const verified = verifyPlatformFleetApprovalPublishingInputs({
    approvalBytes,
    grant,
    manifestBytes,
    policyAttestationBytes,
    trustRegistryBytes,
    trustRegistryExpectedSha256: TRUST_REGISTRY_SHA256,
  });
  const manifest = parseJson(manifestBytes, 'platform-release.json');
  const tag = verified.tag;
  const sourceSha = verified.sourceSha;
  const expectedAssets = manifestAssets(manifest, manifestBytes);
  if (new Set(expectedAssets.map((asset) => asset.name)).size !== expectedAssets.length) {
    throw new Error('manifest base asset 이름이 중복되었습니다.');
  }

  const [initialPolicy, initialRelease, initialTagCommit] = await Promise.all([
    verifyImmutablePolicy(fetchImpl, token, verified.policyAttestation),
    getRelease(fetchImpl, token, tag, sourceSha),
    resolveTagCommit(fetchImpl, token, tag),
  ]);
  if (initialTagCommit !== sourceSha) {
    throw new Error('release tag commit이 승인된 Platform source SHA와 다릅니다.');
  }
  let indexed = await verifyBaseAssets(fetchImpl, token, initialRelease, expectedAssets);
  const existing = indexed.get(APPROVAL_ASSET_NAME);
  if (existing) {
    await verifyApprovalAsset(fetchImpl, token, existing, approvalBytes);
  } else {
    if (!initialRelease.draft) {
      throw new Error('immutable published release에 Fleet approval asset이 없습니다.');
    }
    const uploadResponse = await uploadApproval(fetchImpl, initialRelease, token, approvalBytes);
    if (!uploadResponse.ok && uploadResponse.status !== 422) {
      throw requestFailure('Fleet approval upload', uploadResponse);
    }
  }

  const [prePublishPolicy, prePublishRelease, prePublishTagCommit] = await Promise.all([
    verifyImmutablePolicy(fetchImpl, token, verified.policyAttestation),
    getRelease(fetchImpl, token, tag, sourceSha),
    resolveTagCommit(fetchImpl, token, tag),
  ]);
  if (
    prePublishPolicy.policyDigest !== initialPolicy.policyDigest
    || prePublishTagCommit !== sourceSha
    || prePublishRelease.id !== initialRelease.id
  ) {
    throw new Error('Fleet approval 게시 중 release 또는 보호 정책 경계가 변경되었습니다.');
  }
  indexed = await verifyBaseAssets(fetchImpl, token, prePublishRelease, expectedAssets);
  const persistedApproval = indexed.get(APPROVAL_ASSET_NAME);
  if (!persistedApproval) {
    throw new Error('Fleet approval 게시 후 fleet-approved.json을 readback하지 못했습니다.');
  }
  await verifyApprovalAsset(fetchImpl, token, persistedApproval, approvalBytes);

  if (prePublishRelease.draft) {
    await publishDraft(fetchImpl, token, prePublishRelease);
  }

  const [finalPolicy, finalRelease, finalTagCommit] = await Promise.all([
    verifyImmutablePolicy(fetchImpl, token, verified.policyAttestation),
    getRelease(fetchImpl, token, tag, sourceSha),
    resolveTagCommit(fetchImpl, token, tag),
  ]);
  if (
    finalPolicy.policyDigest !== initialPolicy.policyDigest
    || finalRelease.id !== initialRelease.id
    || finalRelease.draft !== false
    || finalRelease.immutable !== true
    || finalTagCommit !== sourceSha
  ) {
    throw new Error('GitHub immutable release 최종 상태가 승인 경계와 다릅니다.');
  }
  indexed = await verifyBaseAssets(fetchImpl, token, finalRelease, expectedAssets);
  const finalApproval = indexed.get(APPROVAL_ASSET_NAME);
  if (!finalApproval) {
    throw new Error('immutable release에 fleet-approved.json이 없습니다.');
  }
  await verifyApprovalAsset(fetchImpl, token, finalApproval, approvalBytes);
  return Object.freeze({
    approvalSha256: `sha256:${sha256(approvalBytes)}`,
    immutable: true,
    policyAttestationSha256: verified.policyAttestationSha256,
    policyDigest: finalPolicy.policyDigest,
    releaseId: finalRelease.id,
    sourceSha,
    tag,
  });
}

function parseInheritedFileDescriptor(value, label) {
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
      throw new Error('Fleet approval publisher 인자가 올바르지 않습니다.');
    }
    options[key] = value;
  }
  const required = [
    '--approval',
    '--grant-fd',
    '--manifest',
    '--policy-attestation',
    '--token-fd',
  ];
  if (
    Object.keys(options).some((key) => !required.includes(key))
    || required.some((key) => !options[key])
  ) {
    throw new Error('Fleet approval publisher 필수 인자가 없거나 알 수 없는 인자가 있습니다.');
  }
  const grantFd = parseInheritedFileDescriptor(options['--grant-fd'], 'broker grant');
  const tokenFd = parseInheritedFileDescriptor(options['--token-fd'], 'GitHub token');
  if (grantFd === tokenFd) {
    throw new Error('broker grant와 GitHub token FD는 달라야 합니다.');
  }
  return { ...options, grantFd, tokenFd };
}

function readInheritedInput(fd, label, maximum) {
  const bytes = readFileSync(fd);
  if (bytes.length < 1 || bytes.length > maximum) {
    throw new Error(`${label} inherited input 크기가 올바르지 않습니다.`);
  }
  return bytes;
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  let tokenBytes;
  try {
    const grantBytes = readInheritedInput(options.grantFd, 'broker grant', MAX_INPUT_BYTES);
    tokenBytes = readInheritedInput(options.tokenFd, 'GitHub token', 64 * 1024);
    const result = await publishPlatformFleetApproval({
      approvalPath: options['--approval'],
      grant: parseJson(grantBytes, 'broker grant'),
      manifestPath: options['--manifest'],
      policyAttestationPath: options['--policy-attestation'],
      repository: process.env.GITHUB_REPOSITORY ?? '',
      token: tokenBytes.toString('utf8').trim(),
    });
    process.stdout.write(`Fleet immutable approval 게시 확인: ${result.tag}\n`);
  } finally {
    tokenBytes?.fill(0);
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
