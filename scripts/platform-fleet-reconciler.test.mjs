import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { describe, it } from 'node:test';

import {
  executeFleetPlan,
  platformReleaseApprovalPayload,
  reconcilePlatformFleet,
} from './platform-fleet-reconciler.mjs';

const NOW = '2026-08-27T00:00:00.000Z';
const SOURCE_SHA = 'a'.repeat(40);
const BASE_SHA = 'b'.repeat(40);
const CONTRACT_REVISION = `sha256:${'c'.repeat(64)}`;
const BASE_CONTRACT_REVISION = `sha256:${'d'.repeat(64)}`;
const TS_ARTIFACT_SHA = '1'.repeat(64);
const GD_ARTIFACT_SHA = '2'.repeat(64);
const GD_TREE_SHA = '3'.repeat(64);
const GD_CHECKSUM_SHA = '4'.repeat(64);

function stable(value) {
  if (Array.isArray(value)) {
    return value.map(stable);
  }
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, stable(value[key])]));
  }
  return value;
}

function digestPlan(plan) {
  const unsigned = Object.fromEntries(Object.entries(plan).filter(([key]) => key !== 'planDigest'));
  return `sha256:${createHash('sha256').update(JSON.stringify(stable(unsigned))).digest('hex')}`;
}

function manifest(classification = 'implementation-only', version = '0.7.0') {
  const artifactName = `seorilabs-platform-gdscript-${version}.tar.gz`;
  return {
    schemaVersion: 1,
    release: {
      tag: `v${version}`,
      sourceSha: SOURCE_SHA,
      baseSourceSha: BASE_SHA,
    },
    sdk: {
      typescript: {
        package: '@seorilabs/platform-sdk',
        version: '0.5.0',
        registry: 'https://npm.pkg.github.com',
        artifact: {
          name: 'seorilabs-platform-sdk-0.5.0.tgz',
          sha256: TS_ARTIFACT_SHA,
          size: 101,
        },
      },
      gdscript: {
        version,
        source: `https://github.com/seorilabs/platform/releases/download/v${version}/${artifactName}`,
        treeChecksum: GD_TREE_SHA,
        artifact: { name: artifactName, sha256: GD_ARTIFACT_SHA, size: 202 },
        checksumArtifact: {
          name: `${artifactName}.sha256`,
          sha256: GD_CHECKSUM_SHA,
          size: 96,
        },
      },
    },
    contract: {
      revision: CONTRACT_REVISION,
      baseRevision: BASE_CONTRACT_REVISION,
      classification,
      supportedApiMajor: 1,
      affectedConsumers: {
        cohort: 'backoffice-active-apps',
        resolution: 'reconcile-time',
      },
      affectedTracks: classification === 'implementation-only'
        ? ['gdscript', 'typescript']
        : ['gdscript', 'typescript'],
      affectedCapabilities: ['events', 'presence'],
    },
  };
}

function canaryApprovalEvidence() {
  const workflowSourceSha = '4'.repeat(40);
  const canary = (profile, repositoryId, repositoryFullName, sourceSha, runOffset) => ({
    profile,
    repositoryId,
    repositoryFullName,
    sourceSha,
    staticRun: {
      runId: String(1000 + runOffset),
      conclusion: 'success',
      headSha: sourceSha,
      workflowSourceSha,
    },
    buildOnlyRun: {
      runId: String(2000 + runOffset),
      conclusion: 'success',
      headSha: sourceSha,
      workflowSourceSha,
      cloudBuildId: `build-${runOffset}`,
      builderImageDigest: `sha256:${String(runOffset).repeat(64)}`,
      buildConfigDigest: `sha256:${String(runOffset + 2).repeat(64)}`,
      artifact: {
        name: `${profile}-release.aab`,
        sha256: `sha256:${String(runOffset + 4).repeat(64)}`,
        size: 1024 + runOffset,
      },
    },
  });
  return {
    attestationSha256: `sha256:${'8'.repeat(64)}`,
    readbackKeyId: 'canary-readback-1',
    workflowBundle: {
      repository: 'seorilabs/.github',
      sourceSha: workflowSourceSha,
      digest: `sha256:${'9'.repeat(64)}`,
    },
    canaries: [
      canary('godot', '1265192029', 'seorilabs/lizard-tycoon', '5'.repeat(40), 1),
      canary('react-native', '1250442131', 'seorilabs/happy-farm', '6'.repeat(40), 2),
    ],
  };
}

