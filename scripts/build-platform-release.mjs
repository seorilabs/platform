#!/usr/bin/env node
import { spawnSync } from 'node:child_process';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { basename, dirname, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  assertTypescriptPackageArtifact,
  canonicalJson,
  classifyContract,
  compareConformanceContracts,
  computeContractRevision,
  createGdscriptRelease,
  deriveReleaseImpact,
  parseOasdiffJson,
  parseSupportedApiMajor,
  sha256,
} from './platform-release-lib.mjs';
import { verifyTypescriptArtifactIntegrity } from './typescript-registry-artifact.mjs';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const OPENAPI_PATH = 'spec/openapi.yaml';
const CONFORMANCE_PREFIX = 'spec/conformance/';
const GDSCRIPT_PREFIX = 'sdk-gdscript/addons/seorilabs_platform/';

function run(command, args, { encoding = 'utf8', label = command } = {}) {
  const result = spawnSync(command, args, {
    cwd: repoRoot,
    encoding,
    maxBuffer: 16 * 1024 * 1024,
  });
  if (result.error) {
    throw new Error(`${label} 실행에 실패했습니다.`, { cause: result.error });
  }
  if (result.status !== 0) {
    const stderr = Buffer.isBuffer(result.stderr)
      ? result.stderr.toString('utf8')
      : result.stderr;
    throw new Error(`${label} 실행에 실패했습니다: ${(stderr || '').trim()}`);
  }
  return result.stdout;
}

function assertSafeRevision(revision) {
  if (
    typeof revision !== 'string'
    || revision.length === 0
    || revision.startsWith('-')
    || revision.includes('..')
    || revision.includes('@{')
    || !/^[A-Za-z0-9._/~^-]+$/u.test(revision)
  ) {
    throw new Error(`Git revision 형식이 올바르지 않습니다: ${revision}`);
  }
}

function resolveRevision(revision) {
  assertSafeRevision(revision);
  const resolved = run('git', ['rev-parse', '--verify', `${revision}^{commit}`], {
    label: `Git revision ${revision}`,
  }).trim();
  if (!/^[0-9a-f]{40}$/u.test(resolved)) {
    throw new Error(`Git revision을 40자리 SHA로 확정하지 못했습니다: ${revision}`);
  }
  return resolved;
}

function resolveBaseRevision(sourceSha, requestedBase) {
  if (!requestedBase) {
    throw new Error('검증된 Fleet 승인 또는 bootstrap base revision이 필요합니다.');
  }
  const baseSha = resolveRevision(requestedBase);
  run('git', ['merge-base', '--is-ancestor', baseSha, sourceSha], {
    label: 'base revision ancestor 확인',
  });
  if (baseSha === sourceSha) {
    throw new Error('base revision은 source SHA와 달라야 합니다.');
  }
  return baseSha;
}

function parseTree(output) {
  const records = Buffer.from(output).toString('utf8').split('\0').filter(Boolean);
  return records.map((record) => {
    const separator = record.indexOf('\t');
    if (separator < 0) {
      throw new Error('git ls-tree 결과를 해석하지 못했습니다.');
    }
    const [mode, type, object] = record.slice(0, separator).split(' ');
    const path = record.slice(separator + 1);
    if (type !== 'blob' || !['100644', '100755'].includes(mode)) {
      throw new Error(`release 입력에는 일반 파일만 허용됩니다: ${path} (${mode} ${type})`);
    }
    return { mode, object, path };
  });
}

function listTree(revision, pathspecs) {
  const output = run('git', ['ls-tree', '-r', '-z', '--full-tree', revision, '--', ...pathspecs], {
    encoding: null,
    label: `Git tree ${revision}`,
  });
  return parseTree(output);
}

function readBlob(revision, path) {
  return run('git', ['show', `${revision}:${path}`], {
    encoding: null,
    label: `Git blob ${revision}:${path}`,
  });
}

function readContractFiles(revision) {
  const entries = listTree(revision, [OPENAPI_PATH, CONFORMANCE_PREFIX]);
  const files = entries
    .filter(({ path }) => path === OPENAPI_PATH || (
      path.startsWith(CONFORMANCE_PREFIX) && path.endsWith('.json')
    ))
    .map(({ path }) => ({ path, content: readBlob(revision, path) }));
  if (!files.some(({ path }) => path === OPENAPI_PATH)) {
    throw new Error(`${revision}에 ${OPENAPI_PATH}가 없습니다.`);
  }
  return files;
}

