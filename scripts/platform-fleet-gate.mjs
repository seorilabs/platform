import { createHash } from 'node:crypto';
import { lstat, readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { parseTrustedPlatformReleaseKeys } from './platform-fleet-approval.mjs';
import { reconcilePlatformFleet } from './platform-fleet-reconciler.mjs';

const SOURCE_SHA_PATTERN = /^[0-9a-f]{40}$/u;
const REPOSITORY_ID_PATTERN = /^\d+$/u;
const MAX_INPUT_BYTES = 1024 * 1024;

function isRecord(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function canonicalize(value) {
  if (Array.isArray(value)) {
    return value.map(canonicalize);
  }
  if (isRecord(value)) {
    return Object.fromEntries(
      Object.keys(value).sort().map((key) => [key, canonicalize(value[key])]),
    );
  }
  return value;
}

function digest(value) {
  return `sha256:${createHash('sha256').update(JSON.stringify(canonicalize(value))).digest('hex')}`;
}

function requiredString(value, label, pattern) {
  if (typeof value !== 'string' || value.length === 0 || (pattern && !pattern.test(value))) {
    throw new Error(`${label} 값이 올바르지 않습니다.`);
  }
  return value;
}

function exactKeys(value, expected, label) {
  if (!isRecord(value)) {
    throw new Error(`${label} 형식이 올바르지 않습니다.`);
  }
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  if (JSON.stringify(actual) !== JSON.stringify(wanted)) {
    throw new Error(`${label} 필드가 올바르지 않습니다.`);
  }
}

export function evaluatePlatformReleaseGate({
  approval,
  configRevision,
  existingWorkItems = [],
  expectedConsumer,
  manifestContent,
  now,
  observation,
  repositoryId,
  sourceSha,
  trustedPublicKeys,
}) {
  requiredString(repositoryId, 'repositoryId', REPOSITORY_ID_PATTERN);
  requiredString(sourceSha, 'sourceSha', SOURCE_SHA_PATTERN);
  requiredString(configRevision, 'configRevision');
  if (expectedConsumer?.repositoryId !== repositoryId || observation?.repositoryId !== repositoryId) {
    throw new Error('release gate repository identity가 snapshot과 일치하지 않습니다.');
  }
  if (observation.sourceSha !== sourceSha || observation.configRevision !== configRevision) {
    throw new Error('release gate source SHA 또는 ACTIVE config revision이 snapshot과 다릅니다.');
  }
  if (observation.sourceType !== 'backoffice') {
    throw new Error('release gate는 Backoffice observation readback만 허용합니다.');
  }

  const plan = reconcilePlatformFleet({
    approval,
    existingWorkItems,
    expectedConsumers: [expectedConsumer],
    manifestContent,
    now,
    observations: [observation],
    trustedPublicKeys,
  });
  const consumer = plan.consumers[0];
  if (!consumer || consumer.repositoryId !== repositoryId) {
    throw new Error('release gate consumer plan을 찾지 못했습니다.');
  }
  if (!['PASS', 'EXCEPTION'].includes(consumer.releaseGate.status)) {
    const reasons = consumer.releaseGate.reasons.join(', ') || 'UNKNOWN';
    throw new Error(`Platform release gate가 차단되었습니다: ${reasons}`);
  }

  const unsigned = {
    schemaVersion: 1,
    purpose: 'seorilabs-platform-release-gate-v1',
    status: consumer.releaseGate.status,
    evaluatedAt: now,
    repositoryId,
    repositoryFullName: consumer.repositoryFullName,
    sourceSha,
    configRevision,
    manifestDigest: plan.releaseApproval.manifestDigest,
    platformReleaseSourceSha: plan.releaseApproval.sourceSha,
    contractRevision: plan.releaseApproval.contractRevision,
    planDigest: plan.planDigest,
    observationSnapshotDigest: plan.observationSnapshotDigest,
    sdkBindings: consumer.trackStates.map((track) => ({
      track: track.track,
      version: track.approvedVersion,
      artifactSha256: track.approvedArtifactSha256,
      contractRevision: track.approvedContractRevision,
      treeChecksum: track.approvedTreeChecksum,
      source: track.approvedSource,
    })),
    exceptionId: consumer.releaseGate.exceptionId,
    exceptionExpiresAt: consumer.releaseGate.exceptionExpiresAt,
  };
  return Object.freeze({ ...unsigned, receiptDigest: digest(unsigned) });
}

async function readBoundedFile(path, label) {
  const absolute = resolve(path);
  const metadata = await lstat(absolute);
  if (!metadata.isFile() || metadata.isSymbolicLink() || metadata.size > MAX_INPUT_BYTES) {
    throw new Error(`${label}은 1MiB 이하 일반 파일이어야 합니다.`);
  }
  return readFile(absolute);
}

async function readJson(path, label) {
  const bytes = await readBoundedFile(path, label);
  try {
    return JSON.parse(bytes.toString('utf8'));
  } catch (error) {
    throw new Error(`${label} JSON을 해석하지 못했습니다.`, { cause: error });
  }
}

function parseArguments(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!key?.startsWith('--') || value === undefined || Object.hasOwn(options, key)) {
      throw new Error('Platform release gate 인자가 올바르지 않습니다.');
    }
    options[key] = value;
  }
  const required = [
    '--approval',
    '--config-revision',
    '--manifest',
    '--output',
    '--repository-id',
    '--snapshot',
    '--source-sha',
    '--trusted-keys',
  ];
  const allowed = new Set(required);
  if (
    Object.keys(options).some((key) => !allowed.has(key))
    || required.some((key) => !options[key])
  ) {
    throw new Error('Platform release gate 필수 인자가 없거나 알 수 없는 인자가 있습니다.');
  }
  return options;
}