function signedRelease(classification = 'implementation-only') {
  const release = manifest(classification);
  const manifestContent = `${JSON.stringify(release, null, 2)}\n`;
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const payload = platformReleaseApprovalPayload(manifestContent, canaryApprovalEvidence());
  const signature = sign(
    null,
    Buffer.from(JSON.stringify(stable(payload)), 'utf8'),
    privateKey,
  ).toString('base64');
  return {
    manifestContent,
    approval: {
      schemaVersion: 2,
      algorithm: 'Ed25519',
      keyId: 'fixture-key-1',
      payload,
      signature,
    },
    trustedPublicKeys: new Map([['fixture-key-1', publicKey]]),
  };
}

function expected(repositoryId = '101', repositoryFullName = 'seorilabs/example', tracks = ['typescript']) {
  return { repositoryId, repositoryFullName, tracks };
}

function oldSdk(track) {
  if (track === 'typescript') {
    return {
      track,
      version: '0.4.0',
      artifactSha256: '5'.repeat(64),
      contractRevision: BASE_CONTRACT_REVISION,
    };
  }
  return {
    track,
    version: '0.6.5',
    artifactSha256: '6'.repeat(64),
    treeChecksum: '7'.repeat(64),
    source: 'https://github.com/seorilabs/platform/releases/download/v0.6.5/seorilabs-platform-gdscript-0.6.5.tar.gz',
    contractRevision: BASE_CONTRACT_REVISION,
  };
}

function currentSdk(track) {
  if (track === 'typescript') {
    return {
      track,
      version: '0.5.0',
      artifactSha256: TS_ARTIFACT_SHA,
      contractRevision: CONTRACT_REVISION,
    };
  }
  return {
    track,
    version: '0.7.0',
    artifactSha256: GD_ARTIFACT_SHA,
    treeChecksum: GD_TREE_SHA,
    source: 'https://github.com/seorilabs/platform/releases/download/v0.7.0/seorilabs-platform-gdscript-0.7.0.tar.gz',
    contractRevision: CONTRACT_REVISION,
  };
}

function observation({
  customHttpTracks = [],
  exception = null,
  id = 'observation-1',
  officialSdks = [oldSdk('typescript')],
  repositoryFullName = 'seorilabs/example',
  repositoryId = '101',
  sourceType = 'fixture',
  unmanagedTracks = [],
} = {}) {
  return {
    observationId: id,
    repositoryId,
    repositoryFullName,
    configRevision: 'config-revision-7',
    configState: 'ACTIVE',
    sourceSha: 'e'.repeat(40),
    snapshotDigest: `sha256:${'f'.repeat(64)}`,
    sourceType,
    observedAt: '2026-08-26T23:59:00.000Z',
    evidence: { officialSdks, customHttpTracks, unmanagedTracks },
    exception,
  };
}

function reconcileInputs({
  classification = 'implementation-only',
  existingWorkItems = [],
  expectedConsumers = [expected()],
  observations = [observation()],
} = {}) {
  return {
    ...signedRelease(classification),
    existingWorkItems,
    expectedConsumers,
    observations,
    now: NOW,
  };
}

function reconcile(options = {}) {
  return reconcilePlatformFleet(reconcileInputs(options));
}

