import { createHash, verify as verifySignature } from 'node:crypto';

const APPROVAL_PURPOSE = 'seorilabs-platform-fleet-approved-release-v2';
const APPROVAL_REPOSITORY = 'seorilabs/platform';
const WORKFLOW_BUNDLE_REPOSITORY = 'seorilabs/.github';
const CANARY_PROFILES = ['godot', 'react-native'];
const TRACKS = ['gdscript', 'typescript'];
const CONTRACT_CLASSIFICATIONS = [
  'implementation-only',
  'contract-additive',
  'contract-breaking',
];
const CONTRACT_LABELS = ['P1', 'autopilot', 'platform', 'platform-contract'];
const SHA256_PATTERN = /^[0-9a-f]{64}$/u;
const SHA256_REVISION_PATTERN = /^sha256:[0-9a-f]{64}$/u;
const SOURCE_SHA_PATTERN = /^[0-9a-f]{40}$/u;
const SEMVER_PATTERN = /^\d+\.\d+\.\d+$/u;
const REPOSITORY_PATTERN = /^seorilabs\/[a-z0-9][a-z0-9._-]*$/u;
const SAFE_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$/u;

function isRecord(value) {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function assertRecord(value, label) {
  if (!isRecord(value)) {
    throw new Error(`${label} 형식이 올바르지 않습니다.`);
  }
  return value;
}

function assertExactKeys(value, expected, label) {
  const keys = Object.keys(assertRecord(value, label)).sort();
  const wanted = [...expected].sort();
  if (JSON.stringify(keys) !== JSON.stringify(wanted)) {
    throw new Error(`${label} 필드가 올바르지 않습니다: ${keys.join(', ')}`);
  }
}

function requiredString(value, label, pattern) {
  if (typeof value !== 'string' || value.length === 0 || (pattern && !pattern.test(value))) {
    throw new Error(`${label} 값이 올바르지 않습니다.`);
  }
  return value;
}

function requiredInteger(value, label, minimum = 0) {
  if (!Number.isSafeInteger(value) || value < minimum) {
    throw new Error(`${label} 값이 올바르지 않습니다.`);
  }
  return value;
}

function canonicalize(value) {
  if (Array.isArray(value)) {
    return value.map(canonicalize);
  }
  if (isRecord(value)) {
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, canonicalize(value[key])]),
    );
  }
  return value;
}

function canonicalBytes(value) {
  return Buffer.from(JSON.stringify(canonicalize(value)), 'utf8');
}

function compareCanonical(left, right) {
  return Buffer.compare(canonicalBytes(left), canonicalBytes(right));
}

function compareUtf8(left, right) {
  return Buffer.compare(Buffer.from(left, 'utf8'), Buffer.from(right, 'utf8'));
}

function sha256(value) {
  return createHash('sha256').update(value).digest('hex');
}

function digestOf(value) {
  return `sha256:${sha256(canonicalBytes(value))}`;
}

function immutableClone(value) {
  const clone = structuredClone(value);
  const freeze = (entry) => {
    if (entry !== null && typeof entry === 'object' && !Object.isFrozen(entry)) {
      Object.freeze(entry);
      for (const child of Object.values(entry)) {
        freeze(child);
      }
    }
    return entry;
  };
  return freeze(clone);
}

function assertUniqueSortedStrings(values, allowed, label) {
  if (!Array.isArray(values) || values.length === 0) {
    throw new Error(`${label}은 하나 이상이어야 합니다.`);
  }
  const sorted = [...values].sort();
  let hasInvalidValue = false;
  for (const value of values) {
    if (typeof value !== 'string' || (allowed && !allowed.includes(value))) {
      hasInvalidValue = true;
    }
  }
  if (
    hasInvalidValue
    || new Set(values).size !== values.length
    || JSON.stringify(values) !== JSON.stringify(sorted)
  ) {
    throw new Error(`${label}은 중복 없는 정렬된 허용 값이어야 합니다.`);
  }
  return values;
}

function assertArtifact(value, label) {
  assertExactKeys(value, ['name', 'sha256', 'size'], label);
  requiredString(value.name, `${label}.name`, /^[A-Za-z0-9._-]+$/u);
  requiredString(value.sha256, `${label}.sha256`, SHA256_PATTERN);
  requiredInteger(value.size, `${label}.size`, 1);
}

