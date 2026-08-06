import { readFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');

export function assertSdkReleaseVersions(tag, packageVersion, sdkVersion) {
  const match = /^sdk-ts-v(\d+\.\d+\.\d+)$/.exec(tag);
  if (!match) {
    throw new Error(`SDK tag 형식이 올바르지 않습니다: ${tag}`);
  }

  const tagVersion = match[1];
  if (tagVersion !== packageVersion) {
    throw new Error(`tag ${tagVersion} != package ${packageVersion}`);
  }
  if (sdkVersion !== packageVersion) {
    throw new Error(`SDK ${sdkVersion} != package ${packageVersion}`);
  }
}

async function main() {
  const tag = process.argv[2] ?? '';
  const packageJson = JSON.parse(
    await readFile(resolve(root, 'packages/sdk-ts/package.json'), 'utf8'),
  );
  const { SDK_VERSION } = await import(
    pathToFileURL(resolve(root, 'packages/sdk-ts/dist/version.js')).href
  );

  assertSdkReleaseVersions(tag, packageJson.version, SDK_VERSION);
  console.log(`SDK 발행 버전 확인: ${packageJson.version}`);
}

const entry = process.argv[1] ? pathToFileURL(resolve(process.argv[1])).href : '';
if (entry === import.meta.url) {
  await main();
}
