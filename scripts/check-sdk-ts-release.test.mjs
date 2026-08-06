import assert from 'node:assert/strict';
import { describe, it } from 'node:test';

import { assertSdkReleaseVersions } from './check-sdk-ts-release.mjs';

describe('SDK 발행 버전 계약', () => {
  it('tag, package, SDK 버전이 같으면 통과한다', () => {
    assert.doesNotThrow(() => assertSdkReleaseVersions('sdk-ts-v0.1.0', '0.1.0', '0.1.0'));
  });

  it('tag 형식이 다르면 중단한다', () => {
    assert.throws(() => assertSdkReleaseVersions('v0.1.0', '0.1.0', '0.1.0'), /tag 형식/);
  });

  it('tag와 package 버전이 다르면 중단한다', () => {
    assert.throws(() => assertSdkReleaseVersions('sdk-ts-v0.1.1', '0.1.0', '0.1.0'), /tag 0.1.1/);
  });

  it('package와 SDK 버전이 다르면 중단한다', () => {
    assert.throws(() => assertSdkReleaseVersions('sdk-ts-v0.1.0', '0.1.0', '0.1.1'), /SDK 0.1.1/);
  });
});