function validateManifest(manifest) {
  assertExactKeys(manifest, ['contract', 'release', 'schemaVersion', 'sdk'], 'manifest');
  if (manifest.schemaVersion !== 1) {
    throw new Error(`지원하지 않는 platform release schema입니다: ${manifest.schemaVersion}`);
  }

  assertExactKeys(manifest.release, ['baseSourceSha', 'sourceSha', 'tag'], 'manifest.release');
  requiredString(manifest.release.sourceSha, 'manifest.release.sourceSha', SOURCE_SHA_PATTERN);
  requiredString(manifest.release.baseSourceSha, 'manifest.release.baseSourceSha', SOURCE_SHA_PATTERN);
  requiredString(manifest.release.tag, 'manifest.release.tag', /^v\d+\.\d+\.\d+$/u);
  if (manifest.release.sourceSha === manifest.release.baseSourceSha) {
    throw new Error('manifest source SHA와 base SHA가 같습니다.');
  }

  assertExactKeys(manifest.sdk, ['gdscript', 'typescript'], 'manifest.sdk');
  const typescript = manifest.sdk.typescript;
  assertExactKeys(
    typescript,
    ['artifact', 'package', 'registry', 'version'],
    'manifest.sdk.typescript',
  );
  if (
    typescript.package !== '@seorilabs/platform-sdk'
    || typescript.registry !== 'https://npm.pkg.github.com'
  ) {
    throw new Error('TypeScript SDK package 또는 registry가 허용된 값과 다릅니다.');
  }
  requiredString(typescript.version, 'manifest.sdk.typescript.version', SEMVER_PATTERN);
  assertArtifact(typescript.artifact, 'manifest.sdk.typescript.artifact');
  if (typescript.artifact.name !== `seorilabs-platform-sdk-${typescript.version}.tgz`) {
    throw new Error('TypeScript SDK artifact 이름이 package version과 일치하지 않습니다.');
  }

  const gdscript = manifest.sdk.gdscript;
  assertExactKeys(
    gdscript,
    ['artifact', 'checksumArtifact', 'source', 'treeChecksum', 'version'],
    'manifest.sdk.gdscript',
  );
  requiredString(gdscript.version, 'manifest.sdk.gdscript.version', SEMVER_PATTERN);
  requiredString(gdscript.treeChecksum, 'manifest.sdk.gdscript.treeChecksum', SHA256_PATTERN);
  assertArtifact(gdscript.artifact, 'manifest.sdk.gdscript.artifact');
  assertArtifact(gdscript.checksumArtifact, 'manifest.sdk.gdscript.checksumArtifact');
  const expectedTag = `v${gdscript.version}`;
  const expectedArtifactName = `seorilabs-platform-gdscript-${gdscript.version}.tar.gz`;
  const expectedSource = `https://github.com/${APPROVAL_REPOSITORY}/releases/download/${expectedTag}/${expectedArtifactName}`;
  if (
    manifest.release.tag !== expectedTag
    || gdscript.artifact.name !== expectedArtifactName
    || gdscript.checksumArtifact.name !== `${expectedArtifactName}.sha256`
    || gdscript.source !== expectedSource
  ) {
    throw new Error('GDScript release tag, asset 또는 고정 source URL이 일치하지 않습니다.');
  }

  const contract = manifest.contract;
  assertExactKeys(
    contract,
    [
      'affectedCapabilities',
      'affectedTracks',
      'baseRevision',
      'classification',
      'revision',
      'supportedApiMajor',
    ],
    'manifest.contract',
  );
  requiredString(contract.revision, 'manifest.contract.revision', SHA256_REVISION_PATTERN);
  requiredString(contract.baseRevision, 'manifest.contract.baseRevision', SHA256_REVISION_PATTERN);
  if (!CONTRACT_CLASSIFICATIONS.includes(contract.classification)) {
    throw new Error(`알 수 없는 계약 분류입니다: ${contract.classification}`);
  }
  requiredInteger(contract.supportedApiMajor, 'manifest.contract.supportedApiMajor', 1);
  assertUniqueSortedStrings(contract.affectedTracks, TRACKS, 'manifest.contract.affectedTracks');
  assertUniqueSortedStrings(contract.affectedCapabilities, undefined, 'manifest.contract.affectedCapabilities');
  if (contract.affectedCapabilities.some((value) => !/^[a-z][a-z0-9_-]*$/u.test(value))) {
    throw new Error('manifest.contract.affectedCapabilities 값이 올바르지 않습니다.');
  }
  if (
    contract.classification !== 'implementation-only'
    && JSON.stringify(contract.affectedTracks) !== JSON.stringify(TRACKS)
  ) {
    throw new Error('계약 변경 release는 두 SDK track을 모두 포함해야 합니다.');
  }
  return manifest;
}

function parseManifestContent(manifestContent) {
  if (!(typeof manifestContent === 'string' || Buffer.isBuffer(manifestContent))) {
    throw new Error('manifestContent는 발행된 원문 byte여야 합니다.');
  }
  const bytes = Buffer.isBuffer(manifestContent)
    ? Buffer.from(manifestContent)
    : Buffer.from(manifestContent, 'utf8');
  let manifest;
  try {
    manifest = JSON.parse(bytes.toString('utf8'));
  } catch (error) {
    throw new Error('platform-release.json을 해석하지 못했습니다.', { cause: error });
  }
  return { bytes, manifest: validateManifest(manifest) };
}

export function platformReleaseIdentity(manifestContent) {
  const { bytes, manifest } = parseManifestContent(manifestContent);
  return immutableClone({
    repository: APPROVAL_REPOSITORY,
    manifestSha256: `sha256:${sha256(bytes)}`,
    sourceSha: manifest.release.sourceSha,
    releaseTag: manifest.release.tag,
  });
}

function validateCanaryArtifact(value, label) {
  assertExactKeys(value, ['name', 'sha256', 'size'], label);
  requiredString(value.name, `${label}.name`, /^[A-Za-z0-9._-]+\.aab$/u);
  requiredString(value.sha256, `${label}.sha256`, SHA256_REVISION_PATTERN);
  requiredInteger(value.size, `${label}.size`, 1);
}

function validateCanaryRun(value, label, { buildOnly, sourceSha, workflowSourceSha }) {
  assertExactKeys(
    value,
    buildOnly
      ? [
          'artifact',
          'buildConfigDigest',
          'builderImageDigest',
          'cloudBuildId',
          'conclusion',
          'headSha',
          'runId',
          'workflowSourceSha',
        ]
      : ['conclusion', 'headSha', 'runId', 'workflowSourceSha'],
    label,
  );
  requiredString(value.runId, `${label}.runId`, /^\d+$/u);
  if (value.conclusion !== 'success') {
    throw new Error(`${label}.conclusion은 success여야 합니다.`);
  }
  requiredString(value.headSha, `${label}.headSha`, SOURCE_SHA_PATTERN);
  requiredString(value.workflowSourceSha, `${label}.workflowSourceSha`, SOURCE_SHA_PATTERN);
  if (value.headSha !== sourceSha || value.workflowSourceSha !== workflowSourceSha) {
    throw new Error(`${label}의 source SHA가 canary 또는 WorkflowBundle과 다릅니다.`);
  }
  if (buildOnly) {
    requiredString(value.cloudBuildId, `${label}.cloudBuildId`, SAFE_ID_PATTERN);
    requiredString(value.builderImageDigest, `${label}.builderImageDigest`, SHA256_REVISION_PATTERN);
    requiredString(value.buildConfigDigest, `${label}.buildConfigDigest`, SHA256_REVISION_PATTERN);
    validateCanaryArtifact(value.artifact, `${label}.artifact`);
  }
}

