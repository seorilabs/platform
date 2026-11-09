#!/usr/bin/env node
import assert from 'node:assert/strict';
import {readFileSync, writeFileSync} from 'node:fs';
import {resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

const candidatePath = new URL('../registry/iap-catalog/lizard-tycoon.json', import.meta.url);
const speciesIds = new Set(['sp_bearded_dragon', 'sp_blue_tongue_skink', 'sp_veiled_chameleon']);

function assertCatalogShape(value, label) {
  assert.equal(value?.version, 2, `${label}: expected app-scoped catalog version 2`);
  assert.ok(value.apps && typeof value.apps === 'object' && !Array.isArray(value.apps), `${label}: apps missing`);
  assert.ok(!value.entitlements, `${label}: global entitlements are not supported`);
  assert.deepEqual(Object.keys(value).sort(), ['apps', 'version'], `${label}: unexpected root fields`);
}

export function mergeLizardIapCatalog(current, candidate) {
  assertCatalogShape(current, 'current');
  assertCatalogShape(candidate, 'candidate');
  assert.deepEqual(Object.keys(candidate.apps), ['lizard-tycoon'], 'candidate must contain only lizard-tycoon');
  const before = current.apps['lizard-tycoon']?.entitlements;
  const after = candidate.apps['lizard-tycoon']?.entitlements;
  assert.ok(before && after, 'lizard-tycoon entitlement map missing');
  assert.equal(Object.keys(after).length, 18, 'candidate must have 18 products');
  assert.equal([...speciesIds].filter(id => id in after).length, 3, 'candidate species products missing');
  for (const [id, entry] of Object.entries(before)) {
    assert.ok(id in after, `existing lizard product disappeared: ${id}`);
    assert.deepEqual(entry, after[id], `existing lizard product changed: ${id}`);
  }
  const existingSpecies = Object.keys(before).filter(id => speciesIds.has(id)).length;
  assert.ok(existingSpecies === 0 || existingSpecies === 3, 'partially installed species catalog requires investigation');
  assert.equal(Object.keys(before).length, 15 + existingSpecies, 'unexpected existing lizard products');
  const merged = structuredClone(current);
  merged.apps['lizard-tycoon'] = structuredClone(candidate.apps['lizard-tycoon']);
  for (const [appId, value] of Object.entries(current.apps)) {
    if (appId !== 'lizard-tycoon') assert.deepEqual(merged.apps[appId], value, `other app changed: ${appId}`);
  }
  return merged;
}

function main() {
  const [currentFlag, currentPath, outputFlag, outputPath] = process.argv.slice(2);
  assert.equal(currentFlag, '--current');
  assert.equal(outputFlag, '--output');
  assert.ok(currentPath && outputPath && process.argv.length === 6, 'usage: --current FILE --output NEW_FILE');
  assert.notEqual(resolve(currentPath), resolve(outputPath), 'output must be a different file');
  const current = JSON.parse(readFileSync(currentPath, 'utf8'));
  const candidate = JSON.parse(readFileSync(candidatePath, 'utf8'));
  const merged = mergeLizardIapCatalog(current, candidate);
  writeFileSync(outputPath, `${JSON.stringify(merged, null, 2)}\n`, {flag: 'wx', mode: 0o600});
  assert.deepEqual(JSON.parse(readFileSync(outputPath, 'utf8')), merged, 'merged file readback differs');
  console.log(`IAP_CATALOG_MERGE_OK apps=${Object.keys(merged.apps).length} lizard_products=18`);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  try { main(); } catch (error) { console.error(`IAP_CATALOG_MERGE_FAILED ${error.message}`); process.exitCode = 1; }
}