describe('Platform Fleet release 승인', () => {
  it('발행 byte의 Ed25519 승인 서명과 manifest 의미 계약을 함께 검증한다', () => {
    const release = signedRelease();
    const plan = reconcilePlatformFleet({
      ...release,
      expectedConsumers: [expected()],
      observations: [observation()],
      now: NOW,
    });
    assert.equal(plan.releaseApproval.sourceSha, SOURCE_SHA);
    assert.equal(plan.releaseApproval.contractRevision, CONTRACT_REVISION);
    assert.match(plan.releaseApproval.manifestDigest, /^sha256:[0-9a-f]{64}$/u);
    assert.match(plan.releaseApproval.canaryEvidenceDigest, /^sha256:[0-9a-f]{64}$/u);
    assert.equal(plan.releaseApproval.workflowBundleSourceSha, '4'.repeat(40));
    assert.equal(Object.isFrozen(plan), true);
    assert.equal(Object.isFrozen(plan.actions[0]), true);
  });

  it('manifest 변조, 서명 변조, 미등록 key와 floating GDScript source를 거부한다', () => {
    const release = signedRelease();
    const changedManifest = release.manifestContent.replace(TS_ARTIFACT_SHA, '9'.repeat(64));
    assert.throws(() => reconcilePlatformFleet({
      ...release,
      manifestContent: changedManifest,
      expectedConsumers: [expected()],
      observations: [observation()],
      now: NOW,
    }), /payload|서명/u);

    assert.throws(() => reconcilePlatformFleet({
      ...release,
      approval: { ...release.approval, signature: Buffer.alloc(64).toString('base64') },
      expectedConsumers: [expected()],
      observations: [observation()],
      now: NOW,
    }), /서명/u);

    assert.throws(() => reconcilePlatformFleet({
      ...release,
      trustedPublicKeys: new Map(),
      expectedConsumers: [expected()],
      observations: [observation()],
      now: NOW,
    }), /신뢰하지 않는/u);

    assert.throws(() => reconcilePlatformFleet({
      ...release,
      approval: { ...release.approval, schemaVersion: 1 },
      expectedConsumers: [expected()],
      observations: [observation()],
      now: NOW,
    }), /지원하지 않는/u);

    const invalid = manifest();
    invalid.sdk.gdscript.source = 'https://github.com/seorilabs/platform/tree/main/sdk-gdscript';
    assert.throws(
      () => platformReleaseApprovalPayload(
        `${JSON.stringify(invalid)}\n`,
        canaryApprovalEvidence(),
      ),
      /고정 source URL/u,
    );
  });

  it('새 release의 영향 consumer 선택 계약을 검증하고 v0.6.7 omission만 읽기 허용한다', () => {
    const malformed = manifest();
    malformed.contract = null;
    assert.throws(
      () => platformReleaseApprovalPayload(
        `${JSON.stringify(malformed)}\n`,
        canaryApprovalEvidence(),
      ),
      /manifest\.contract 형식이 올바르지 않습니다/u,
    );

    const invalid = manifest();
    invalid.contract.affectedConsumers.cohort = 'repository-file-list';
    assert.throws(
      () => platformReleaseApprovalPayload(
        `${JSON.stringify(invalid)}\n`,
        canaryApprovalEvidence(),
      ),
      /affectedConsumers/u,
    );

    const legacy = manifest();
    assert.throws(
      () => platformReleaseApprovalPayload(
        `${JSON.stringify({
          ...legacy,
          contract: Object.fromEntries(
            Object.entries(legacy.contract).filter(([key]) => key !== 'affectedConsumers'),
          ),
        })}\n`,
        canaryApprovalEvidence(),
      ),
      /affectedConsumers/u,
    );

    const publishedLegacy = manifest('implementation-only', '0.6.7');
    delete publishedLegacy.contract.affectedConsumers;
    assert.doesNotThrow(() => platformReleaseApprovalPayload(
      `${JSON.stringify(publishedLegacy)}\n`,
      canaryApprovalEvidence(),
    ));

    for (const version of ['0.6.6', '0.6.8']) {
      const unsupportedOmission = manifest('implementation-only', version);
      delete unsupportedOmission.contract.affectedConsumers;
      assert.throws(
        () => platformReleaseApprovalPayload(
          `${JSON.stringify(unsupportedOmission)}\n`,
          canaryApprovalEvidence(),
        ),
        /affectedConsumers/u,
      );
    }
  });
});