function readGdscriptPayload(revision) {
  return listTree(revision, [GDSCRIPT_PREFIX])
    .filter(({ path }) => !path.startsWith(`${GDSCRIPT_PREFIX}tools/`))
    .map(({ path }) => ({
      path: path.slice(GDSCRIPT_PREFIX.length),
      content: readBlob(revision, path),
    }));
}

function readText(revision, path) {
  return readBlob(revision, path).toString('utf8').trim();
}

function readJson(revision, path) {
  try {
    return JSON.parse(readBlob(revision, path).toString('utf8'));
  } catch (error) {
    throw new Error(`${revision}:${path} JSON을 해석하지 못했습니다.`, { cause: error });
  }
}

function listChangedPaths(baseSha, sourceSha) {
  return Buffer.from(run('git', ['diff', '--name-only', '-z', baseSha, sourceSha], {
    encoding: null,
    label: 'release 변경 경로 조회',
  })).toString('utf8').split('\0').filter(Boolean);
}

function runOasdiff(executable, command, baseSha, sourceSha) {
  const output = run(executable, [
    command,
    '-f',
    'json',
    `${baseSha}:${OPENAPI_PATH}`,
    `${sourceSha}:${OPENAPI_PATH}`,
  ], { encoding: null, label: `oasdiff ${command}` });
  return parseOasdiffJson(output, command);
}

function parseArguments(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!name?.startsWith('--') || value === undefined) {
      throw new Error(`인자가 올바르지 않습니다: ${name ?? ''}`);
    }
    if (Object.hasOwn(options, name)) {
      throw new Error(`인자가 중복되었습니다: ${name}`);
    }
    options[name] = value;
  }
  const allowed = new Set([
    '--base-ref',
    '--oasdiff',
    '--output-dir',
    '--release-tag',
    '--source-sha',
    '--typescript-artifact',
    '--typescript-registry-integrity',
  ]);
  for (const name of Object.keys(options)) {
    if (!allowed.has(name)) {
      throw new Error(`알 수 없는 인자입니다: ${name}`);
    }
  }
  for (const required of [
    '--oasdiff',
    '--output-dir',
    '--release-tag',
    '--source-sha',
    '--typescript-artifact',
    '--base-ref',
    '--typescript-registry-integrity',
  ]) {
    if (!options[required]) {
      throw new Error(`필수 인자가 없습니다: ${required}`);
    }
  }
  return options;
}

