import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import {mergeLizardIapCatalog} from './merge-lizard-iap-catalog.mjs';

const candidate = JSON.parse(readFileSync(new URL('../registry/iap-catalog/lizard-tycoon.json', import.meta.url), 'utf8'));
const old = Object.fromEntries(Object.entries(candidate.apps['lizard-tycoon'].entitlements).filter(([id]) => !id.startsWith('crystal_')));
const current = {version: 2, apps: {'other-game': {entitlements: {old_item: {type: 'non_consumable', google_play: 'old_item'}}}, 'lizard-tycoon': {entitlements: old}}};

test('adds five crystals while preserving every other app and legacy product', () => {
  for (const entry of Object.values(old)) {
    assert.deepEqual(Object.keys(entry).sort(), ['app_store', 'apps_in_toss', 'google_play']);
  }
  const result = mergeLizardIapCatalog(current, candidate);
  assert.deepEqual(result.apps['other-game'], current.apps['other-game']);
  assert.deepEqual(result.apps['lizard-tycoon'], candidate.apps['lizard-tycoon']);
  assert.equal(Object.keys(result.apps['lizard-tycoon'].entitlements).length, 15);
  assert.deepEqual(mergeLizardIapCatalog(result, candidate), result);
});

test('fails on unknown format, legacy drift, and partial crystal installation', () => {
  assert.throws(() => mergeLizardIapCatalog({version: 1, entitlements: old}, candidate));
  const drift = structuredClone(current);
  drift.apps['lizard-tycoon'].entitlements.sp_galaxy_gecko.google_play = 'unexpected';
  assert.throws(() => mergeLizardIapCatalog(drift, candidate));
  const partial = structuredClone(current);
  partial.apps['lizard-tycoon'].entitlements.crystal_300 = candidate.apps['lizard-tycoon'].entitlements.crystal_300;
  assert.throws(() => mergeLizardIapCatalog(partial, candidate));
});