function validateCanaryApprovalEvidence(value) {
  assertExactKeys(
    value,
    ['attestationSha256', 'canaries', 'readbackKeyId', 'workflowBundle'],
    'release approval canary evidence',
  );
  requiredString(
    value.attestationSha256,
    'release approval canary evidence.attestationSha256',
    SHA256_REVISION_PATTERN,
  );
  requiredString(
    value.readbackKeyId,
    'release approval canary evidence.readbackKeyId',
    SAFE_ID_PATTERN,
  );
  assertExactKeys(
    value.workflowBundle,
    ['digest', 'repository', 'sourceSha'],
    'release approval canary evidence.workflowBundle',
  );
  if (value.workflowBundle.repository !== WORKFLOW_BUNDLE_REPOSITORY) {
    throw new Error('canary evidence WorkflowBundle repository가 올바르지 않습니다.');
  }
  requiredString(
    value.workflowBundle.sourceSha,
    'release approval canary evidence.workflowBundle.sourceSha',
    SOURCE_SHA_PATTERN,
  );
  requiredString(
    value.workflowBundle.digest,
    'release approval canary evidence.workflowBundle.digest',
    SHA256_REVISION_PATTERN,
  );
  if (!Array.isArray(value.canaries) || value.canaries.length !== CANARY_PROFILES.length) {
    throw new Error('release approval에는 RN과 Godot canary가 정확히 하나씩 필요합니다.');
  }
  const repositoryIds = new Set();
  const repositoryNames = new Set();
  const profiles = [];
  for (const [index, canary] of value.canaries.entries()) {
    const label = `release approval canary evidence.canaries[${index}]`;
    assertExactKeys(
      canary,
      [
        'buildOnlyRun',
        'profile',
        'repositoryFullName',
        'repositoryId',
        'sourceSha',
        'staticRun',
      ],
      label,
    );
    if (!CANARY_PROFILES.includes(canary.profile)) {
      throw new Error(`${label}.profile 값이 올바르지 않습니다.`);
    }
    profiles.push(canary.profile);
    requiredString(canary.repositoryId, `${label}.repositoryId`, /^\d+$/u);
    requiredString(
      canary.repositoryFullName,
      `${label}.repositoryFullName`,
      REPOSITORY_PATTERN,
    );
    requiredString(canary.sourceSha, `${label}.sourceSha`, SOURCE_SHA_PATTERN);
    if (repositoryIds.has(canary.repositoryId) || repositoryNames.has(canary.repositoryFullName)) {
      throw new Error('RN과 Godot canary repository는 서로 달라야 합니다.');
    }
    repositoryIds.add(canary.repositoryId);
    repositoryNames.add(canary.repositoryFullName);
    validateCanaryRun(canary.staticRun, `${label}.staticRun`, {
      buildOnly: false,
      sourceSha: canary.sourceSha,
      workflowSourceSha: value.workflowBundle.sourceSha,
    });
    validateCanaryRun(canary.buildOnlyRun, `${label}.buildOnlyRun`, {
      buildOnly: true,
      sourceSha: canary.sourceSha,
      workflowSourceSha: value.workflowBundle.sourceSha,
    });
    if (canary.staticRun.runId === canary.buildOnlyRun.runId) {
      throw new Error(`${label}의 static과 build-only run ID가 같습니다.`);
    }
  }
  if (JSON.stringify(profiles) !== JSON.stringify(CANARY_PROFILES)) {
    throw new Error('release approval canary는 godot, react-native 순서로 고정해야 합니다.');
  }
  return value;
}

export function platformReleaseApprovalPayload(manifestContent, canaryEvidence) {
  const identity = platformReleaseIdentity(manifestContent);
  const evidence = validateCanaryApprovalEvidence(structuredClone(canaryEvidence));
  return immutableClone({
    purpose: APPROVAL_PURPOSE,
    ...identity,
    status: 'fleet-approved',
    canaryEvidence: evidence,
  });
}

// Signer와 verifier가 같은 byte 표현을 쓰도록 승인 payload의 canonical byte를
// producer가 아닌 계약 모듈에서 한 번만 정의한다. 반환값에는 secret이 없다.
export function platformReleaseApprovalBytes(manifestContent, canaryEvidence) {
  return Buffer.from(canonicalBytes(platformReleaseApprovalPayload(manifestContent, canaryEvidence)));
}

function trustedKey(trustedPublicKeys, keyId) {
  if (!(trustedPublicKeys instanceof Map)) {
    throw new Error('trustedPublicKeys는 명시적인 Map이어야 합니다.');
  }
  const key = trustedPublicKeys.get(keyId);
  if (!key) {
    throw new Error(`신뢰하지 않는 Fleet approval key입니다: ${keyId}`);
  }
  return key;
}

