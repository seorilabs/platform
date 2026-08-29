import assert from 'node:assert/strict';
import { describe, it } from 'node:test';

import {
  assertPlatformReleaseTag,
  assertTypescriptPackageArtifact,
  canonicalJson,
  classifyContract,
  compareConformanceContracts,
  computeContractRevision,
  computeVendoredTreeChecksum,
  createDeterministicTarGz,
  createGdscriptRelease,
  deriveReleaseImpact,
  parseOasdiffJson,
  parseSupportedApiMajor,
  readTarGzEntries,
  sha256,
} from './platform-release-lib.mjs';

const jsonFile = (path, value) => ({
  path,
  content: Buffer.from(`${JSON.stringify(value)}\n`, 'utf8'),
});

describe('Platform 계약 revision', () => {
  it('파일 입력 순서와 무관하고 파일 이름과 내용을 함께 고정한다', () => {
    const files = [
      { path: 'spec/openapi.yaml', content: Buffer.from('openapi: 3.0.3\n') },
      jsonFile('spec/conformance/envelope.json', { version: 1 }),
    ];
    const first = computeContractRevision(files);
    const second = computeContractRevision([...files].reverse());
    assert.equal(first, second);
    assert.match(first, /^sha256:[0-9a-f]{64}$/u);
    assert.notEqual(
      first,
      computeContractRevision([{ ...files[0], path: 'spec/openapi-renamed.yaml' }, files[1]]),
    );
  });

  it('OpenAPI info.version의 major만 확정한다', () => {
    assert.equal(parseSupportedApiMajor('openapi: 3.0.3\ninfo:\n  title: x\n  version: 2.1.0\npaths: {}\n'), 2);
    assert.throws(() => parseSupportedApiMajor('openapi: 3.0.3\ninfo:\n  title: x\n'), /API major/u);
  });
});

