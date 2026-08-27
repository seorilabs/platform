import { createHash } from 'node:crypto';
import { gzipSync, gunzipSync } from 'node:zlib';

const CONTRACT_HASH_DOMAIN = Buffer.from('seorilabs-platform-contract-v1\0', 'utf8');
const VENDORED_TREE_HASH_DOMAIN = Buffer.from('seorilabs-vendored-tree-v1\0', 'utf8');
const GITHUB_REPOSITORY = 'seorilabs/platform';

function asBuffer(value) {
  return Buffer.isBuffer(value) ? value : Buffer.from(value);
}

function compareUtf8(left, right) {
  return Buffer.compare(Buffer.from(left, 'utf8'), Buffer.from(right, 'utf8'));
}

function updateHashWithLength(hash, value) {
  const content = asBuffer(value);
  const length = Buffer.alloc(8);
  length.writeBigUInt64BE(BigInt(content.length));
  hash.update(length);
  hash.update(content);
}

export function sha256(value) {
  return createHash('sha256').update(asBuffer(value)).digest('hex');
}

function hashNamedFiles(files, domain) {
  const sorted = [...files].sort((left, right) => compareUtf8(left.path, right.path));
  const seen = new Set();
  const hash = createHash('sha256');
  hash.update(domain);

  for (const file of sorted) {
    if (!file.path || seen.has(file.path)) {
      throw new Error(`중복되거나 비어 있는 계약 파일 경로입니다: ${file.path}`);
    }
    seen.add(file.path);
    updateHashWithLength(hash, file.path);
    updateHashWithLength(hash, file.content);
  }
  return hash.digest('hex');
}

export function computeContractRevision(files) {
  if (files.length === 0) {
    throw new Error('계약 revision을 계산할 파일이 없습니다.');
  }
  return `sha256:${hashNamedFiles(files, CONTRACT_HASH_DOMAIN)}`;
}

export function computeVendoredTreeChecksum(files) {
  const included = files.filter((file) => file.path !== 'CHECKSUM');
  return hashNamedFiles(included, VENDORED_TREE_HASH_DOMAIN);
}

export function assertPlatformReleaseTag(tag, gdscriptVersion) {
  const match = /^v(\d+\.\d+\.\d+)$/.exec(tag);
  if (!match) {
    throw new Error(`Platform release tag 형식이 올바르지 않습니다: ${tag}`);
  }
  if (match[1] !== gdscriptVersion) {
    throw new Error(`release tag ${match[1]} != GDScript ${gdscriptVersion}`);
  }
}