describe('Platform Fleet 계획과 중복 방지', () => {
  it('compatible release는 두 track을 exact update하는 PR 계획을 repo별 하나만 만든다', () => {
    const inputs = {
      expectedConsumers: [expected('101', 'seorilabs/example', ['gdscript', 'typescript'])],
      observations: [observation({ officialSdks: [oldSdk('gdscript'), oldSdk('typescript')] })],
    };
    const first = reconcile(inputs);
    const second = reconcile(inputs);
    assert.equal(first.planDigest, second.planDigest);
    assert.equal(first.actions.length, 1);
    assert.equal(first.actions[0].kind, 'pull-request');
    assert.deepEqual(first.actions[0].exactUpdates.map(({ track }) => track), ['gdscript', 'typescript']);
    assert.equal(first.consumers[0].releaseGate.status, 'BLOCKED');
    assert.match(first.actions[0].idempotencyKey, /^platform-fleet\/v1\/[0-9a-f]{64}$/u);

    const duplicateInput = reconcile({
      ...inputs,
      observations: [inputs.observations[0], structuredClone(inputs.observations[0])],
    });
    assert.equal(duplicateInput.actions.length, 1);
    assert.equal(duplicateInput.actions[0].idempotencyKey, first.actions[0].idempotencyKey);

    const reordered = reconcile({
      ...inputs,
      observations: [observation({
        officialSdks: [oldSdk('typescript'), oldSdk('gdscript')],
      })],
    });
    assert.equal(reordered.planDigest, first.planDigest);
  });

  it('기존 work item readback이 있으면 같은 PR이나 Issue를 다시 계획하지 않는다', () => {
    const first = reconcile();
    const action = first.actions[0];
    const second = reconcile({
      existingWorkItems: [{
        idempotencyKey: action.idempotencyKey,
        actionDigest: action.actionDigest,
        concurrencyKey: action.concurrencyKey,
        repositoryId: action.repositoryId,
        kind: action.kind,
        state: 'OPEN',
      }],
    });
    assert.equal(second.actions.length, 0);
    assert.deepEqual(second.deduplicated, [{
      idempotencyKey: action.idempotencyKey,
      repositoryId: '101',
      kind: 'pull-request',
      state: 'OPEN',
    }]);

    assert.throws(() => reconcile({
      existingWorkItems: [{
        idempotencyKey: action.idempotencyKey,
        actionDigest: action.actionDigest,
        concurrencyKey: `${action.concurrencyKey}-mismatch`,
        repositoryId: action.repositoryId,
        kind: action.kind,
        state: 'CLOSED',
      }],
    }), /readback/u);
  });

  it('다른 release의 자율 PR이 열려 있으면 repo concurrency scope에서 새 PR을 보류한다', () => {
    const first = reconcile();
    const action = first.actions[0];
    const plan = reconcile({
      existingWorkItems: [{
        idempotencyKey: `platform-fleet/v1/${'8'.repeat(64)}`,
        actionDigest: `sha256:${'7'.repeat(64)}`,
        concurrencyKey: action.concurrencyKey,
        repositoryId: action.repositoryId,
        kind: 'pull-request',
        state: 'OPEN',
      }],
    });
    assert.equal(plan.actions.length, 0);
    assert.equal(plan.deferred.length, 1);
    assert.equal(plan.deferred[0].plannedIdempotencyKey, action.idempotencyKey);
    assert.equal(plan.consumers[0].actionDisposition, 'deferred');
  });

  for (const classification of ['contract-additive', 'contract-breaking']) {
    it(`${classification} release는 repo별 P1 계약 Issue 계획 하나만 만든다`, () => {
      const plan = reconcile({ classification });
      assert.equal(plan.actions.length, 1);
      assert.equal(plan.actions[0].kind, 'issue');
      assert.equal(plan.actions[0].priority, 'P1');
      assert.deepEqual(plan.actions[0].labels, ['P1', 'autopilot', 'platform', 'platform-contract']);
      assert.equal(plan.actions[0].contractClassification, classification);
      assert.equal(plan.actions.some(({ kind }) => kind === 'pull-request'), false);
    });
  }

  it('서로 다른 관찰 두 개는 최신값을 추측하지 않고 needs_input으로 중단한다', () => {
    const plan = reconcile({
      observations: [
        observation({ id: 'observation-1' }),
        observation({ id: 'observation-2', officialSdks: [currentSdk('typescript')] }),
      ],
    });
    assert.equal(plan.applyBlocked, true);
    assert.deepEqual(plan.consumers[0].needsInput, ['AMBIGUOUS_CONSUMER_OBSERVATION']);
    assert.equal(plan.actions.length, 0);
  });

  it('대소문자 observation ID도 locale과 입력 순서에 무관한 plan digest를 만든다', () => {
    const expectedConsumers = [
      expected('101', 'seorilabs/example-a'),
      expected('102', 'seorilabs/example-b'),
    ];
    const observations = [
      observation({
        id: 'A-observation',
        repositoryId: '101',
        repositoryFullName: 'seorilabs/example-a',
      }),
      observation({
        id: 'a-observation',
        repositoryId: '102',
        repositoryFullName: 'seorilabs/example-b',
      }),
    ];
    const first = reconcile({ expectedConsumers, observations });
    const second = reconcile({
      expectedConsumers: [...expectedConsumers].reverse(),
      observations: [...observations].reverse(),
    });
    assert.equal(first.planDigest, second.planDigest);
  });
});

