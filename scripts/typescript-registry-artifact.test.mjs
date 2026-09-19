import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, it } from 'node:test';

import { createDeterministicTarGz, sha256 } from './platform-release-lib.mjs';
import {
  fetchTypescriptRegistryArtifact,
  typescriptArtifactIntegrity,
  verifyTypescriptArtifactIntegrity,
} from './typescript-registry-artifact.mjs';

const PACKAGE_NAME = '@seorilabs/platform-sdk';
const VERSION = '0.4.0';

function artifact() {
  return createDeterministicTarGz([
    {
      path: 'package.json',
      content: Buffer.from(JSON.stringify({ name: PACKAGE_NAME, version: VERSION }), 'utf8'),
    },
    { path: 'dist/index.js', content: Buffer.from('export const ok = true;\n', 'utf8') },
  ], 'package');
}

function metadata(bytes, overrides = {}) {
  const shasum = createHash('sha1').update(bytes).digest('hex');
  return {
    integrity: typescriptArtifactIntegrity(bytes),
    shasum,
    tarball: `https://registry.npmjs.org/${PACKAGE_NAME}/-/platform-sdk-${VERSION}.tgz`,
    ...overrides,
  };
}

describe('TypeScript registry artifact byte 계약', () => {
  it('registry SRI와 shasum을 만족한 동일 byte만 release artifact로 저장한다', async (test) => {
    const directory = await mkdtemp(join(tmpdir(), 'typescript-registry-artifact-'));
    test.after(() => rm(directory, { recursive: true, force: true }));
    const bytes = artifact();
    const outputPath = join(directory, `seorilabs-platform-sdk-${VERSION}.tgz`);
    const requests = [];

    const result = await fetchTypescriptRegistryArtifact({
      fetchImpl: async (url, options) => {
        requests.push({ url, options });
        return new Response(bytes, {
          status: 200,
          headers: { 'Content-Length': String(bytes.length) },
        });
      },
      metadata: metadata(bytes),
      outputPath,
      version: VERSION,
    });

    assert.deepEqual(await readFile(outputPath), bytes);
    assert.equal(result.integrity, typescriptArtifactIntegrity(bytes));
    assert.equal(result.sha256, sha256(bytes));
    assert.equal(result.size, bytes.length);
    assert.equal(requests.length, 1);
    assert.equal(
      requests[0].url,
      `https://registry.npmjs.org/${PACKAGE_NAME}/-/platform-sdk-${VERSION}.tgz`,
    );
    assert.equal(requests[0].options.redirect, 'error');
    assert.equal(Object.hasOwn(requests[0].options.headers, 'Authorization'), false);
  });

  it('registry integrity가 다른 byte를 저장 전에 거부한다', async (test) => {
    const directory = await mkdtemp(join(tmpdir(), 'typescript-registry-integrity-'));
    test.after(() => rm(directory, { recursive: true, force: true }));
    const bytes = artifact();
    const corrupted = Buffer.concat([bytes, Buffer.from('corrupt')]);
    const outputPath = join(directory, `seorilabs-platform-sdk-${VERSION}.tgz`);

    await assert.rejects(
      fetchTypescriptRegistryArtifact({
        fetchImpl: async () => new Response(corrupted, { status: 200 }),
        metadata: metadata(bytes),
        outputPath,
        version: VERSION,
      }),
      /registry integrity/u,
    );
    await assert.rejects(readFile(outputPath), /ENOENT/u);
  });

  it('look-alike registry origin과 redirect 응답을 거부한다', async (test) => {
    const directory = await mkdtemp(join(tmpdir(), 'typescript-registry-origin-'));
    test.after(() => rm(directory, { recursive: true, force: true }));
    const bytes = artifact();
    const outputPath = join(directory, `seorilabs-platform-sdk-${VERSION}.tgz`);

    await assert.rejects(
      fetchTypescriptRegistryArtifact({
        fetchImpl: async () => new Response(bytes, { status: 200 }),
        metadata: metadata(bytes, {
          tarball: `https://registry.npmjs.example/${PACKAGE_NAME}/-/platform-sdk-${VERSION}.tgz`,
        }),
        outputPath,
        version: VERSION,
      }),
      /exact package 경계/u,
    );
    await assert.rejects(
      fetchTypescriptRegistryArtifact({
        fetchImpl: async () => new Response(null, {
          status: 302,
          headers: { Location: 'https://example.com/artifact.tgz' },
        }),
        metadata: metadata(bytes),
        outputPath,
        version: VERSION,
      }),
      /download 실패: status=302/u,
    );
  });

  it('builder handoff integrity도 artifact byte에 다시 결합한다', () => {
    const bytes = artifact();
    const integrity = typescriptArtifactIntegrity(bytes);
    assert.equal(verifyTypescriptArtifactIntegrity(bytes, integrity), integrity);
    assert.throws(
      () => verifyTypescriptArtifactIntegrity(Buffer.concat([bytes, Buffer.from('x')]), integrity),
      /registry integrity/u,
    );
  });
});