describe('계약 변경 분류', () => {
  const emptyOasdiff = [];

  it('conformance 객체와 배열의 추가만 additive로 분류한다', () => {
    const before = [jsonFile('spec/conformance/a.json', {
      policy: { max: 3 },
      cases: [{ name: '기존', expect: { ok: true } }],
    })];
    const after = [jsonFile('spec/conformance/a.json', {
      policy: { max: 3, jitter: true },
      cases: [
        { name: '신규', expect: { ok: false } },
        { name: '기존', expect: { ok: true, code: 'ok' } },
      ],
    })];
    const comparison = compareConformanceContracts(before, after);
    assert.deepEqual(comparison.additiveFiles, ['spec/conformance/a.json']);
    assert.deepEqual(comparison.breakingFiles, []);
    assert.equal(classifyContract({
      apiMajorChanged: false,
      openapiChanged: false,
      changelog: emptyOasdiff,
      breaking: emptyOasdiff,
      conformance: comparison,
    }), 'contract-additive');
  });

  it('기존 conformance 값 변경과 파일 제거는 breaking으로 분류한다', () => {
    const before = [
      jsonFile('spec/conformance/a.json', { policy: { max: 3 } }),
      jsonFile('spec/conformance/b.json', { version: 1 }),
    ];
    const after = [jsonFile('spec/conformance/a.json', { policy: { max: 4 } })];
    const comparison = compareConformanceContracts(before, after);
    assert.deepEqual(comparison.breakingFiles, [
      'spec/conformance/a.json',
      'spec/conformance/b.json',
    ]);
    assert.equal(classifyContract({
      apiMajorChanged: false,
      openapiChanged: false,
      changelog: [],
      breaking: [],
      conformance: comparison,
    }), 'contract-breaking');
  });

  it('JSON 공백, 객체 키와 배열 순서만 바뀌면 의미 변경으로 세지 않는다', () => {
    const before = [jsonFile('spec/conformance/a.json', {
      policy: { max: 3, enabled: true },
      cases: [{ name: 'a' }, { name: 'b' }],
    })];
    const after = [{
      path: 'spec/conformance/a.json',
      content: Buffer.from(`{
        "cases": [{"name":"b"}, {"name":"a"}],
        "policy": {"enabled": true, "max": 3}
      }\n`),
    }];
    assert.deepEqual(compareConformanceContracts(before, after), {
      changedFiles: [],
      additiveFiles: [],
      breakingFiles: [],
    });
  });

  it('oasdiff가 보고한 OpenAPI 변경을 breaking 우선으로 분류한다', () => {
    const conformance = { changedFiles: [], additiveFiles: [], breakingFiles: [] };
    assert.equal(classifyContract({
      apiMajorChanged: false,
      openapiChanged: true,
      changelog: [{ id: 'api-path-added' }],
      breaking: [],
      conformance,
    }), 'contract-additive');
    assert.equal(classifyContract({
      apiMajorChanged: false,
      openapiChanged: true,
      changelog: [{ id: 'api-path-deleted' }],
      breaking: [{ id: 'api-path-deleted' }],
      conformance,
    }), 'contract-breaking');
  });

  it('지원 API major 변경은 다른 diff가 없어도 breaking이다', () => {
    assert.equal(classifyContract({
      apiMajorChanged: true,
      openapiChanged: true,
      changelog: [],
      breaking: [],
      conformance: { changedFiles: [], additiveFiles: [], breakingFiles: [] },
    }), 'contract-breaking');
  });

  it('형식을 알 수 없는 oasdiff와 conformance 입력은 fail-closed한다', () => {
    assert.throws(() => parseOasdiffJson('{"changes":[]}', 'changelog'), /형식/u);
    assert.throws(() => parseOasdiffJson('[{}]', 'breaking'), /형식/u);
    assert.throws(
      () => compareConformanceContracts(
        [jsonFile('spec/conformance/a.json', { version: 1 })],
        [{ path: 'spec/conformance/a.json', content: Buffer.from('{') }],
      ),
      /해석하지 못했습니다/u,
    );
    assert.throws(() => classifyContract({
      apiMajorChanged: false,
      openapiChanged: false,
      changelog: [{ id: 'endpoint-added' }],
      breaking: [],
      conformance: { changedFiles: [], additiveFiles: [], breakingFiles: [] },
    }), /일치하지 않습니다/u);
  });

  it('변경이 없거나 문서만 달라지면 implementation-only다', () => {
    assert.equal(classifyContract({
      apiMajorChanged: false,
      openapiChanged: true,
      changelog: [],
      breaking: [],
      conformance: { changedFiles: [], additiveFiles: [], breakingFiles: [] },
    }), 'implementation-only');
  });
});

describe('영향 범위', () => {
  it('계약 변경은 두 SDK track과 API/conformance capability를 함께 지정한다', () => {
    const result = deriveReleaseImpact({
      classification: 'contract-additive',
      releasedTrack: 'gdscript',
      changelog: [{ id: 'api-path-added', path: '/presence/heartbeat' }],
      breaking: [],
      conformance: {
        changedFiles: ['spec/conformance/param-normalization.json'],
        additiveFiles: ['spec/conformance/param-normalization.json'],
        breakingFiles: [],
      },
      changedPaths: [],
    });
    assert.deepEqual(result, {
      affectedConsumers: {
        cohort: 'backoffice-active-apps',
        resolution: 'reconcile-time',
      },
      affectedTracks: ['gdscript', 'typescript'],
      affectedCapabilities: ['events', 'presence'],
    });
  });

  it('구현 변경은 실제로 바뀐 SDK track만 지정한다', () => {
    const result = deriveReleaseImpact({
      classification: 'implementation-only',
      releasedTrack: 'gdscript',
      changelog: [],
      breaking: [],
      conformance: { changedFiles: [], additiveFiles: [], breakingFiles: [] },
      changedPaths: ['sdk-gdscript/addons/seorilabs_platform/core/presence_client.gd'],
    });
    assert.deepEqual(result, {
      affectedConsumers: {
        cohort: 'backoffice-active-apps',
        resolution: 'reconcile-time',
      },
      affectedTracks: ['gdscript'],
      affectedCapabilities: ['presence'],
    });
  });

  it('첫 Fleet release도 발행한 GDScript track과 core 영향을 남긴다', () => {
    const result = deriveReleaseImpact({
      classification: 'implementation-only',
      releasedTrack: 'gdscript',
      changelog: [],
      breaking: [],
      conformance: { changedFiles: [], additiveFiles: [], breakingFiles: [] },
      changedPaths: [],
    });
    assert.deepEqual(result, {
      affectedConsumers: {
        cohort: 'backoffice-active-apps',
        resolution: 'reconcile-time',
      },
      affectedTracks: ['gdscript'],
      affectedCapabilities: ['core'],
    });
  });
});