function verifyApproval(manifestContent, approval, trustedPublicKeys) {
  assertExactKeys(
    approval,
    ['algorithm', 'keyId', 'payload', 'schemaVersion', 'signature'],
    'release approval',
  );
  if (approval.schemaVersion !== 2 || approval.algorithm !== 'Ed25519') {
    throw new Error('지원하지 않는 Fleet approval 형식입니다.');
  }
  requiredString(approval.keyId, 'release approval keyId', SAFE_ID_PATTERN);
  requiredString(approval.signature, 'release approval signature', /^[A-Za-z0-9+/]+={0,2}$/u);
  assertExactKeys(
    approval.payload,
    [
      'canaryEvidence',
      'manifestSha256',
      'purpose',
      'releaseTag',
      'repository',
      'sourceSha',
      'status',
    ],
    'release approval payload',
  );
  const expectedPayload = platformReleaseApprovalPayload(
    manifestContent,
    approval.payload.canaryEvidence,
  );
  if (JSON.stringify(canonicalize(approval.payload)) !== JSON.stringify(canonicalize(expectedPayload))) {
    throw new Error('Fleet approval payload가 platform release와 일치하지 않습니다.');
  }
  let signature;
  try {
    signature = Buffer.from(approval.signature, 'base64');
  } catch (error) {
    throw new Error('Fleet approval signature를 해석하지 못했습니다.', { cause: error });
  }
  if (
    signature.length !== 64
    || !verifySignature(
      null,
      canonicalBytes(approval.payload),
      trustedKey(trustedPublicKeys, approval.keyId),
      signature,
    )
  ) {
    throw new Error('Fleet approval 서명이 올바르지 않습니다.');
  }
  return expectedPayload;
}

function assertTimestamp(value, label) {
  requiredString(value, label);
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp) || new Date(timestamp).toISOString() !== value) {
    throw new Error(`${label}은 UTC ISO-8601 형식이어야 합니다.`);
  }
  return timestamp;
}

function validateExpectedConsumer(value) {
  assertExactKeys(value, ['repositoryFullName', 'repositoryId', 'tracks'], 'expected consumer');
  requiredString(value.repositoryId, 'expected consumer repositoryId', /^\d+$/u);
  requiredString(value.repositoryFullName, 'expected consumer repositoryFullName', REPOSITORY_PATTERN);
  assertUniqueSortedStrings(value.tracks, TRACKS, 'expected consumer tracks');
  return value;
}

function validateSdkObservation(value, label) {
  assertRecord(value, label);
  if (value.track === 'typescript') {
    assertExactKeys(value, ['artifactSha256', 'contractRevision', 'track', 'version'], label);
  } else if (value.track === 'gdscript') {
    assertExactKeys(
      value,
      ['artifactSha256', 'contractRevision', 'source', 'track', 'treeChecksum', 'version'],
      label,
    );
    requiredString(value.source, `${label}.source`, /^https:\/\/github\.com\/seorilabs\/platform\/releases\/download\/v\d+\.\d+\.\d+\/seorilabs-platform-gdscript-\d+\.\d+\.\d+\.tar\.gz$/u);
    requiredString(value.treeChecksum, `${label}.treeChecksum`, SHA256_PATTERN);
  } else {
    throw new Error(`${label}.track 값이 올바르지 않습니다.`);
  }
  requiredString(value.version, `${label}.version`, SEMVER_PATTERN);
  requiredString(value.artifactSha256, `${label}.artifactSha256`, SHA256_PATTERN);
  requiredString(value.contractRevision, `${label}.contractRevision`, SHA256_REVISION_PATTERN);
  if (value.track === 'gdscript') {
    const expectedArtifact = `seorilabs-platform-gdscript-${value.version}.tar.gz`;
    const expectedSource = `https://github.com/${APPROVAL_REPOSITORY}/releases/download/v${value.version}/${expectedArtifact}`;
    if (value.source !== expectedSource) {
      throw new Error(`${label}.source가 observed version과 일치하지 않습니다.`);
    }
  }
  return value;
}

function validateException(value, label) {
  if (value === null) {
    return null;
  }
  assertExactKeys(
    value,
    ['capability', 'expiresAt', 'id', 'manifestSha256', 'status'],
    label,
  );
  requiredString(value.id, `${label}.id`, SAFE_ID_PATTERN);
  if (value.capability !== 'release-build' || value.status !== 'ACTIVE') {
    throw new Error(`${label} capability 또는 status가 올바르지 않습니다.`);
  }
  requiredString(value.manifestSha256, `${label}.manifestSha256`, SHA256_REVISION_PATTERN);
  assertTimestamp(value.expiresAt, `${label}.expiresAt`);
  return value;
}

function validateObservation(value, nowTimestamp) {
  assertExactKeys(
    value,
    [
      'configRevision',
      'configState',
      'evidence',
      'exception',
      'observationId',
      'observedAt',
      'repositoryFullName',
      'repositoryId',
      'snapshotDigest',
      'sourceSha',
      'sourceType',
    ],
    'consumer observation',
  );
  requiredString(value.observationId, 'consumer observation observationId', SAFE_ID_PATTERN);
  requiredString(value.repositoryId, 'consumer observation repositoryId', /^\d+$/u);
  requiredString(value.repositoryFullName, 'consumer observation repositoryFullName', REPOSITORY_PATTERN);
  requiredString(value.configRevision, 'consumer observation configRevision', SAFE_ID_PATTERN);
  if (value.configState !== 'ACTIVE') {
    throw new Error('consumer observation configState는 ACTIVE여야 합니다.');
  }
  requiredString(value.sourceSha, 'consumer observation sourceSha', SOURCE_SHA_PATTERN);
  requiredString(value.snapshotDigest, 'consumer observation snapshotDigest', SHA256_REVISION_PATTERN);
  if (!['backoffice', 'fixture'].includes(value.sourceType)) {
    throw new Error(`consumer observation sourceType이 올바르지 않습니다: ${value.sourceType}`);
  }
  const observedTimestamp = assertTimestamp(value.observedAt, 'consumer observation observedAt');
  if (observedTimestamp > nowTimestamp) {
    throw new Error('consumer observation observedAt이 평가 시각보다 미래입니다.');
  }
  assertExactKeys(
    value.evidence,
    ['customHttpTracks', 'officialSdks', 'unmanagedTracks'],
    'consumer observation evidence',
  );
  if (!Array.isArray(value.evidence.officialSdks)) {
    throw new Error('consumer observation officialSdks가 배열이 아닙니다.');
  }
  value.evidence.officialSdks.forEach((sdk, index) => (
    validateSdkObservation(sdk, `consumer observation officialSdks[${index}]`)
  ));
  for (const [name, tracks] of [
    ['customHttpTracks', value.evidence.customHttpTracks],
    ['unmanagedTracks', value.evidence.unmanagedTracks],
  ]) {
    if (!Array.isArray(tracks)) {
      throw new Error(`consumer observation ${name}이 배열이 아닙니다.`);
    }
    if (tracks.length > 0) {
      assertUniqueSortedStrings(tracks, TRACKS, `consumer observation ${name}`);
    }
  }
  validateException(value.exception, 'consumer observation exception');
  return value;
}

