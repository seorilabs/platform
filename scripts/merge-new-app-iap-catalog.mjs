#!/usr/bin/env node
// 운영 iap-catalog 비밀값에 새 앱의 카탈로그를 더한다. 다른 앱 항목은 바꾸지 않는다.
// 이미 같은 앱이 있으면 후보와 완전히 같을 때만 통과한다(변경은 앱별 전용 도구로 검토한다).
// --update-pass는 그 예외 하나다. 기존 항목에 기간 이용권 일정(pass)을 더하거나 놓친 날
// 정책만 바꾼다. 판매 중인 일정의 지급량과 일수 변경과 일정 삭제는 이미 산 이용권에 바로
// 적용되므로 거부한다(ADR 0036). 상품 목록, 유형, SKU는 그대로여야 한다.
// usage: merge-new-app-iap-catalog.mjs --app APP_ID --current FILE --output NEW_FILE [--update-pass]
import assert from 'node:assert/strict';
import {readFileSync, writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

function assertCatalogShape(value, label) {
  assert.equal(value?.version, 2, `${label}: expected app-scoped catalog version 2`);
  assert.ok(value.apps && typeof value.apps === 'object' && !Array.isArray(value.apps), `${label}: apps missing`);
  assert.deepEqual(Object.keys(value).sort(), ['apps', 'version'], `${label}: unexpected root fields`);
}

function withoutPass(entries) {
  return Object.fromEntries(Object.entries(entries).map(([id, entry]) => {
    const {pass: _pass, ...rest} = entry;
    return [id, rest];
  }));
}

function assertOnlyPassAdded(current, candidate, appId) {
  assert.deepEqual(Object.keys(current).sort(), ['entitlements'], `${appId}: unexpected current fields`);
  assert.deepEqual(Object.keys(candidate).sort(), ['entitlements'], `${appId}: unexpected candidate fields`);
  assert.deepEqual(withoutPass(candidate.entitlements), withoutPass(current.entitlements), `${appId} products changed beyond pass schedules`);
  for (const [id, entry] of Object.entries(current.entitlements)) {
    if (!entry.pass) continue;
    const next = candidate.entitlements[id].pass;
    assert.ok(next, `${appId}/${id}: pass schedule removal is not allowed`);
    const {missed_days: _before, ...sold} = entry.pass;
    const {missed_days: _after, ...proposed} = next;
    assert.deepEqual(proposed, sold, `${appId}/${id}: only missed_days may change on a sold pass`);
  }
}

export function mergeNewAppIapCatalog(current, candidate, appId, {updatePass = false} = {}) {
  assertCatalogShape(current, 'current');
  assertCatalogShape(candidate, 'candidate');
  assert.deepEqual(Object.keys(candidate.apps), [appId], `candidate must contain only ${appId}`);
  const entries = candidate.apps[appId]?.entitlements;
  assert.ok(entries && Object.keys(entries).length > 0, `${appId} entitlement map missing`);
  if (appId in current.apps) {
    if (updatePass) assertOnlyPassAdded(current.apps[appId], candidate.apps[appId], appId);
    else assert.deepEqual(current.apps[appId], candidate.apps[appId], `${appId} already exists with different products`);
  }
  const merged = structuredClone(current);
  merged.apps[appId] = structuredClone(candidate.apps[appId]);
  for (const [otherId, value] of Object.entries(current.apps)) {
    if (otherId !== appId) assert.deepEqual(merged.apps[otherId], value, `other app changed: ${otherId}`);
  }
  return merged;
}

function main() {
  const args = process.argv.slice(2);
  const updatePass = args.at(-1) === '--update-pass';
  if (updatePass) args.pop();
  assert.equal(args.length, 6, 'usage: --app APP_ID --current FILE --output NEW_FILE [--update-pass]');
  const [appFlag, appId, currentFlag, currentPath, outputFlag, outputPath] = args;
  assert.equal(appFlag, '--app');
  assert.equal(currentFlag, '--current');
  assert.equal(outputFlag, '--output');
  assert.match(appId, /^[a-z0-9-]+$/);
  assert.notEqual(resolve(currentPath), resolve(outputPath), 'output must be a different file');
  const current = JSON.parse(readFileSync(currentPath, 'utf8'));
  const candidate = JSON.parse(readFileSync(new URL(`../registry/iap-catalog/${appId}.json`, import.meta.url), 'utf8'));
  const merged = mergeNewAppIapCatalog(current, candidate, appId, {updatePass});
  writeFileSync(outputPath, `${JSON.stringify(merged, null, 2)}\n`, {flag: 'wx', mode: 0o600});
  assert.deepEqual(JSON.parse(readFileSync(outputPath, 'utf8')), merged, 'merged file readback differs');
  console.log(`IAP_CATALOG_MERGE_OK apps=${Object.keys(merged.apps).length} ${appId}_products=${Object.keys(merged.apps[appId].entitlements).length}`);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  try { main(); } catch (error) { console.error(`IAP_CATALOG_MERGE_FAILED ${error.message}`); process.exitCode = 1; }
}