describe('immutable GDScript release', () => {
  const payload = [
    {
      path: 'platform_client.gd',
      content: Buffer.from('extends Node\nconst SDK_VERSION := "0.6.5"\n'),
    },
    { path: 'core/backoff.gd', content: Buffer.from('class_name Backoff\n') },
    { path: 'SOURCE', content: Buffer.from('https://github.com/seorilabs/platform/tree/main/sdk-gdscript\n') },
  ];

  it('같은 입력으로 byte-identical tar.gz와 checksum을 만든다', () => {
    const first = createGdscriptRelease({ version: '0.6.5', releaseTag: 'v0.6.5', payloadFiles: payload });
    const second = createGdscriptRelease({
      version: '0.6.5',
      releaseTag: 'v0.6.5',
      payloadFiles: [...payload].reverse(),
    });
    assert.equal(sha256(first.archive), sha256(second.archive));
    assert.equal(first.treeChecksum, second.treeChecksum);

    const entries = readTarGzEntries(first.archive);
    const root = 'seorilabs_platform/';
    assert.equal(
      entries.get(`${root}SOURCE`).toString('utf8'),
      `https://github.com/seorilabs/platform/releases/download/v0.6.5/${first.artifactName}\n`,
    );
    assert.equal(entries.get(`${root}VERSION`).toString('utf8'), '0.6.5\n');
    assert.equal(entries.get(`${root}CHECKSUM`).toString('utf8'), `${first.treeChecksum}\n`);
    assert.equal(computeVendoredTreeChecksum(first.releaseFiles), first.treeChecksum);
    assert.equal(first.checksumArtifact.toString('utf8'), `${first.artifactSha256}  ${first.artifactName}\n`);
  });

  it('floating tag와 VERSION 불일치를 거부한다', () => {
    assert.throws(() => assertPlatformReleaseTag('main', '0.6.5'), /tag 형식/u);
    assert.throws(() => assertPlatformReleaseTag('v0.6.4', '0.6.5'), /GDScript/u);
    assert.throws(() => createGdscriptRelease({
      version: '0.6.4',
      releaseTag: 'v0.6.4',
      payloadFiles: payload,
    }), /내부 버전/u);
  });
});

describe('TypeScript artifact와 canonical manifest', () => {
  it('npm tarball 내부 package 식별자를 검증한다', () => {
    const archive = createDeterministicTarGz([
      {
        path: 'package.json',
        content: Buffer.from('{"name":"@seorilabs/platform-sdk","version":"0.4.0"}\n'),
      },
    ], 'package');
    assert.doesNotThrow(() => assertTypescriptPackageArtifact(
      archive,
      '@seorilabs/platform-sdk',
      '0.4.0',
    ));
    assert.throws(() => assertTypescriptPackageArtifact(
      archive,
      '@seorilabs/platform-sdk',
      '0.5.0',
    ), /식별자/u);
  });

  it('manifest는 시간 필드 없이 안정적인 JSON byte를 만든다', () => {
    const value = { schemaVersion: 1, release: { tag: 'v0.6.5' } };
    assert.equal(canonicalJson(value), canonicalJson(value));
    assert.equal(canonicalJson(value).endsWith('\n'), true);
    assert.equal(canonicalJson(value).includes('generatedAt'), false);
  });
});