function parseSnapshot(snapshot, repositoryId) {
  exactKeys(
    snapshot,
    ['existingWorkItems', 'expectedConsumers', 'now', 'observations', 'schemaVersion'],
    'Backoffice platform snapshot',
  );
  if (
    snapshot.schemaVersion !== 1
    || !Array.isArray(snapshot.expectedConsumers)
    || !Array.isArray(snapshot.observations)
    || !Array.isArray(snapshot.existingWorkItems)
  ) {
    throw new Error('Backoffice platform snapshot version 또는 배열이 올바르지 않습니다.');
  }
  const expected = snapshot.expectedConsumers.filter((item) => item.repositoryId === repositoryId);
  const observations = snapshot.observations.filter((item) => item.repositoryId === repositoryId);
  if (expected.length !== 1 || observations.length !== 1) {
    throw new Error('repository별 expected consumer와 observation은 정확히 하나여야 합니다.');
  }
  return {
    expectedConsumer: expected[0],
    observation: observations[0],
    existingWorkItems: snapshot.existingWorkItems.filter((item) => item.repositoryId === repositoryId),
    now: snapshot.now,
  };
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const repositoryId = options['--repository-id'];
  const [manifestContent, approval, trustedRegistry, snapshot] = await Promise.all([
    readBoundedFile(options['--manifest'], 'platform-release.json'),
    readJson(options['--approval'], 'Fleet approval'),
    readJson(options['--trusted-keys'], 'trusted key registry'),
    readJson(options['--snapshot'], 'Backoffice platform snapshot'),
  ]);
  const scoped = parseSnapshot(snapshot, repositoryId);
  const receipt = evaluatePlatformReleaseGate({
    ...scoped,
    approval,
    configRevision: options['--config-revision'],
    manifestContent,
    repositoryId,
    sourceSha: options['--source-sha'],
    trustedPublicKeys: parseTrustedPlatformReleaseKeys(trustedRegistry),
  });
  await writeFile(resolve(options['--output']), `${JSON.stringify(receipt, null, 2)}\n`, {
    encoding: 'utf8',
    flag: 'wx',
    mode: 0o600,
  });
  process.stdout.write(`Platform release gate ${receipt.status}: ${receipt.receiptDigest}\n`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
