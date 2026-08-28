#!/usr/bin/env node
import { mkdir, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { readTarGzEntries, sha256 } from './platform-release-lib.mjs';

export const OASDIFF_VERSION = '1.29.1';
export const OASDIFF_LINUX_ARM64_SHA256 = '8bc247f0280f62ca73599265db0d984e853d7df6e714dad6ead85afc7cfc5883';
export const OASDIFF_LINUX_ARM64_URL = [
  `https://github.com/Tufin/oasdiff/releases/download/v${OASDIFF_VERSION}`,
  `oasdiff_${OASDIFF_VERSION}_linux_arm64.tar.gz`,
].join('/');
export const OASDIFF_LINUX_AMD64_SHA256 = '541f7c66c933495fceef24eaf5c48aa66c19069f366f7bd0a60a6a4820c5e533';
export const OASDIFF_DARWIN_ALL_SHA256 = '759cc5703d9335c441ad84a7074c705486b2c493f79bcfdf251c7a9c788b1171';

function releaseAsset() {
  if (process.platform === 'linux' && process.arch === 'arm64') {
    return { name: `oasdiff_${OASDIFF_VERSION}_linux_arm64.tar.gz`, sha256: OASDIFF_LINUX_ARM64_SHA256 };
  }
  if (process.platform === 'linux' && process.arch === 'x64') {
    return { name: `oasdiff_${OASDIFF_VERSION}_linux_amd64.tar.gz`, sha256: OASDIFF_LINUX_AMD64_SHA256 };
  }
  if (process.platform === 'darwin' && ['arm64', 'x64'].includes(process.arch)) {
    return { name: `oasdiff_${OASDIFF_VERSION}_darwin_all.tar.gz`, sha256: OASDIFF_DARWIN_ALL_SHA256 };
  }
  throw new Error(`지원하지 않는 oasdiff 실행 환경입니다: ${process.platform}-${process.arch}`);
}

export async function installOasdiff(outputPath, fetchImpl = fetch) {
  const asset = releaseAsset();
  const url = `https://github.com/Tufin/oasdiff/releases/download/v${OASDIFF_VERSION}/${asset.name}`;
  const response = await fetchImpl(url);
  if (!response.ok) {
    throw new Error(`oasdiff 다운로드에 실패했습니다: ${response.status} ${response.statusText}`);
  }
  const archive = Buffer.from(await response.arrayBuffer());
  const actual = sha256(archive);
  if (actual !== asset.sha256) {
    throw new Error(`oasdiff archive digest가 다릅니다: ${actual}`);
  }
  const executable = readTarGzEntries(archive).get('oasdiff');
  if (!executable) {
    throw new Error('oasdiff archive에 실행 파일이 없습니다.');
  }
  await mkdir(dirname(outputPath), { recursive: true });
  await writeFile(outputPath, executable, { flag: 'wx', mode: 0o755 });
}

async function main() {
  const [output = ''] = process.argv.slice(2);
  if (!output || process.argv.length !== 3) {
    throw new Error('사용법: node scripts/install-oasdiff.mjs <output-path>');
  }
  const resolved = resolve(output);
  await installOasdiff(resolved);
  console.log(`oasdiff ${OASDIFF_VERSION} 설치: ${resolved}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