export async function buildPlatformRelease(options) {
  const sourceSha = resolveRevision(options['--source-sha']);
  const baseSourceSha = resolveBaseRevision(sourceSha, options['--base-ref']);
  const releaseTag = options['--release-tag'];
  const currentContractFiles = readContractFiles(sourceSha);
  const baseContractFiles = readContractFiles(baseSourceSha);
  const currentOpenapi = currentContractFiles.find(({ path }) => path === OPENAPI_PATH).content;
  const baseOpenapi = baseContractFiles.find(({ path }) => path === OPENAPI_PATH).content;
  const supportedApiMajor = parseSupportedApiMajor(currentOpenapi);
  const baseSupportedApiMajor = parseSupportedApiMajor(baseOpenapi);
  const currentConformance = currentContractFiles.filter(({ path }) => path !== OPENAPI_PATH);
  const baseConformance = baseContractFiles.filter(({ path }) => path !== OPENAPI_PATH);
  const openapiChanged = !currentOpenapi.equals(baseOpenapi);
  const conformance = compareConformanceContracts(baseConformance, currentConformance);
  const changelog = openapiChanged
    ? runOasdiff(options['--oasdiff'], 'changelog', baseSourceSha, sourceSha)
    : [];
  const breaking = openapiChanged
    ? runOasdiff(options['--oasdiff'], 'breaking', baseSourceSha, sourceSha)
    : [];
  const classification = classifyContract({
    apiMajorChanged: supportedApiMajor !== baseSupportedApiMajor,
    openapiChanged,
    changelog,
    breaking,
    conformance,
  });
  const impact = deriveReleaseImpact({
    classification,
    releasedTrack: 'gdscript',
    changelog,
    breaking,
    conformance,
    changedPaths: listChangedPaths(baseSourceSha, sourceSha),
  });

  const typescriptPackage = readJson(sourceSha, 'packages/sdk-ts/package.json');
  if (
    typescriptPackage.name !== '@seorilabs/platform-sdk'
    || !/^\d+\.\d+\.\d+$/u.test(typescriptPackage.version ?? '')
  ) {
    throw new Error('TypeScript SDK package 이름 또는 버전이 올바르지 않습니다.');
  }
  const typescriptSourceVersion = /^export const SDK_VERSION = "([^"]+)";$/mu.exec(
    readText(sourceSha, 'packages/sdk-ts/src/version.ts'),
  )?.[1];
  if (typescriptSourceVersion !== typescriptPackage.version) {
    throw new Error(
      `TypeScript SDK 내부 버전 ${typescriptSourceVersion ?? '없음'} != package ${typescriptPackage.version}`,
    );
  }
  const gdscriptVersion = readText(sourceSha, 'sdk-gdscript/VERSION');
  if (!/^\d+\.\d+\.\d+$/u.test(gdscriptVersion)) {
    throw new Error(`GDScript SDK VERSION이 올바르지 않습니다: ${gdscriptVersion}`);
  }

  const typescriptArtifactPath = resolve(options['--typescript-artifact']);
  const typescriptArtifact = await readFile(typescriptArtifactPath);
  verifyTypescriptArtifactIntegrity(
    typescriptArtifact,
    options['--typescript-registry-integrity'],
  );
  assertTypescriptPackageArtifact(
    typescriptArtifact,
    typescriptPackage.name,
    typescriptPackage.version,
  );
  const typescriptArtifactName = `seorilabs-platform-sdk-${typescriptPackage.version}.tgz`;
  if (basename(typescriptArtifactPath) !== typescriptArtifactName) {
    throw new Error(
      `TypeScript artifact 파일명 ${basename(typescriptArtifactPath)} != ${typescriptArtifactName}`,
    );
  }
  const gdscriptRelease = createGdscriptRelease({
    version: gdscriptVersion,
    releaseTag,
    payloadFiles: readGdscriptPayload(sourceSha),
  });

  const manifest = {
    schemaVersion: 1,
    release: {
      tag: releaseTag,
      sourceSha,
      baseSourceSha,
    },
    sdk: {
      typescript: {
        package: typescriptPackage.name,
        version: typescriptPackage.version,
        registry: 'https://npm.pkg.github.com',
        artifact: {
          name: typescriptArtifactName,
          sha256: sha256(typescriptArtifact),
          size: typescriptArtifact.length,
        },
      },
      gdscript: {
        version: gdscriptVersion,
        source: gdscriptRelease.source,
        treeChecksum: gdscriptRelease.treeChecksum,
        artifact: {
          name: gdscriptRelease.artifactName,
          sha256: gdscriptRelease.artifactSha256,
          size: gdscriptRelease.archive.length,
        },
        checksumArtifact: {
          name: gdscriptRelease.checksumArtifactName,
          sha256: gdscriptRelease.checksumArtifactSha256,
          size: gdscriptRelease.checksumArtifact.length,
        },
      },
    },
    contract: {
      revision: computeContractRevision(currentContractFiles),
      baseRevision: computeContractRevision(baseContractFiles),
      classification,
      supportedApiMajor,
      affectedConsumers: impact.affectedConsumers,
      affectedTracks: impact.affectedTracks,
      affectedCapabilities: impact.affectedCapabilities,
    },
  };
  const manifestContent = Buffer.from(canonicalJson(manifest), 'utf8');
  const outputDirectory = resolve(options['--output-dir']);
  await mkdir(outputDirectory, { recursive: true });
  const outputs = [
    [typescriptArtifactName, typescriptArtifact],
    [gdscriptRelease.artifactName, gdscriptRelease.archive],
    [gdscriptRelease.checksumArtifactName, gdscriptRelease.checksumArtifact],
    ['platform-release.json', manifestContent],
  ];
  for (const [name, content] of outputs) {
    await writeFile(resolve(outputDirectory, name), content, { flag: 'wx' });
  }

  return {
    manifest,
    outputDirectory,
    outputs: outputs.map(([name]) => name),
  };
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const result = await buildPlatformRelease(options);
  const shownDirectory = relative(process.cwd(), result.outputDirectory) || '.';
  console.log(`Platform release 생성: ${result.manifest.release.tag}`);
  console.log(`계약 분류: ${result.manifest.contract.classification}`);
  console.log(`산출물: ${shownDirectory}/${result.outputs.join(`, ${shownDirectory}/`)}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
