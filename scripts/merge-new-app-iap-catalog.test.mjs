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

// 운영 비밀값에 이용권 일정 없이 먼저 등록된 merge-lizard에 일정만 더한다(ADR 0036).
test('update-pass adds a schedule to an existing app and refuses other drift', () => {
  const mergeLizard = JSON.parse(readFileSync(new URL('../registry/iap-catalog/merge-lizard.json', import.meta.url), 'utf8'));
  const deployed = structuredClone(mergeLizard.apps['merge-lizard']);
  delete deployed.entitlements.amber_pass_30d.pass;
  const live = {version: 2, apps: {...structuredClone(current.apps), 'merge-lizard': deployed}};

  assert.throws(() => mergeNewAppIapCatalog(live, mergeLizard, 'merge-lizard'), /already exists/);
  const result = mergeNewAppIapCatalog(live, mergeLizard, 'merge-lizard', {updatePass: true});
  assert.deepEqual(result.apps['merge-lizard'], mergeLizard.apps['merge-lizard']);
  assert.deepEqual(result.apps['lizard-tycoon'], current.apps['lizard-tycoon']);
  assert.deepEqual(mergeNewAppIapCatalog(result, mergeLizard, 'merge-lizard', {updatePass: true}), result);

  // 놓친 날 정책 전환만 판매 중인 일정에 허용한다.
  const catchUp = structuredClone(mergeLizard);
  catchUp.apps['merge-lizard'].entitlements.amber_pass_30d.pass.missed_days = 'catch_up';
  assert.equal(mergeNewAppIapCatalog(result, catchUp, 'merge-lizard', {updatePass: true})
    .apps['merge-lizard'].entitlements.amber_pass_30d.pass.missed_days, 'catch_up');

  const mutate = [
    (c) => { c.entitlements.amber_pass_30d.pass.days = 7; },
    (c) => { c.entitlements.amber_pass_30d.pass.daily_amount = 10; },
    (c) => { delete c.entitlements.amber_pass_30d.pass; },
    (c) => { c.entitlements.amber_v2_s.google_play = 'other'; },
    (c) => { delete c.entitlements.premium_land; },
  ];
  for (const change of mutate) {
    const bad = structuredClone(mergeLizard);
    change(bad.apps['merge-lizard']);
    assert.throws(() => mergeNewAppIapCatalog(result, bad, 'merge-lizard', {updatePass: true}));
  }
});