export function parseSupportedApiMajor(openapi) {
  const lines = asBuffer(openapi).toString('utf8').split(/\r?\n/u);
  const infoIndex = lines.findIndex((line) => /^info:\s*(?:#.*)?$/u.test(line));
  if (infoIndex < 0) {
    throw new Error('OpenAPI info.version을 찾지 못했습니다.');
  }

  for (let index = infoIndex + 1; index < lines.length; index += 1) {
    const line = lines[index];
    if (/^\S/u.test(line) && !/^\s*(?:#.*)?$/u.test(line)) {
      break;
    }
    const match = /^\s+version:\s*["']?(\d+)(?:\.|["']|\s|$)/u.exec(line);
    if (match) {
      const major = Number.parseInt(match[1], 10);
      if (Number.isSafeInteger(major) && major > 0) {
        return major;
      }
    }
  }
  throw new Error('OpenAPI info.version의 API major를 해석하지 못했습니다.');
}

function isRecord(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function isJsonSubset(base, current) {
  if (Object.is(base, current)) {
    return true;
  }
  if (Array.isArray(base)) {
    if (!Array.isArray(current) || base.length > current.length) {
      return false;
    }
    const matchedBaseByCurrent = new Array(current.length).fill(-1);
    const assign = (baseIndex, visited) => {
      for (let currentIndex = 0; currentIndex < current.length; currentIndex += 1) {
        if (visited.has(currentIndex) || !isJsonSubset(base[baseIndex], current[currentIndex])) {
          continue;
        }
        visited.add(currentIndex);
        if (
          matchedBaseByCurrent[currentIndex] === -1
          || assign(matchedBaseByCurrent[currentIndex], visited)
        ) {
          matchedBaseByCurrent[currentIndex] = baseIndex;
          return true;
        }
      }
      return false;
    };
    return base.every((_item, baseIndex) => assign(baseIndex, new Set()));
  }
  if (isRecord(base)) {
    if (!isRecord(current)) {
      return false;
    }
    return Object.entries(base).every(([key, value]) => (
      Object.hasOwn(current, key) && isJsonSubset(value, current[key])
    ));
  }
  return false;
}

function parseConformanceJson(file) {
  try {
    return JSON.parse(asBuffer(file.content).toString('utf8'));
  } catch (error) {
    throw new Error(`conformance JSON을 해석하지 못했습니다: ${file.path}`, { cause: error });
  }
}

export function compareConformanceContracts(baseFiles, currentFiles) {
  for (const file of [...baseFiles, ...currentFiles]) {
    parseConformanceJson(file);
  }
  const base = new Map(baseFiles.map((file) => [file.path, file]));
  const current = new Map(currentFiles.map((file) => [file.path, file]));
  if (base.size !== baseFiles.length || current.size !== currentFiles.length) {
    throw new Error('conformance 파일 경로가 중복되었습니다.');
  }
  const changedFiles = [];
  const additiveFiles = [];
  const breakingFiles = [];
  const paths = [...new Set([...base.keys(), ...current.keys()])].sort(compareUtf8);

  for (const path of paths) {
    const before = base.get(path);
    const after = current.get(path);
    if (!before) {
      parseConformanceJson(after);
      changedFiles.push(path);
      additiveFiles.push(path);
      continue;
    }
    if (!after) {
      parseConformanceJson(before);
      changedFiles.push(path);
      breakingFiles.push(path);
      continue;
    }
    if (asBuffer(before.content).equals(asBuffer(after.content))) {
      continue;
    }

    const beforeJson = parseConformanceJson(before);
    const afterJson = parseConformanceJson(after);
    const preservesBefore = isJsonSubset(beforeJson, afterJson);
    if (preservesBefore && isJsonSubset(afterJson, beforeJson)) {
      continue;
    }
    changedFiles.push(path);
    if (preservesBefore) {
      additiveFiles.push(path);
    } else {
      breakingFiles.push(path);
    }
  }

  return { changedFiles, additiveFiles, breakingFiles };
}

export function parseOasdiffJson(output, label) {
  let parsed;
  try {
    parsed = JSON.parse(asBuffer(output).toString('utf8'));
  } catch (error) {
    throw new Error(`oasdiff ${label} 결과가 JSON이 아닙니다.`, { cause: error });
  }
  if (!Array.isArray(parsed) || parsed.some((entry) => !isRecord(entry) || typeof entry.id !== 'string')) {
    throw new Error(`oasdiff ${label} 결과 형식이 올바르지 않습니다.`);
  }
  return parsed;
}

export function classifyContract({
  apiMajorChanged,
  openapiChanged,
  changelog,
  breaking,
  conformance,
}) {
  if (
    typeof apiMajorChanged !== 'boolean'
    || typeof openapiChanged !== 'boolean'
    || !Array.isArray(changelog)
    || !Array.isArray(breaking)
  ) {
    throw new Error('OpenAPI 변경 분류 입력이 올바르지 않습니다.');
  }
  if (!conformance || !Array.isArray(conformance.additiveFiles) || !Array.isArray(conformance.breakingFiles)) {
    throw new Error('conformance 변경 분류 입력이 올바르지 않습니다.');
  }
  if (!openapiChanged && (apiMajorChanged || changelog.length > 0 || breaking.length > 0)) {
    throw new Error('OpenAPI 원문과 변경 분류 결과가 일치하지 않습니다.');
  }
  if (apiMajorChanged || breaking.length > 0 || conformance.breakingFiles.length > 0) {
    return 'contract-breaking';
  }
  if (changelog.length > 0 || conformance.additiveFiles.length > 0) {
    return 'contract-additive';
  }
  return 'implementation-only';
}

function capabilityFromApiPath(path) {
  if (typeof path !== 'string') {
    return undefined;
  }
  const segment = path.split('/').filter(Boolean).find((part) => !/^v\d+$/u.test(part));
  return segment && /^[a-z][a-z0-9_-]*$/u.test(segment) ? segment : undefined;
}

function collectApiPaths(value, collected = new Set()) {
  if (typeof value === 'string') {
    if (value.startsWith('/')) {
      collected.add(value);
    }
    return collected;
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      collectApiPaths(item, collected);
    }
    return collected;
  }
  if (isRecord(value)) {
    for (const item of Object.values(value)) {
      collectApiPaths(item, collected);
    }
  }
  return collected;
}

function capabilityFromConformancePath(path) {
  const name = path.split('/').at(-1)?.replace(/\.json$/u, '') ?? '';
  if (name === 'param-normalization') {
    return 'events';
  }
  if (name === 'backoff' || name === 'envelope') {
    return 'transport';
  }
  return 'core';
}

function capabilitiesFromImplementationPath(path) {
  const lower = path.toLowerCase();
  const capabilities = [];
  const mappings = [
    ['presence', 'presence'],
    ['reward', 'ads'],
    ['admob', 'ads'],
    ['iap', 'iap'],
    ['purchase', 'iap'],
    ['entitlement', 'iap'],
    ['event', 'events'],
    ['param_normal', 'events'],
    ['config', 'config'],
    ['content', 'content'],
    ['identity', 'auth'],
    ['session', 'auth'],
    ['auth', 'auth'],
    ['backoff', 'transport'],
    ['envelope', 'transport'],
    ['transport', 'transport'],
  ];
  for (const [needle, capability] of mappings) {
    if (lower.includes(needle)) {
      capabilities.push(capability);
    }
  }
  return capabilities.length > 0 ? capabilities : ['core'];
}

export function deriveReleaseImpact({
  classification,
  releasedTrack,
  changelog,
  breaking,
  conformance,
  changedPaths,
}) {
  if (!['gdscript', 'typescript'].includes(releasedTrack)) {
    throw new Error(`알 수 없는 release track입니다: ${releasedTrack}`);
  }
  const tracks = new Set([releasedTrack]);
  const capabilities = new Set();

  if (classification === 'contract-additive' || classification === 'contract-breaking') {
    tracks.add('typescript');
    tracks.add('gdscript');
    for (const change of [...changelog, ...breaking]) {
      const paths = collectApiPaths(change);
      for (const path of paths) {
        const capability = capabilityFromApiPath(path);
        if (capability) {
          capabilities.add(capability);
        }
      }
    }
    for (const path of conformance.changedFiles) {
      capabilities.add(capabilityFromConformancePath(path));
    }
    if (capabilities.size === 0) {
      capabilities.add('core');
    }
  } else if (classification === 'implementation-only') {
    for (const path of changedPaths) {
      if (path.startsWith('packages/sdk-ts/')) {
        tracks.add('typescript');
        for (const capability of capabilitiesFromImplementationPath(path)) {
          capabilities.add(capability);
        }
      }
      if (path.startsWith('sdk-gdscript/')) {
        tracks.add('gdscript');
        for (const capability of capabilitiesFromImplementationPath(path)) {
          capabilities.add(capability);
        }
      }
    }
  } else {
    throw new Error(`알 수 없는 계약 분류입니다: ${classification}`);
  }

  if (capabilities.size === 0) {
    capabilities.add('core');
  }

  return {
    affectedTracks: [...tracks].sort(compareUtf8),
    affectedCapabilities: [...capabilities].sort(compareUtf8),
  };
}

function writeTarString(header, offset, length, value) {
  const content = Buffer.from(value, 'utf8');
  if (content.length > length) {
    throw new Error(`tar 경로가 너무 깁니다: ${value}`);
  }
  content.copy(header, offset);
}

function writeTarOctal(header, offset, length, value) {
  const encoded = value.toString(8).padStart(length - 1, '0');
  if (encoded.length > length - 1) {
    throw new Error(`tar 숫자 필드가 너무 큽니다: ${value}`);
  }
  writeTarString(header, offset, length, `${encoded}\0`);
}

function tarHeader(path, size, type) {
  const header = Buffer.alloc(512);
  writeTarString(header, 0, 100, path);
  writeTarOctal(header, 100, 8, type === '5' ? 0o755 : 0o644);
  writeTarOctal(header, 108, 8, 0);
  writeTarOctal(header, 116, 8, 0);
  writeTarOctal(header, 124, 12, size);
  writeTarOctal(header, 136, 12, 0);
  header.fill(0x20, 148, 156);
  writeTarString(header, 156, 1, type);
  writeTarString(header, 257, 6, 'ustar\0');
  writeTarString(header, 263, 2, '00');
  writeTarString(header, 265, 32, 'root');
  writeTarString(header, 297, 32, 'root');
  const checksum = header.reduce((sum, byte) => sum + byte, 0);
  const encoded = checksum.toString(8).padStart(6, '0');
  writeTarString(header, 148, 8, `${encoded}\0 `);
  return header;
}

export function createDeterministicTarGz(files, rootDirectory) {
  if (!/^[A-Za-z0-9._-]+$/u.test(rootDirectory)) {
    throw new Error(`tar 최상위 디렉터리가 올바르지 않습니다: ${rootDirectory}`);
  }
  const normalized = files.map((file) => {
    const segments = file.path?.split('/') ?? [];
    if (
      !file.path
      || file.path.startsWith('/')
      || file.path.includes('\\')
      || segments.some((segment) => segment === '' || segment === '.' || segment === '..')
    ) {
      throw new Error(`tar 파일 경로가 올바르지 않습니다: ${file.path}`);
    }
    return { path: file.path, content: asBuffer(file.content) };
  });
  const seen = new Set();
  for (const file of normalized) {
    if (seen.has(file.path)) {
      throw new Error(`tar 파일 경로가 중복됩니다: ${file.path}`);
    }
    seen.add(file.path);
  }

  const directories = new Set([`${rootDirectory}/`]);
  for (const file of normalized) {
    const parts = file.path.split('/');
    let current = `${rootDirectory}/`;
    for (const part of parts.slice(0, -1)) {
      current += `${part}/`;
      directories.add(current);
    }
  }
  const entries = [
    ...[...directories].map((path) => ({ path, content: Buffer.alloc(0), type: '5' })),
    ...normalized.map((file) => ({
      path: `${rootDirectory}/${file.path}`,
      content: file.content,
      type: '0',
    })),
  ].sort((left, right) => compareUtf8(left.path, right.path));

  const chunks = [];
  for (const entry of entries) {
    chunks.push(tarHeader(entry.path, entry.content.length, entry.type));
    if (entry.type === '0') {
      chunks.push(entry.content);
      const padding = (512 - (entry.content.length % 512)) % 512;
      if (padding > 0) {
        chunks.push(Buffer.alloc(padding));
      }
    }
  }
  chunks.push(Buffer.alloc(1024));
  const compressed = gzipSync(Buffer.concat(chunks), { level: 9, mtime: 0 });
  compressed[9] = 0xff;
  return compressed;
}

export function readTarGzEntries(archive) {
  const tar = gunzipSync(asBuffer(archive));
  const entries = new Map();
  for (let offset = 0; offset + 512 <= tar.length;) {
    const header = tar.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) {
      break;
    }
    const path = header.subarray(0, 100).toString('utf8').replace(/\0.*$/su, '');
    const sizeText = header.subarray(124, 136).toString('ascii').replace(/\0.*$/su, '').trim();
    const size = Number.parseInt(sizeText || '0', 8);
    const type = String.fromCharCode(header[156] || 0x30);
    if (!Number.isSafeInteger(size) || size < 0) {
      throw new Error(`tar 파일 크기를 해석하지 못했습니다: ${path}`);
    }
    const contentOffset = offset + 512;
    if (contentOffset + size > tar.length) {
      throw new Error(`tar 파일이 잘렸습니다: ${path}`);
    }
    if (type === '0' || type === '\0') {
      entries.set(path, Buffer.from(tar.subarray(contentOffset, contentOffset + size)));
    }
    offset = contentOffset + Math.ceil(size / 512) * 512;
  }
  return entries;
}

export function assertTypescriptPackageArtifact(archive, packageName, version) {
  const entries = readTarGzEntries(archive);
  const packageJson = entries.get('package/package.json');
  if (!packageJson) {
    throw new Error('TypeScript npm artifact에 package/package.json이 없습니다.');
  }
  let manifest;
  try {
    manifest = JSON.parse(packageJson.toString('utf8'));
  } catch (error) {
    throw new Error('TypeScript npm artifact의 package.json이 올바르지 않습니다.', { cause: error });
  }
  if (manifest.name !== packageName || manifest.version !== version) {
    throw new Error(
      `TypeScript npm artifact 식별자가 다릅니다: ${manifest.name}@${manifest.version}`,
    );
  }
}

export function createGdscriptRelease({ version, releaseTag, payloadFiles }) {
  assertPlatformReleaseTag(releaseTag, version);
  const platformClient = payloadFiles.find(({ path }) => path === 'platform_client.gd');
  const internalVersion = platformClient
    ? /^const SDK_VERSION := "([^"]+)"$/mu.exec(asBuffer(platformClient.content).toString('utf8'))?.[1]
    : undefined;
  if (internalVersion !== version) {
    throw new Error(`GDScript SDK 내부 버전 ${internalVersion ?? '없음'} != VERSION ${version}`);
  }
  const artifactName = `seorilabs-platform-gdscript-${version}.tar.gz`;
  const source = `https://github.com/${GITHUB_REPOSITORY}/releases/download/${releaseTag}/${artifactName}`;
  const releaseFiles = payloadFiles
    .filter((file) => file.path !== 'SOURCE' && file.path !== 'VERSION' && file.path !== 'CHECKSUM')
    .map((file) => ({ path: file.path, content: asBuffer(file.content) }));
  releaseFiles.push({ path: 'SOURCE', content: Buffer.from(`${source}\n`, 'utf8') });
  releaseFiles.push({ path: 'VERSION', content: Buffer.from(`${version}\n`, 'utf8') });
  const treeChecksum = computeVendoredTreeChecksum(releaseFiles);
  releaseFiles.push({ path: 'CHECKSUM', content: Buffer.from(`${treeChecksum}\n`, 'utf8') });

  const archive = createDeterministicTarGz(releaseFiles, 'seorilabs_platform');
  const artifactSha256 = sha256(archive);
  const checksumArtifactName = `${artifactName}.sha256`;
  const checksumArtifact = Buffer.from(`${artifactSha256}  ${artifactName}\n`, 'utf8');
  return {
    artifactName,
    archive,
    artifactSha256,
    checksumArtifactName,
    checksumArtifact,
    checksumArtifactSha256: sha256(checksumArtifact),
    releaseFiles,
    source,
    treeChecksum,
  };
}

export function canonicalJson(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}