function validateExistingWorkItem(value) {
  assertExactKeys(
    value,
    ['actionDigest', 'concurrencyKey', 'idempotencyKey', 'kind', 'repositoryId', 'state'],
    'existing work item',
  );
  requiredString(value.idempotencyKey, 'existing work item idempotencyKey', /^platform-fleet\/v1\/[0-9a-f]{64}$/u);
  requiredString(value.actionDigest, 'existing work item actionDigest', SHA256_REVISION_PATTERN);
  requiredString(value.repositoryId, 'existing work item repositoryId', /^\d+$/u);
  requiredString(value.concurrencyKey, 'existing work item concurrencyKey', SAFE_ID_PATTERN);
  if (!['issue', 'pull-request'].includes(value.kind)) {
    throw new Error('existing work item kind가 올바르지 않습니다.');
  }
  if (!['OPEN', 'MERGED', 'CLOSED'].includes(value.state)) {
    throw new Error('existing work item state가 올바르지 않습니다.');
  }
  return value;
}

function classifyTrack(observation, track) {
  const official = observation.evidence.officialSdks.filter((sdk) => sdk.track === track);
  const custom = observation.evidence.customHttpTracks.includes(track);
  const unmanaged = observation.evidence.unmanagedTracks.includes(track);
  const evidenceKinds = Number(official.length > 0) + Number(custom) + Number(unmanaged);
  if (official.length > 1 || evidenceKinds > 1) {
    return { status: 'ambiguous', sdk: null };
  }
  if (official.length === 1) {
    return { status: 'managed', sdk: official[0] };
  }
  if (custom) {
    return { status: 'custom', sdk: null };
  }
  if (unmanaged) {
    return { status: 'unmanaged', sdk: null };
  }
  return { status: 'missing', sdk: null };
}

function desiredSdk(manifest, track) {
  if (track === 'typescript') {
    return {
      track,
      version: manifest.sdk.typescript.version,
      artifactSha256: manifest.sdk.typescript.artifact.sha256,
      contractRevision: manifest.contract.revision,
    };
  }
  return {
    track,
    version: manifest.sdk.gdscript.version,
    artifactSha256: manifest.sdk.gdscript.artifact.sha256,
    treeChecksum: manifest.sdk.gdscript.treeChecksum,
    source: manifest.sdk.gdscript.source,
    contractRevision: manifest.contract.revision,
  };
}

function sdkMatches(observed, desired) {
  return Object.entries(desired).every(([key, value]) => observed?.[key] === value);
}

function makeAction(intent) {
  const actionDigest = digestOf(intent);
  return immutableClone({
    ...intent,
    actionDigest,
    idempotencyKey: `platform-fleet/v1/${sha256(canonicalBytes(intent))}`,
  });
}

function makePullRequestAction({ consumer, manifest, manifestDigest, observation, desiredTracks }) {
  return makeAction({
    kind: 'pull-request',
    repositoryId: consumer.repositoryId,
    repositoryFullName: consumer.repositoryFullName,
    scope: 'platform-sdk-update',
    concurrencyKey: `platform-fleet/repository/${consumer.repositoryId}/autonomous-pr`,
    releaseSourceSha: manifest.release.sourceSha,
    consumerSourceSha: observation.sourceSha,
    consumerConfigRevision: observation.configRevision,
    consumerObservationDigest: observation.snapshotDigest,
    manifestDigest,
    contractRevision: manifest.contract.revision,
    exactUpdates: desiredTracks,
    mergePolicy: 'auto-after-full-ci',
    acceptanceCriteria: [
      '모든 exact SDK version과 digest가 계획값과 일치한다.',
      '저장소 전체 CI가 성공한다.',
      '승인 manifest에 대한 provider observation readback이 current다.',
    ],
    title: `Platform SDK ${manifest.release.tag} 탑재`,
  });
}

function makeContractIssueAction({ consumer, manifest, manifestDigest, observation, trackStates }) {
  return makeAction({
    kind: 'issue',
    repositoryId: consumer.repositoryId,
    repositoryFullName: consumer.repositoryFullName,
    scope: 'platform-contract-adaptation',
    concurrencyKey: `platform-fleet/repository/${consumer.repositoryId}/contract/${manifest.contract.revision}`,
    releaseSourceSha: manifest.release.sourceSha,
    consumerSourceSha: observation.sourceSha,
    consumerConfigRevision: observation.configRevision,
    consumerObservationDigest: observation.snapshotDigest,
    manifestDigest,
    contractRevision: manifest.contract.revision,
    contractClassification: manifest.contract.classification,
    affectedTracks: manifest.contract.affectedTracks,
    affectedCapabilities: manifest.contract.affectedCapabilities,
    observedTrackStates: trackStates,
    labels: CONTRACT_LABELS,
    priority: 'P1',
    acceptanceCriteria: [
      '영향 capability의 코드와 계약 테스트를 새 revision에 맞춘다.',
      'official SDK를 승인된 exact version과 digest로 탑재한다.',
      '기능 활성화, 업로드, 실기기 QA, 공개 rollout은 별도 gate로 남긴다.',
    ],
    title: `Platform 계약 ${manifest.contract.revision} 적응`,
  });
}

