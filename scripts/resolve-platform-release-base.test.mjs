import assert from 'node:assert/strict';
import { describe, it } from 'node:test';

import {
  resolvePlatformReleaseBase,
  selectPlatformReleaseBase,
} from './resolve-platform-release-base.mjs';

function release({
  draft = false,
  id,
  immutable = true,
  prerelease = false,
  publishedAt,
  sourceSha,
  tag,
}) {
  return {
    id,
    tag_name: tag,
    target_commitish: sourceSha,
    draft,
    prerelease,
    immutable,
    published_at: draft ? null : publishedAt,
  };
}

// 2026-10-10 실제 release 목록의 모양을 줄였다. 승인을 기다리다 멈춘 draft와
// TypeScript 전용 tag, 초기 prerelease가 함께 있다.
const providerReleases = [
  release({ id: 9, tag: 'v0.7.9', draft: true, sourceSha: '9'.repeat(40) }),
  release({ id: 8, tag: 'v0.7.9', draft: true, sourceSha: '9'.repeat(40) }),
  release({ id: 7, tag: 'v0.7.7', draft: true, sourceSha: '7'.repeat(40) }),
  release({ id: 10, tag: 'v0.9.0', publishedAt: '2026-10-09T15:12:07Z', sourceSha: 'a'.repeat(40) }),
  release({ id: 6, tag: 'v0.7.8', publishedAt: '2026-09-09T13:52:26Z', sourceSha: 'b'.repeat(40) }),
  release({
    id: 3,
    tag: 'v0.6.5',
    prerelease: true,
    immutable: false,
    publishedAt: '2026-08-28T02:54:10Z',
    sourceSha: 'main',
  }),
  release({
    id: 2,
    tag: 'sdk-ts-v0.4.0',
    immutable: false,
    publishedAt: '2026-08-26T14:54:17Z',
    sourceSha: 'main',
  }),
];

describe('Platform release base resolver', () => {
  it('draft를 무시하고 가장 최근에 공개된 immutable release를 기준으로 삼는다', () => {
    assert.deepEqual(selectPlatformReleaseBase(providerReleases, 'v0.9.1'), {
      releaseId: 10,
      releaseTag: 'v0.9.0',
      sourceSha: 'a'.repeat(40),
    });
  });

  it('지금 만드는 tag의 공개 release는 기준에서 뺀다', () => {
    assert.deepEqual(selectPlatformReleaseBase(providerReleases, 'v0.9.0'), {
      releaseId: 6,
      releaseTag: 'v0.7.8',
      sourceSha: 'b'.repeat(40),
    });
  });

  it('prerelease, mutable release, 다른 형식의 tag는 기준이 되지 않는다', () => {
    const releases = [
      release({ id: 4, tag: 'v0.8.0', immutable: false, publishedAt: '2026-10-01T00:00:00Z', sourceSha: 'c'.repeat(40) }),
      release({ id: 5, tag: 'v0.8.1', prerelease: true, publishedAt: '2026-10-02T00:00:00Z', sourceSha: 'd'.repeat(40) }),
      release({ id: 11, tag: 'sdk-ts-v0.6.0', publishedAt: '2026-10-03T00:00:00Z', sourceSha: 'e'.repeat(40) }),
      ...providerReleases,
    ];
    assert.equal(selectPlatformReleaseBase(releases, 'v0.9.1').releaseTag, 'v0.9.0');
  });

  it('공개된 기준 release가 없으면 실패한다', () => {
    const drafts = providerReleases.filter(({ draft }) => draft);
    assert.throws(() => selectPlatformReleaseBase(drafts, 'v0.9.1'), /공개된 immutable SDK release가 없습니다/u);
  });

  it('가장 최근 공개 release의 source가 commit SHA가 아니면 이전 release로 낮추지 않는다', () => {
    const releases = [
      release({ id: 12, tag: 'v0.9.1', publishedAt: '2026-10-11T00:00:00Z', sourceSha: 'main' }),
      ...providerReleases,
    ];
    assert.throws(() => selectPlatformReleaseBase(releases, 'v0.9.2'), /40자리 commit/u);
  });

  it('release tag 인자는 vX.Y.Z만 받는다', () => {
    assert.throws(() => selectPlatformReleaseBase(providerReleases, 'sdk-ts-v0.6.0'), /release tag 형식/u);
  });

  it('GitHub release 목록을 페이지 끝까지 읽는다', async () => {
    const calls = [];
    const firstPage = Array.from({ length: 100 }, (_, index) => release({
      id: 100 + index,
      tag: 'v0.7.9',
      draft: true,
      sourceSha: '9'.repeat(40),
    }));
    const fetchImpl = async (url) => {
      calls.push(url);
      const page = new URL(url).searchParams.get('page');
      const body = JSON.stringify(page === '1' ? firstPage : providerReleases);
      return new Response(body, { headers: { 'Content-Length': String(Buffer.byteLength(body)) } });
    };
    const result = await resolvePlatformReleaseBase({ fetchImpl, releaseTag: 'v0.9.1', token: 'test-token' });
    assert.equal(result.releaseTag, 'v0.9.0');
    assert.equal(calls.length, 2);
    assert.equal(
      calls.every((url) => url.startsWith('https://api.github.com/repos/seorilabs/platform/releases?')),
      true,
    );
  });
});
