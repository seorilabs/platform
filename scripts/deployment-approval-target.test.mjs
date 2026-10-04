import assert from 'node:assert/strict';
import { readFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import test from 'node:test';

for (const [file, environment] of [['deploy.yml', 'production'], ['deploy-staging.yml', 'staging'], ['presence-edge.yml', 'production']]) {
  test(`${file}: 승인 이전 job이 실행과 이미지 대상을 artifact로 고정`, () => {
    const source = readFileSync(new URL(`../.github/workflows/${file}`, import.meta.url), 'utf8');
    // job 전체는 다음 job의 두 칸 들여쓰기까지다.
    const job = source.match(/^  approval-target:\n([\s\S]*?)(?=^  [a-z][\w-]*:)/m)[1];
    assert.match(job, /runs-on: ubuntu-latest/);
    if (file === "presence-edge.yml") assert.ok(job.includes("if: ${{ inputs.deploy }}"));
    assert.doesNotMatch(job, /^    environment:/m);
    assert.doesNotMatch(job, /secrets\./);
    assert.match(source, /needs: (\[build, approval-target\]|approval-target)/);
    assert.match(job, /retention-days: 3/);
    const script = job.match(/        run: \|\n([\s\S]*?)\n      - uses:/)[1].replace(/^          /gm, '');
    const directory = mkdtempSync(join(tmpdir(), 'platform-approval-'));
    try {
      const targets = JSON.parse(job.match(/          TARGETS: '(.*)'/)[1]);
      execFileSync('bash', ['-c', script], { cwd: directory, env: { ...process.env, SOURCE_SHA: 'a'.repeat(40), IMAGE_SHA: 'b'.repeat(40), GITHUB_REPOSITORY: 'seorilabs/platform', GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '2', TARGET_ENVIRONMENT: environment, WORKFLOW_FILE: file, IMAGE_BASE: 'example/image', TARGETS: JSON.stringify(targets) } });
      const value = JSON.parse(readFileSync(join(directory, 'deployment-target.json'), 'utf8'));
      assert.equal(value.runAttempt, 2); assert.equal(value.runId, '123');
      assert.equal(value.sourceSha, 'a'.repeat(40)); assert.equal(value.imageSha, 'b'.repeat(40));
      assert.equal(value.environment, environment); assert.deepEqual(value.targets, targets);
      assert.ok(targets.length > 0);
    } finally { rmSync(directory, { recursive: true, force: true }); }
  });
}