function activeException(exceptionValue, manifestDigest, nowTimestamp) {
  if (exceptionValue === null) {
    return { active: false, reason: 'not-configured' };
  }
  if (exceptionValue.manifestSha256 !== manifestDigest) {
    return { active: false, reason: 'manifest-mismatch' };
  }
  if (Date.parse(exceptionValue.expiresAt) <= nowTimestamp) {
    return { active: false, reason: 'expired' };
  }
  return { active: true, reason: null };
}

function existingWorkIndex(existingWorkItems) {
  const byKey = new Map();
  const openByConcurrency = new Map();
  for (const item of existingWorkItems) {
    validateExistingWorkItem(item);
    const existing = byKey.get(item.idempotencyKey);
    if (existing && JSON.stringify(canonicalize(existing)) !== JSON.stringify(canonicalize(item))) {
      throw new Error(`idempotency key가 충돌합니다: ${item.idempotencyKey}`);
    }
    byKey.set(item.idempotencyKey, item);
    if (item.state === 'OPEN') {
      const active = openByConcurrency.get(item.concurrencyKey);
      if (active && active.idempotencyKey !== item.idempotencyKey) {
        throw new Error(`동일 concurrency scope에 열린 work item이 둘 이상입니다: ${item.concurrencyKey}`);
      }
      openByConcurrency.set(item.concurrencyKey, item);
    }
  }
  return { byKey, openByConcurrency };
}

function normalizeInputs(expectedConsumers, observations, nowTimestamp) {
  if (!Array.isArray(expectedConsumers) || expectedConsumers.length === 0) {
    throw new Error('expectedConsumers는 하나 이상이어야 합니다.');
  }
  if (!Array.isArray(observations)) {
    throw new Error('observations가 배열이 아닙니다.');
  }
  const expectedById = new Map();
  const expectedByName = new Map();
  for (const consumer of expectedConsumers) {
    validateExpectedConsumer(consumer);
    if (expectedById.has(consumer.repositoryId) || expectedByName.has(consumer.repositoryFullName)) {
      throw new Error(`expected consumer가 중복되었습니다: ${consumer.repositoryFullName}`);
    }
    expectedById.set(consumer.repositoryId, consumer);
    expectedByName.set(consumer.repositoryFullName, consumer);
  }

  const observationsById = new Map();
  const observationIds = new Map();
  for (const observation of observations) {
    validateObservation(observation, nowTimestamp);
    const normalizedObservation = structuredClone(observation);
    normalizedObservation.evidence.officialSdks.sort(compareCanonical);
    const duplicate = observationIds.get(normalizedObservation.observationId);
    if (duplicate) {
      if (compareCanonical(duplicate, normalizedObservation) !== 0) {
        throw new Error(`observationId가 충돌합니다: ${normalizedObservation.observationId}`);
      }
      continue;
    }
    observationIds.set(normalizedObservation.observationId, normalizedObservation);
    const list = observationsById.get(normalizedObservation.repositoryId) ?? [];
    list.push(normalizedObservation);
    observationsById.set(normalizedObservation.repositoryId, list);
  }
  return {
    expected: [...expectedById.values()].sort((left, right) => (
      compareUtf8(left.repositoryId, right.repositoryId)
    )),
    expectedByName,
    observationsById,
    observations: [...observationIds.values()].sort((left, right) => (
      compareUtf8(left.observationId, right.observationId)
    )),
  };
}