describe('탑재 상태와 release gate', () => {
  it('observed SDK가 승인 manifest와 byte provenance까지 같을 때만 통과한다', () => {
    const plan = reconcile({ observations: [observation({ officialSdks: [currentSdk('typescript')] })] });
    assert.equal(plan.actions.length, 0);
    assert.equal(plan.consumers[0].trackStates[0].current, true);
    assert.deepEqual(plan.consumers[0].releaseGate, {
      status: 'PASS',
      reasons: [],
      exceptionId: null,
      exceptionExpiresAt: null,
    });
  });

  it('만료되지 않고 현재 manifest에 묶인 예외만 stale release build를 허용한다', () => {
    const release = signedRelease();
    const base = {
      id: 'exception-1',
      capability: 'release-build',
      status: 'ACTIVE',
      manifestSha256: release.approval.payload.manifestSha256,
      expiresAt: '2026-08-27T00:05:00.000Z',
    };
    const active = reconcilePlatformFleet({
      ...release,
      expectedConsumers: [expected()],
      observations: [observation({ exception: base })],
      now: NOW,
    });
    assert.equal(active.consumers[0].releaseGate.status, 'EXCEPTION');
    assert.equal(active.consumers[0].releaseGate.exceptionId, 'exception-1');

    const expired = reconcilePlatformFleet({
      ...release,
      expectedConsumers: [expected()],
      observations: [observation({ exception: { ...base, expiresAt: NOW } })],
      now: NOW,
    });
    assert.equal(expired.consumers[0].releaseGate.status, 'BLOCKED');
    assert.deepEqual(expired.consumers[0].releaseGate.reasons, ['STALE_PLATFORM_SDK_EXPIRED']);

    const mismatched = reconcilePlatformFleet({
      ...release,
      expectedConsumers: [expected()],
      observations: [observation({
        exception: { ...base, manifestSha256: `sha256:${'0'.repeat(64)}` },
      })],
      now: NOW,
    });
    assert.deepEqual(mismatched.consumers[0].releaseGate.reasons, [
      'STALE_PLATFORM_SDK_MANIFEST_MISMATCH',
    ]);
  });

  it('custom, unmanaged, missing SDK를 구분하고 불확실한 missing은 계획하지 않는다', () => {
    const expectedConsumers = [
      expected('101', 'seorilabs/custom'),
      expected('102', 'seorilabs/unmanaged', ['gdscript']),
      expected('103', 'seorilabs/missing'),
    ];
    const observations = [
      observation({
        id: 'custom-observation',
        repositoryId: '101',
        repositoryFullName: 'seorilabs/custom',
        officialSdks: [],
        customHttpTracks: ['typescript'],
      }),
      observation({
        id: 'unmanaged-observation',
        repositoryId: '102',
        repositoryFullName: 'seorilabs/unmanaged',
        officialSdks: [],
        unmanagedTracks: ['gdscript'],
      }),
      observation({
        id: 'missing-observation',
        repositoryId: '103',
        repositoryFullName: 'seorilabs/missing',
        officialSdks: [],
      }),
    ];
    const plan = reconcile({
      classification: 'contract-additive',
      expectedConsumers,
      observations,
    });
    assert.deepEqual(
      plan.consumers.map(({ trackStates }) => trackStates[0]?.integration),
      ['custom', 'unmanaged', 'missing'],
    );
    assert.deepEqual(plan.actions.map(({ repositoryId }) => repositoryId), ['101', '102']);
    assert.deepEqual(plan.consumers[2].needsInput, ['MISSING_TYPESCRIPT_SDK']);
    assert.equal(plan.consumers.every(({ releaseGate }) => releaseGate.status === 'BLOCKED'), true);
    assert.equal(plan.applyBlocked, true);
  });

  it('필수 consumer observation 자체가 없으면 needs_input으로 fail-closed한다', () => {
    const plan = reconcile({ observations: [] });
    assert.equal(plan.actions.length, 0);
    assert.equal(plan.applyBlocked, true);
    assert.deepEqual(plan.consumers[0].needsInput, ['MISSING_CONSUMER_OBSERVATION']);
  });

  it('desired state에 없는 SDK track과 비활성 config observation을 허용하지 않는다', () => {
    const unexpected = reconcile({
      observations: [observation({
        officialSdks: [oldSdk('gdscript'), oldSdk('typescript')],
      })],
    });
    assert.equal(unexpected.actions.length, 0);
    assert.deepEqual(unexpected.consumers[0].needsInput, ['UNEXPECTED_GDSCRIPT_INTEGRATION']);

    const inactive = observation();
    inactive.configState = 'DRAFT';
    assert.throws(
      () => reconcile({ observations: [inactive] }),
      /configState/u,
    );
  });
});

