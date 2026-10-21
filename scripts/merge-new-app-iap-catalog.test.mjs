import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import {mergeNewAppIapCatalog} from './merge-new-app-iap-catalog.mjs';

const candidate = JSON.parse(readFileSync(new URL('../registry/iap-catalog/bloomhand.json', import.meta.url), 'utf8'));
const current = {version: 2, apps: {'lizard-tycoon': {entitlements: {sp_galaxy_gecko: {google_play: 'sp_galaxy_gecko'}}}}};

test('adds bloomhand boxes without touching other apps and is idempotent', () => {
  const result = mergeNewAppIapCatalog(current, candidate, 'bloomhand');
  assert.deepEqual(result.apps['lizard-tycoon'], current.apps['lizard-tycoon']);
  assert.deepEqual(result.apps.bloomhand, candidate.apps.bloomhand);
  assert.deepEqual(mergeNewAppIapCatalog(result, candidate, 'bloomhand'), result);
});

test('adds merge-lizard products without touching other apps', () => {
  const mergeLizard = JSON.parse(readFileSync(new URL('../registry/iap-catalog/merge-lizard.json', import.meta.url), 'utf8'));
  const withBloomhand = mergeNewAppIapCatalog(current, candidate, 'bloomhand');
  const result = mergeNewAppIapCatalog(withBloomhand, mergeLizard, 'merge-lizard');
  assert.deepEqual(result.apps['lizard-tycoon'], current.apps['lizard-tycoon']);
  assert.deepEqual(result.apps.bloomhand, candidate.apps.bloomhand);
  assert.equal(Object.keys(result.apps['merge-lizard'].entitlements).length, 15);
  assert.deepEqual(mergeNewAppIapCatalog(result, mergeLizard, 'merge-lizard'), result);
});

test('refuses unknown format, changed existing app, and mismatched candidate', () => {
  assert.throws(() => mergeNewAppIapCatalog({version: 1, entitlements: {}}, candidate, 'bloomhand'));
  const drift = structuredClone(current);
  drift.apps.bloomhand = {entitlements: {friend_box_1: {type: 'consumable', google_play: 'other'}}};
  assert.throws(() => mergeNewAppIapCatalog(drift, candidate, 'bloomhand'));
  assert.throws(() => mergeNewAppIapCatalog(current, candidate, 'lizard-tycoon'));
});