export function reconcilePlatformFleet({
  approval,
  existingWorkItems = [],
  expectedConsumers,
  manifestContent,
  now,
  observations,
  trustedPublicKeys,
}) {
  const nowTimestamp = assertTimestamp(now, 'now');
  const { bytes, manifest } = parseManifestContent(manifestContent);
  const approvalPayload = verifyApproval(manifestContent, approval, trustedPublicKeys);
  const manifestDigest = `sha256:${sha256(bytes)}`;
  const normalized = normalizeInputs(expectedConsumers, observations, nowTimestamp);
  const workIndex = existingWorkIndex(existingWorkItems);
  const observationSnapshotDigest = digestOf({
    expectedConsumers: normalized.expected,
    observations: normalized.observations,
  });

  const actions = [];
  const deduplicated = [];
  const deferred = [];
  const consumers = [];
  let hasNeedsInput = false;

  for (const consumer of normalized.expected) {
    const candidates = normalized.observationsById.get(consumer.repositoryId) ?? [];
    let needsInput = [];
    let observation = null;
    if (candidates.length === 0) {
      needsInput.push('MISSING_CONSUMER_OBSERVATION');
    } else if (candidates.length > 1) {
      needsInput.push('AMBIGUOUS_CONSUMER_OBSERVATION');
    } else {
      [observation] = candidates;
      if (observation.repositoryFullName !== consumer.repositoryFullName) {
        needsInput.push('REPOSITORY_IDENTITY_MISMATCH');
      }
      const claimed = normalized.expectedByName.get(observation.repositoryFullName);
      if (!claimed || claimed.repositoryId !== consumer.repositoryId) {
        needsInput.push('REPOSITORY_NAME_NOT_EXPECTED');
      }
    }

    const trackStates = [];
    const desiredTracks = [];
    if (observation && needsInput.length === 0) {
      const observedTracks = new Set([
        ...observation.evidence.officialSdks.map(({ track }) => track),
        ...observation.evidence.customHttpTracks,
        ...observation.evidence.unmanagedTracks,
      ]);
      for (const track of observedTracks) {
        if (!consumer.tracks.includes(track)) {
          needsInput.push(`UNEXPECTED_${track.toUpperCase()}_INTEGRATION`);
        }
      }
      for (const track of consumer.tracks) {
        const integration = classifyTrack(observation, track);
        const desired = desiredSdk(manifest, track);
        const current = integration.status === 'managed' && sdkMatches(integration.sdk, desired);
        trackStates.push({
          track,
          integration: integration.status,
          current,
          observedVersion: integration.sdk?.version ?? null,
          approvedVersion: desired.version,
          observedContractRevision: integration.sdk?.contractRevision ?? null,
          approvedContractRevision: desired.contractRevision,
          observedArtifactSha256: integration.sdk?.artifactSha256 ?? null,
          approvedArtifactSha256: desired.artifactSha256,
          observedTreeChecksum: integration.sdk?.treeChecksum ?? null,
          approvedTreeChecksum: desired.treeChecksum ?? null,
          observedSource: integration.sdk?.source ?? null,
          approvedSource: desired.source ?? null,
        });
        if (integration.status === 'ambiguous') {
          needsInput.push(`AMBIGUOUS_${track.toUpperCase()}_INTEGRATION`);
        } else if (integration.status === 'missing') {
          needsInput.push(`MISSING_${track.toUpperCase()}_SDK`);
        }
        if (!current) {
          desiredTracks.push(desired);
        }
      }
    }

    needsInput = [...new Set(needsInput)].sort();
    if (needsInput.length > 0) {
      hasNeedsInput = true;
    }
    const stale = desiredTracks.length > 0;
    const exception = observation
      ? activeException(observation.exception, manifestDigest, nowTimestamp)
      : { active: false, reason: 'no-observation' };
    const nonManaged = trackStates.some(({ integration }) => integration !== 'managed');

    let gateStatus = 'PASS';
    const gateReasons = [];
    if (needsInput.length > 0) {
      gateStatus = 'BLOCKED';
      gateReasons.push(...needsInput);
    } else if (nonManaged) {
      gateStatus = 'BLOCKED';
      gateReasons.push('CUSTOM_OR_UNMANAGED_PLATFORM_INTEGRATION');
    } else if (stale && exception.active) {
      gateStatus = 'EXCEPTION';
      gateReasons.push('ACTIVE_STALE_SDK_EXCEPTION');
    } else if (stale) {
      gateStatus = 'BLOCKED';
      gateReasons.push(`STALE_PLATFORM_SDK_${exception.reason.toUpperCase().replaceAll('-', '_')}`);
    }

    let plannedAction = null;
    if (needsInput.length === 0 && stale) {
      if (manifest.contract.classification === 'implementation-only' && !nonManaged) {
        plannedAction = makePullRequestAction({
          consumer,
          manifest,
          manifestDigest,
          observation,
          desiredTracks,
        });
      } else if (manifest.contract.classification !== 'implementation-only') {
        plannedAction = makeContractIssueAction({
          consumer,
          manifest,
          manifestDigest,
          observation,
          trackStates,
        });
      }
    }

    let actionDisposition = null;
    if (plannedAction) {
      const existing = workIndex.byKey.get(plannedAction.idempotencyKey);
      if (existing) {
        if (
          existing.actionDigest !== plannedAction.actionDigest
          || existing.repositoryId !== plannedAction.repositoryId
          || existing.kind !== plannedAction.kind
          || existing.concurrencyKey !== plannedAction.concurrencyKey
        ) {
          throw new Error(`기존 work item readback이 계획과 다릅니다: ${plannedAction.idempotencyKey}`);
        }
        deduplicated.push({
          idempotencyKey: plannedAction.idempotencyKey,
          repositoryId: consumer.repositoryId,
          kind: plannedAction.kind,
          state: existing.state,
        });
        actionDisposition = 'deduplicated';
      } else if (workIndex.openByConcurrency.has(plannedAction.concurrencyKey)) {
        const active = workIndex.openByConcurrency.get(plannedAction.concurrencyKey);
        deferred.push({
          repositoryId: consumer.repositoryId,
          kind: plannedAction.kind,
          concurrencyKey: plannedAction.concurrencyKey,
          plannedIdempotencyKey: plannedAction.idempotencyKey,
          blockingIdempotencyKey: active.idempotencyKey,
        });
        actionDisposition = 'deferred';
      } else {
        actions.push(plannedAction);
        actionDisposition = 'planned';
      }
    }

    consumers.push({
      repositoryId: consumer.repositoryId,
      repositoryFullName: consumer.repositoryFullName,
      observationId: observation?.observationId ?? null,
      observationSourceType: observation?.sourceType ?? null,
      observationSnapshotDigest: observation?.snapshotDigest ?? null,
      configRevision: observation?.configRevision ?? null,
      sourceSha: observation?.sourceSha ?? null,
      trackStates,
      releaseGate: {
        status: gateStatus,
        reasons: [...new Set(gateReasons)].sort(),
        exceptionId: exception.active ? observation.exception.id : null,
        exceptionExpiresAt: exception.active ? observation.exception.expiresAt : null,
      },
      needsInput,
      plannedActionIdempotencyKey: plannedAction?.idempotencyKey ?? null,
      actionDisposition,
    });
  }

  actions.sort((left, right) => (
    compareUtf8(left.repositoryId, right.repositoryId) || compareUtf8(left.kind, right.kind)
  ));
  deduplicated.sort((left, right) => compareUtf8(left.idempotencyKey, right.idempotencyKey));
  deferred.sort((left, right) => compareUtf8(left.plannedIdempotencyKey, right.plannedIdempotencyKey));

  const unsignedPlan = {
    schemaVersion: 1,
    mode: 'dry-run',
    evaluatedAt: now,
    releaseApproval: {
      keyId: approval.keyId,
      manifestDigest,
      sourceSha: approvalPayload.sourceSha,
      releaseTag: approvalPayload.releaseTag,
      contractRevision: manifest.contract.revision,
      contractClassification: manifest.contract.classification,
      canaryEvidenceDigest: approvalPayload.canaryEvidence.attestationSha256,
      workflowBundleSourceSha: approvalPayload.canaryEvidence.workflowBundle.sourceSha,
      workflowBundleDigest: approvalPayload.canaryEvidence.workflowBundle.digest,
    },
    observationSnapshotDigest,
    consumers,
    actions,
    deduplicated,
    deferred,
    applyBlocked: hasNeedsInput,
    summary: {
      consumers: consumers.length,
      actions: actions.length,
      deduplicated: deduplicated.length,
      deferred: deferred.length,
      blockedReleaseGates: consumers.filter(({ releaseGate }) => releaseGate.status === 'BLOCKED').length,
      exceptionReleaseGates: consumers.filter(({ releaseGate }) => releaseGate.status === 'EXCEPTION').length,
      needsInput: consumers.filter(({ needsInput: input }) => input.length > 0).length,
    },
  };
  const plan = immutableClone({ ...unsignedPlan, planDigest: digestOf(unsignedPlan) });
  return plan;
}

