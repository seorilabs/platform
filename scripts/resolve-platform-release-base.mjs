#!/usr/bin/env node
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const GITHUB_API_BASE = 'https://api.github.com';
const GITHUB_REPOSITORY = 'seorilabs/platform';
const API_VERSION = '2026-03-10';
// release 하나의 목록 JSON은 asset 다섯 개 기준 약 6KB다. 본문이 긴 release가 섞여도
// 한 페이지(100개)가 들어가도록 여유를 둔다.
const MAX_PAGE_BYTES = 4 * 1024 * 1024;
const MAX_RELEASE_PAGES = 10;
const RELEASES_PER_PAGE = 100;
const SOURCE_SHA_PATTERN = /^[0-9a-f]{40}$/u;
const RELEASE_TAG_PATTERN = /^v\d+\.\d+\.\d+$/u;

function isRecord(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function parseJson(bytes, label) {
  try {
    return JSON.parse(bytes.toString('utf8'));
  } catch (error) {
    throw new Error(`${label} JSON을 해석하지 못했습니다.`, { cause: error });
  }
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

async function listReleases(fetchImpl, token) {
  const headers = {
    Accept: 'application/vnd.github+json',
    'X-GitHub-Api-Version': API_VERSION,
    'User-Agent': 'seorilabs-platform-release-base',
  };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const releases = [];
  for (let page = 1; page <= MAX_RELEASE_PAGES; page += 1) {
    const url = `${GITHUB_API_BASE}/repos/${GITHUB_REPOSITORY}/releases`
      + `?per_page=${RELEASES_PER_PAGE}&page=${page}`;
    const response = await fetchImpl(url, { headers, redirect: 'error' });
    if (!response.ok) {
      throw new Error(`GitHub release 목록 조회 실패: status=${response.status}`);
    }
    const batch = parseJson(
      await readResponseBounded(response, MAX_PAGE_BYTES, 'GitHub release 목록'),
      'GitHub release 목록',
    );
    if (!Array.isArray(batch)) {
      throw new Error('GitHub release 목록 형식이 올바르지 않습니다.');
    }
    releases.push(...batch);
    if (batch.length < RELEASES_PER_PAGE) {
      return releases;
    }
  }
  throw new Error('GitHub release 목록이 탐색 범위를 초과했습니다.');
}

// 계약 분류의 기준은 소비 앱이 실제로 받을 수 있었던 마지막 SDK release다.
// - draft는 공개되지 않았으므로 기준이 될 수 없다. 승인 체계 시절 공개되지 못한
//   v0.7.4·v0.7.7·v0.7.9 draft가 남아 있다.
// - prerelease와 `sdk-ts-v*` 같은 다른 형식의 tag는 통합 SDK release가 아니다.
// - immutable이어야 tag와 asset이 기준으로 고정된다. 초기 v0.6.6·v0.6.7은 mutable이다.
// - 지금 만드는 tag 자신은 제외한다. 같은 tag의 재실행이나 VERSION을 올리기 전 PR check가
//   자기 release를 기준으로 삼으면 base와 source가 같아진다.
export function selectPlatformReleaseBase(releases, releaseTag) {
  if (!Array.isArray(releases)) {
    throw new Error('GitHub release 목록 형식이 올바르지 않습니다.');
  }
  if (typeof releaseTag !== 'string' || !RELEASE_TAG_PATTERN.test(releaseTag)) {
    throw new Error(`release tag 형식이 올바르지 않습니다: ${releaseTag}`);
  }
  const candidates = releases
    .filter((release) => (
      isRecord(release)
      && release.draft === false
      && release.prerelease === false
      && release.immutable === true
      && typeof release.tag_name === 'string'
      && RELEASE_TAG_PATTERN.test(release.tag_name)
      && release.tag_name !== releaseTag
    ))
    .map((release) => {
      const publishedAt = Date.parse(release.published_at);
      if (!Number.isSafeInteger(release.id) || release.id < 1 || !Number.isFinite(publishedAt)) {
        throw new Error(`공개 release ${release.tag_name}의 id 또는 published_at이 올바르지 않습니다.`);
      }
      return { release, publishedAt };
    })
    .sort((left, right) => (
      right.publishedAt - left.publishedAt || right.release.id - left.release.id
    ));
  if (candidates.length === 0) {
    throw new Error(`${releaseTag} 이전에 공개된 immutable SDK release가 없습니다.`);
  }
  const { release } = candidates[0];
  // 가장 최근 공개 release의 source가 비정상이면 더 오래된 release로 낮추지 않는다.
  // 기준을 조용히 바꾸면 계약 분류가 실제 소비 앱 상태와 어긋난다.
  if (typeof release.target_commitish !== 'string' || !SOURCE_SHA_PATTERN.test(release.target_commitish)) {
    throw new Error(`공개 release ${release.tag_name}의 source SHA가 40자리 commit이 아닙니다.`);
  }
  return Object.freeze({
    releaseId: release.id,
    releaseTag: release.tag_name,
    sourceSha: release.target_commitish,
  });
}

export async function resolvePlatformReleaseBase({ fetchImpl = fetch, releaseTag, token = '' }) {
  if (typeof fetchImpl !== 'function') {
    throw new Error('GitHub release 조회 adapter가 올바르지 않습니다.');
  }
  return selectPlatformReleaseBase(await listReleases(fetchImpl, token), releaseTag);
}

async function main() {
  const [releaseTag = ''] = process.argv.slice(2);
  if (!releaseTag || process.argv.length !== 3) {
    throw new Error('사용법: node scripts/resolve-platform-release-base.mjs <vX.Y.Z>');
  }
  const token = process.env.GITHUB_TOKEN;
  if (!token) {
    throw new Error('GITHUB_TOKEN이 필요합니다.');
  }
  const result = await resolvePlatformReleaseBase({ releaseTag, token });
  process.stderr.write(
    `Platform release base: ${result.releaseTag} ${result.sourceSha} (${releaseTag} 기준)\n`,
  );
  process.stdout.write(`${result.sourceSha}\n`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
