import assert from 'node:assert/strict';
import { describe, it } from 'node:test';

import { resolvePlatformReleaseBase } from './resolve-platform-release-base.mjs';

const BOOTSTRAP = {
  schemaVersion: 1,
  purpose: 'seorilabs-platform-release-bootstrap-base-v1',
  releaseTag: 'v0.6.6',
  sourceSha: '6'.repeat(40),
};

function jsonResponse(value, status = 200) {
  const bytes = Buffer.from(JSON.stringify(value), 'utf8');
  return new Response(bytes, {
    status,
    headers: {
      'Content-Length': String(bytes.length),
      'Content-Type': 'application/json',
    },
  });
}

function bytesResponse(bytes, status = 200) {
  return new Response(bytes, {
    status,
    headers: { 'Content-Length': String(bytes.length) },
  });
}

function release({
  approved = true,
  id,
  immutable = true,
  publishedAt,
  sourceSha,
  tag,
}) {
  const manifestBytes = Buffer.from(JSON.stringify({ release: { sourceSha, tag } }), 'utf8');
  const approvalBytes = Buffer.from(JSON.stringify({
    payload: { releaseTag: tag, sourceSha },
  }), 'utf8');
  const asset = (assetId, name, bytes) => ({
    id: assetId,
    name,
    size: bytes.length,
    browser_download_url: `https://github.com/seorilabs/platform/releases/download/${tag}/${name}`,
  });
  const assets = [asset(id * 10 + 1, 'platform-release.json', manifestBytes)];
  if (approved) {
    assets.push(asset(id * 10 + 2, 'fleet-approved.json', approvalBytes));
  }
  return {
    provider: {
      id,
      tag_name: tag,
      target_commitish: sourceSha,
      draft: false,
      prerelease: false,
      immutable,
      published_at: publishedAt,
      assets,
    },
    downloads: new Map([
      [assets[0].browser_download_url, manifestBytes],
      ...(approved ? [[assets[1].browser_download_url, approvalBytes]] : []),
    ]),
  };
}

function githubFixture(entries) {
  const downloads = new Map(entries.flatMap((entry) => [...entry.downloads]));
  const calls = [];
  const fetchImpl = async (url) => {
    calls.push(url);
    if (url.startsWith('https://api.github.com/repos/seorilabs/platform/releases?')) {
      return jsonResponse(entries.map((entry) => entry.provider));
    }
    const bytes = downloads.get(url);
    return bytes ? bytesResponse(bytes) : bytesResponse(Buffer.from('not found'), 404);
  };
  return { calls, fetchImpl };
}

function verifiedPayload(_manifestBytes, approval) {
  return approval.payload;
}

describe('Platform release base resolver', () => {
  it('mutable latest를 무시하고 마지막 immutable Fleet approval source를 선택한다', async () => {
    const mutableLatest = release({
      id: 12,
      immutable: false,
      publishedAt: '2026-08-30T02:00:00.000Z',
      sourceSha: 'c'.repeat(40),
      tag: 'v0.7.2',
    });
    const approved = release({
      id: 11,
      publishedAt: '2026-08-29T02:00:00.000Z',
      sourceSha: 'b'.repeat(40),
      tag: 'v0.7.1',
    });
    const older = release({
      id: 10,
      publishedAt: '2026-08-28T02:00:00.000Z',
      sourceSha: 'a'.repeat(40),
      tag: 'v0.7.0',
    });
    const github = githubFixture([mutableLatest, older, approved]);

    const result = await resolvePlatformReleaseBase({
      bootstrap: BOOTSTRAP,
      fetchImpl: github.fetchImpl,
      trustedPublicKeys: new Map(),
      verifyApprovalImpl: verifiedPayload,
    });

    assert.deepEqual(result, {
      kind: 'fleet-approved',
      releaseId: 11,
      releaseTag: 'v0.7.1',
      sourceSha: 'b'.repeat(40),
    });
    assert.equal(github.calls.some((url) => url.includes('/v0.7.2/')), false);
    assert.equal(github.calls.some((url) => url.includes('/v0.7.1/')), true);
  });

  it('Fleet approval이 하나도 없을 때만 exact bootstrap base를 사용한다', async () => {
    const unapproved = release({
      approved: false,
      id: 12,
      publishedAt: '2026-08-30T02:00:00.000Z',
      sourceSha: 'c'.repeat(40),
      tag: 'v0.7.2',
    });
    const github = githubFixture([unapproved]);

    const result = await resolvePlatformReleaseBase({
      bootstrap: BOOTSTRAP,
      fetchImpl: github.fetchImpl,
      trustedPublicKeys: new Map(),
      verifyApprovalImpl: verifiedPayload,
    });

    assert.deepEqual(result, {
      kind: 'bootstrap',
      releaseTag: 'v0.6.6',
      sourceSha: '6'.repeat(40),
    });
  });

  it('가장 최근 immutable approval의 서명 검증 실패를 이전 release로 낮추지 않는다', async () => {
    const newest = release({
      id: 12,
      publishedAt: '2026-08-30T02:00:00.000Z',
      sourceSha: 'c'.repeat(40),
      tag: 'v0.7.2',
    });
    const older = release({
      id: 11,
      publishedAt: '2026-08-29T02:00:00.000Z',
      sourceSha: 'b'.repeat(40),
      tag: 'v0.7.1',
    });
    const github = githubFixture([older, newest]);

    await assert.rejects(
      resolvePlatformReleaseBase({
        bootstrap: BOOTSTRAP,
        fetchImpl: github.fetchImpl,
        trustedPublicKeys: new Map(),
        verifyApprovalImpl: () => {
          throw new Error('signature invalid');
        },
      }),
      /signature invalid/u,
    );
    assert.equal(github.calls.some((url) => url.includes('/v0.7.1/')), false);
  });

  it('look-alike release asset URL을 거부한다', async () => {
    const approved = release({
      id: 11,
      publishedAt: '2026-08-29T02:00:00.000Z',
      sourceSha: 'b'.repeat(40),
      tag: 'v0.7.1',
    });
    approved.provider.assets[0].browser_download_url =
      'https://github.example.com/seorilabs/platform/releases/download/v0.7.1/platform-release.json';
    const github = githubFixture([approved]);

    await assert.rejects(
      resolvePlatformReleaseBase({
        bootstrap: BOOTSTRAP,
        fetchImpl: github.fetchImpl,
        trustedPublicKeys: new Map(),
        verifyApprovalImpl: verifiedPayload,
      }),
      /exact release 경계/u,
    );
  });

  it('bootstrap은 full source SHA와 fixed semver tag만 허용한다', async () => {
    const github = githubFixture([]);
    await assert.rejects(
      resolvePlatformReleaseBase({
        bootstrap: { ...BOOTSTRAP, sourceSha: 'v0.6.6' },
        fetchImpl: github.fetchImpl,
        trustedPublicKeys: new Map(),
        verifyApprovalImpl: verifiedPayload,
      }),
      /bootstrap sourceSha/u,
    );
  });
});
