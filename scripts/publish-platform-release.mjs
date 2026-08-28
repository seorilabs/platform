#!/usr/bin/env node
import { spawnSync } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import { basename, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { platformReleaseIdentity } from './platform-fleet-reconciler.mjs';
import { sha256 } from './platform-release-lib.mjs';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const API_VERSION = '2022-11-28';
const RELEASE_REPOSITORY = 'seorilabs/platform';

function requiredString(value, label) {
  if (typeof value !== 'string' || value.length === 0) {
    throw new Error(`${label}이 비어 있습니다.`);
  }
  return value;
}

function safeAssetName(value, label) {
  const name = requiredString(value, label);
  if (basename(name) !== name || !/^[A-Za-z0-9._-]+$/u.test(name)) {
    throw new Error(`${label}이 안전한 asset 이름이 아닙니다: ${name}`);
  }
  return name;
}

function requiredPositiveSize(value, label) {
  if (!Number.isSafeInteger(value) || value < 1) {
    throw new Error(`${label}이 올바르지 않습니다.`);
  }
  return value;
}

async function githubRequest(fetchImpl, url, token, options = {}) {
  const response = await fetchImpl(url, {
    ...options,
    headers: {
      Accept: options.accept ?? 'application/vnd.github+json',
      Authorization: `Bearer ${token}`,
      'X-GitHub-Api-Version': API_VERSION,
      'User-Agent': 'seorilabs-platform-release',
      ...options.headers,
    },
  });
  if (!response.ok) {
    const detail = (await response.text()).slice(0, 1000);
    throw new Error(`GitHub API ${response.status} ${response.statusText}: ${detail}`);
  }
  return response;
}

async function loadReleaseAssets(directory, tag) {
  const manifestPath = resolve(directory, 'platform-release.json');
  const manifestContent = await readFile(manifestPath);
  let manifest;
  try {
    manifest = JSON.parse(manifestContent.toString('utf8'));
  } catch (error) {
    throw new Error('platform-release.json을 해석하지 못했습니다.', { cause: error });
  }
  const releaseIdentity = platformReleaseIdentity(manifestContent);
  if (manifest.schemaVersion !== 1 || manifest.release?.tag !== tag) {
    throw new Error(`manifest release tag가 실행 tag와 다릅니다: ${manifest.release?.tag}`);
  }
  if (
    releaseIdentity.releaseTag !== tag
    || releaseIdentity.sourceSha !== manifest.release.sourceSha
  ) {
    throw new Error('manifest release identity가 검증 결과와 다릅니다.');
  }

  const typescript = manifest.sdk?.typescript;
  const gdscript = manifest.sdk?.gdscript;
  const declared = [
    {
      name: safeAssetName(typescript?.artifact?.name, 'TypeScript artifact name'),
      digest: requiredString(typescript?.artifact?.sha256, 'TypeScript artifact sha256'),
      size: requiredPositiveSize(typescript?.artifact?.size, 'TypeScript artifact size'),
      contentType: 'application/gzip',
    },
    {
      name: safeAssetName(gdscript?.artifact?.name, 'GDScript artifact name'),
      digest: requiredString(gdscript?.artifact?.sha256, 'GDScript artifact sha256'),
      size: requiredPositiveSize(gdscript?.artifact?.size, 'GDScript artifact size'),
      contentType: 'application/gzip',
    },
    {
      name: safeAssetName(gdscript?.checksumArtifact?.name, 'checksum artifact name'),
      digest: requiredString(gdscript?.checksumArtifact?.sha256, 'checksum artifact sha256'),
      size: requiredPositiveSize(gdscript?.checksumArtifact?.size, 'checksum artifact size'),
      contentType: 'text/plain; charset=utf-8',
    },
    {
      name: 'platform-release.json',
      digest: sha256(manifestContent),
      contentType: 'application/json',
    },
  ];
  const assets = [];
  for (const asset of declared) {
    if (!/^[0-9a-f]{64}$/u.test(asset.digest)) {
      throw new Error(`${asset.name} sha256 형식이 올바르지 않습니다.`);
    }
    const content = await readFile(resolve(directory, asset.name));
    if (asset.size !== undefined && content.length !== asset.size) {
      throw new Error(`${asset.name} size가 manifest와 다릅니다.`);
    }
    const actual = sha256(content);
    if (actual !== asset.digest) {
      throw new Error(`${asset.name} digest가 manifest와 다릅니다.`);
    }
    assets.push({ ...asset, content });
  }
  return { manifest, assets };
}

async function findRelease(fetchImpl, apiBase, repository, token, tag) {
  const url = `${apiBase}/repos/${repository}/releases?per_page=100`;
  const response = await githubRequest(fetchImpl, url, token);
  const releases = await response.json();
  if (!Array.isArray(releases)) {
    throw new Error('GitHub release 목록 형식이 올바르지 않습니다.');
  }
  return releases.find((release) => release.tag_name === tag);
}

async function verifyExistingAsset(fetchImpl, token, remote, local) {
  if (remote.size !== local.content.length) {
    throw new Error(`기존 release asset 크기가 다릅니다: ${local.name}`);
  }
  const response = await githubRequest(fetchImpl, remote.url, token, {
    accept: 'application/octet-stream',
  });
  const content = Buffer.from(await response.arrayBuffer());
  if (sha256(content) !== local.digest) {
    throw new Error(`기존 release asset digest가 다릅니다: ${local.name}`);
  }
}

async function verifyReleaseAssets(fetchImpl, token, release, assets) {
  if (!Array.isArray(release.assets)) {
    throw new Error('GitHub release asset 목록 형식이 올바르지 않습니다.');
  }
  const expectedNames = new Set(assets.map(({ name }) => name));
  const unexpected = release.assets.filter(({ name }) => !expectedNames.has(name));
  if (unexpected.length > 0) {
    throw new Error(`release에 예상하지 않은 asset이 있습니다: ${unexpected.map(({ name }) => name).join(', ')}`);
  }
  for (const asset of assets) {
    const existing = release.assets.find(({ name }) => name === asset.name);
    if (!existing) {
      throw new Error(`release에 asset이 없습니다: ${asset.name}`);
    }
    await verifyExistingAsset(fetchImpl, token, existing, asset);
  }
}

export async function publishPlatformRelease({
  apiBase,
  directory,
  fetchImpl = fetch,
  repository,
  sourceSha,
  tag,
  token,
}) {
  if (repository !== RELEASE_REPOSITORY) {
    throw new Error(`release repository가 올바르지 않습니다: ${repository}`);
  }
  if (!/^v\d+\.\d+\.\d+$/u.test(tag)) {
    throw new Error(`release tag 형식이 올바르지 않습니다: ${tag}`);
  }
  requiredString(token, 'GITHUB_TOKEN');
  const { manifest, assets } = await loadReleaseAssets(directory, tag);
  if (manifest.release.sourceSha !== sourceSha) {
    throw new Error(`manifest sourceSha가 workflow source SHA와 다릅니다: ${sourceSha}`);
  }
  let release = await findRelease(fetchImpl, apiBase, repository, token, tag);

  if (!release) {
    const response = await githubRequest(
      fetchImpl,
      `${apiBase}/repos/${repository}/releases`,
      token,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          tag_name: tag,
          target_commitish: manifest.release.sourceSha,
          name: `Platform SDK ${tag}`,
          body: [
            `Contract classification: \`${manifest.contract.classification}\``,
            `Contract revision: \`${manifest.contract.revision}\``,
            `Source SHA: \`${manifest.release.sourceSha}\``,
          ].join('\n\n'),
          draft: true,
          prerelease: false,
          generate_release_notes: false,
        }),
      },
    );
    release = await response.json();
  }
  if (
    !Number.isSafeInteger(release.id)
    || release.tag_name !== tag
    || !Array.isArray(release.assets)
  ) {
    throw new Error('GitHub release 응답 형식이 올바르지 않습니다.');
  }

  const expectedNames = new Set(assets.map(({ name }) => name));
  const unexpected = release.assets.filter(({ name }) => !expectedNames.has(name));
  if (unexpected.length > 0) {
    throw new Error(`release에 예상하지 않은 asset이 있습니다: ${unexpected.map(({ name }) => name).join(', ')}`);
  }

  for (const asset of assets) {
    const existing = release.assets.find(({ name }) => name === asset.name);
    if (existing) {
      await verifyExistingAsset(fetchImpl, token, existing, asset);
      continue;
    }
    if (!release.draft) {
      throw new Error(`공개된 immutable release에 asset이 없습니다: ${asset.name}`);
    }
    const uploadUrl = requiredString(release.upload_url, 'release upload_url')
      .replace(/\{.*$/u, '');
    await githubRequest(
      fetchImpl,
      `${uploadUrl}?name=${encodeURIComponent(asset.name)}`,
      token,
      {
        method: 'POST',
        accept: 'application/vnd.github+json',
        headers: {
          'Content-Type': asset.contentType,
          'Content-Length': String(asset.content.length),
        },
        body: asset.content,
      },
    );
  }

  if (release.draft) {
    const verificationResponse = await githubRequest(
      fetchImpl,
      `${apiBase}/repos/${repository}/releases/${release.id}`,
      token,
    );
    const persistedRelease = await verificationResponse.json();
    if (
      persistedRelease.id !== release.id
      || persistedRelease.tag_name !== tag
      || persistedRelease.draft !== true
    ) {
      throw new Error('draft release readback이 실행 경계와 다릅니다.');
    }
    await verifyReleaseAssets(fetchImpl, token, persistedRelease, assets);
    const publishResponse = await githubRequest(
      fetchImpl,
      `${apiBase}/repos/${repository}/releases/${release.id}`,
      token,
      {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ draft: false }),
      },
    );
    const publishedRelease = await publishResponse.json();
    if (publishedRelease.tag_name !== tag || publishedRelease.draft !== false) {
      throw new Error('GitHub Release 공개 상태를 확인하지 못했습니다.');
    }
  }
  return { releaseId: release.id, tag };
}

async function main() {
  const [directory = ''] = process.argv.slice(2);
  if (!directory || process.argv.length !== 3) {
    throw new Error('사용법: node scripts/publish-platform-release.mjs <release-directory>');
  }
  const sourceResult = spawnSync(
    'git',
    ['rev-parse', '--verify', `${process.env.GITHUB_SHA ?? ''}^{commit}`],
    { cwd: repoRoot, encoding: 'utf8' },
  );
  const sourceSha = sourceResult.status === 0 ? sourceResult.stdout.trim() : '';
  if (!/^[0-9a-f]{40}$/u.test(sourceSha)) {
    throw new Error('GITHUB_SHA를 source commit으로 확정하지 못했습니다.');
  }
  const result = await publishPlatformRelease({
    apiBase: process.env.GITHUB_API_URL ?? 'https://api.github.com',
    directory: resolve(repoRoot, directory),
    repository: process.env.GITHUB_REPOSITORY ?? '',
    sourceSha,
    tag: process.env.GITHUB_REF_NAME ?? '',
    token: process.env.GITHUB_TOKEN ?? '',
  });
  console.log(`GitHub Release 발행 완료: ${result.tag} (id=${result.releaseId})`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