describe('trusted adapter 실행 경계', () => {
  it('기본값은 adapter 없이도 영구 변경이 없는 dry-run이다', async () => {
    const plan = reconcile();
    assert.deepEqual(await executeFleetPlan({ plan }), {
      mode: 'dry-run',
      planDigest: plan.planDigest,
      plannedActions: 1,
      appliedActions: [],
    });
  });

  it('직렬화된 plan도 서명 재검증, 최신 state preflight, action readback 뒤 write한다', async () => {
    const verification = reconcileInputs({
      observations: [observation({ sourceType: 'backoffice' })],
    });
    const plan = reconcilePlatformFleet(verification);
    const calls = [];
    const adapter = {
      async readPlanState() {
        calls.push('preflight');
        return {
          planDigest: plan.planDigest,
          manifestDigest: plan.releaseApproval.manifestDigest,
          observationSnapshotDigest: plan.observationSnapshotDigest,
          writeAllowed: true,
          concurrencyAvailable: true,
        };
      },
      async applyAction(action, context) {
        calls.push(`apply:${context.idempotencyKey}`);
        assert.equal(context.planDigest, plan.planDigest);
        assert.deepEqual(action, plan.actions[0]);
      },
      async readAction() {
        calls.push('readback');
        const action = plan.actions[0];
        return {
          idempotencyKey: action.idempotencyKey,
          actionDigest: action.actionDigest,
          concurrencyKey: action.concurrencyKey,
          repositoryId: action.repositoryId,
          state: 'PERSISTED',
        };
      },
    };
    const result = await executeFleetPlan({
      plan: structuredClone(plan),
      adapter,
      apply: true,
      verification,
    });
    assert.equal(result.mode, 'applied');
    assert.deepEqual(result.appliedActions, [plan.actions[0].idempotencyKey]);
    assert.deepEqual(calls, [
      'preflight',
      `apply:${plan.actions[0].idempotencyKey}`,
      'readback',
    ]);
  });

  it('사전 검증 실패는 write 전 차단하고 불일치 action readback은 후속 write를 중단한다', async () => {
    let guardedWrites = 0;
    const guardedAdapter = {
      async readPlanState() { throw new Error('호출되면 안 됩니다.'); },
      async applyAction() { guardedWrites += 1; },
      async readAction() { throw new Error('호출되면 안 됩니다.'); },
    };
    const fixtureVerification = reconcileInputs();
    const fixturePlan = reconcilePlatformFleet(fixtureVerification);
    await assert.rejects(
      executeFleetPlan({ plan: fixturePlan, adapter: guardedAdapter, apply: true }),
      /재검증 입력/u,
    );
    await assert.rejects(
      executeFleetPlan({
        plan: fixturePlan,
        adapter: guardedAdapter,
        apply: true,
        verification: fixtureVerification,
      }),
      /fixture/u,
    );

    const blockedVerification = reconcileInputs({ observations: [] });
    const blockedPlan = reconcilePlatformFleet(blockedVerification);
    await assert.rejects(
      executeFleetPlan({
        plan: blockedPlan,
        adapter: guardedAdapter,
        apply: true,
        verification: blockedVerification,
      }),
      /needs_input/u,
    );
    assert.equal(guardedWrites, 0);

    const verification = reconcileInputs({
      expectedConsumers: [
        expected(),
        expected('202', 'seorilabs/second'),
      ],
      observations: [
        observation({ sourceType: 'backoffice' }),
        observation({
          id: 'observation-2',
          repositoryFullName: 'seorilabs/second',
          repositoryId: '202',
          sourceType: 'backoffice',
        }),
      ],
    });
    const plan = reconcilePlatformFleet(verification);
    let writes = 0;
    const staleAdapter = {
      async readPlanState() {
        return {
          planDigest: plan.planDigest,
          manifestDigest: `sha256:${'0'.repeat(64)}`,
          observationSnapshotDigest: plan.observationSnapshotDigest,
          writeAllowed: true,
          concurrencyAvailable: true,
        };
      },
      async applyAction() { writes += 1; },
      async readAction() { throw new Error('호출되면 안 됩니다.'); },
    };
    await assert.rejects(
      executeFleetPlan({ plan, adapter: staleAdapter, apply: true, verification }),
      /최신 plan state/u,
    );
    assert.equal(writes, 0);

    const mismatchAdapter = {
      async readPlanState() {
        return {
          planDigest: plan.planDigest,
          manifestDigest: plan.releaseApproval.manifestDigest,
          observationSnapshotDigest: plan.observationSnapshotDigest,
          writeAllowed: true,
          concurrencyAvailable: true,
        };
      },
      async applyAction() { writes += 1; },
      async readAction() {
        return {
          idempotencyKey: plan.actions[0].idempotencyKey,
          actionDigest: `sha256:${'9'.repeat(64)}`,
          concurrencyKey: plan.actions[0].concurrencyKey,
          repositoryId: '101',
          state: 'PERSISTED',
        };
      },
    };
    await assert.rejects(
      executeFleetPlan({ plan, adapter: mismatchAdapter, apply: true, verification }),
      /readback/u,
    );
    assert.equal(writes, 1);

    const forged = structuredClone(plan);
    forged.actions[0].title = '위조된 작업';
    forged.planDigest = digestPlan(forged);
    await assert.rejects(
      executeFleetPlan({ plan: forged, adapter: mismatchAdapter, apply: true, verification }),
      /재검증한 Fleet plan/u,
    );
    assert.equal(writes, 1);
  });
});

describe('순수 코어 의존성 경계', () => {
  it('GitHub/provider mutation client와 secret 입력 경로를 포함하지 않는다', async () => {
    const source = await readFile(
      new URL('./platform-fleet-reconciler.mjs', import.meta.url),
      'utf8',
    );
    const imports = [...source.matchAll(/from\s+['"]([^'"]+)['"]/gu)]
      .map((match) => match[1]);

    assert.deepEqual(imports, ['node:crypto']);
    assert.doesNotMatch(source, /\bfetch\s*\(/u);
    assert.doesNotMatch(source, /node:(?:http|https|net|tls|child_process|fs)/u);
    assert.doesNotMatch(source, /process\.env|GITHUB_TOKEN|Authorization/u);
    assert.doesNotMatch(source, /\b(?:getSecret|printSecret|copyPassword)\b/u);
  });
});