function assertPlan(plan) {
  assertRecord(plan, 'Fleet plan');
  if (
    plan.schemaVersion !== 1
    || plan.mode !== 'dry-run'
    || !SHA256_REVISION_PATTERN.test(plan.planDigest ?? '')
    || digestOf(Object.fromEntries(Object.entries(plan).filter(([key]) => key !== 'planDigest')))
      !== plan.planDigest
  ) {
    throw new Error('Fleet plan 형식 또는 digest가 올바르지 않습니다.');
  }
  return plan;
}

function assertPlanState(state, plan) {
  assertExactKeys(
    state,
    [
      'concurrencyAvailable',
      'manifestDigest',
      'observationSnapshotDigest',
      'planDigest',
      'writeAllowed',
    ],
    'adapter plan state',
  );
  if (
    state.writeAllowed !== true
    || state.concurrencyAvailable !== true
    || state.planDigest !== plan.planDigest
    || state.manifestDigest !== plan.releaseApproval.manifestDigest
    || state.observationSnapshotDigest !== plan.observationSnapshotDigest
  ) {
    throw new Error('adapter의 최신 plan state readback이 실행 계획과 다릅니다.');
  }
}

export async function executeFleetPlan({ adapter, apply = false, plan, verification }) {
  assertPlan(plan);
  if (apply !== true) {
    return immutableClone({
      mode: 'dry-run',
      planDigest: plan.planDigest,
      plannedActions: plan.actions.length,
      appliedActions: [],
    });
  }
  if (!verification) {
    throw new Error('write 전 release 서명과 consumer observation 재검증 입력이 필요합니다.');
  }
  const executablePlan = reconcilePlatformFleet(verification);
  if (executablePlan.planDigest !== plan.planDigest) {
    throw new Error('재검증한 Fleet plan이 dry-run plan과 일치하지 않습니다.');
  }
  if (executablePlan.applyBlocked) {
    throw new Error('needs_input이 남은 Fleet plan은 부분 적용하지 않습니다.');
  }
  if (executablePlan.consumers.some(({ observationSourceType }) => observationSourceType !== 'backoffice')) {
    throw new Error('fixture 또는 출처 없는 consumer observation으로는 write할 수 없습니다.');
  }
  for (const method of ['applyAction', 'readAction', 'readPlanState']) {
    if (typeof adapter?.[method] !== 'function') {
      throw new Error(`trusted adapter에 ${method}가 없습니다.`);
    }
  }

  const appliedActions = [];
  for (const action of executablePlan.actions) {
    const state = await adapter.readPlanState({
      planDigest: executablePlan.planDigest,
      manifestDigest: executablePlan.releaseApproval.manifestDigest,
      observationSnapshotDigest: executablePlan.observationSnapshotDigest,
      repositoryId: action.repositoryId,
      idempotencyKey: action.idempotencyKey,
      actionDigest: action.actionDigest,
      concurrencyKey: action.concurrencyKey,
    });
    assertPlanState(state, executablePlan);
    await adapter.applyAction(action, {
      idempotencyKey: action.idempotencyKey,
      planDigest: executablePlan.planDigest,
      actionDigest: action.actionDigest,
      concurrencyKey: action.concurrencyKey,
    });
    const readback = await adapter.readAction({
      idempotencyKey: action.idempotencyKey,
      repositoryId: action.repositoryId,
    });
    assertExactKeys(
      readback,
      ['actionDigest', 'concurrencyKey', 'idempotencyKey', 'repositoryId', 'state'],
      'adapter action readback',
    );
    if (
      readback.state !== 'PERSISTED'
      || readback.idempotencyKey !== action.idempotencyKey
      || readback.actionDigest !== action.actionDigest
      || readback.repositoryId !== action.repositoryId
      || readback.concurrencyKey !== action.concurrencyKey
    ) {
      throw new Error(`action readback이 실행 계획과 다릅니다: ${action.idempotencyKey}`);
    }
    appliedActions.push(action.idempotencyKey);
  }
  return immutableClone({
    mode: 'applied',
    planDigest: executablePlan.planDigest,
    plannedActions: executablePlan.actions.length,
    appliedActions,
  });
}
