import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import test from 'node:test';
import {mergeLizardIapCatalog} from './merge-lizard-iap-catalog.mjs';
const read = p => JSON.parse(readFileSync(new URL(p, import.meta.url), 'utf8'));
const candidate = read('../registry/iap-catalog/lizard-tycoon.json');
const species = ['sp_bearded_dragon','sp_blue_tongue_skink','sp_veiled_chameleon'];
const old = Object.fromEntries(Object.entries(candidate.apps['lizard-tycoon'].entitlements).filter(([id]) => !species.includes(id)));
const current = {version: 2, apps: {'other-game': {entitlements: {old_item: {type: 'non_consumable', google_play: 'old_item'}}}, 'lizard-tycoon': {entitlements: old}}};

test('AC-3: adds three native species from 15 to 18 and preserves every existing item and other app', () => {
  assert.equal(Object.keys(old).length,15);
  const before = structuredClone(current);
  const result = mergeLizardIapCatalog(current,candidate);
  assert.equal(Object.keys(result.apps['lizard-tycoon'].entitlements).length,18);
  for (const [id,entry] of Object.entries(old)) assert.deepEqual(result.apps['lizard-tycoon'].entitlements[id],entry);
  assert.deepEqual(result.apps['other-game'], current.apps['other-game']);
  assert.deepEqual(current,before);
  assert.deepEqual(mergeLizardIapCatalog(result,candidate),result);
  for (const id of species) assert.deepEqual(Object.keys(result.apps['lizard-tycoon'].entitlements[id]).sort(),['app_store','google_play']);
  const app=read('../registry/apps/lizard-tycoon.json');
  assert.deepEqual([...app.iap.entitlement_ids].sort(),Object.keys(candidate.apps['lizard-tycoon'].entitlements).sort());
});

test('AC-3: rejects product drift, missing base items, partial install and unexpected additions', () => {
  assert.throws(() => mergeLizardIapCatalog({version:1,entitlements:old},candidate));
  for(const mutate of [
    e=>{e.sp_galaxy_gecko.google_play='unexpected';},
    e=>{delete e.crystal_300;},
    e=>{e.sp_bearded_dragon={google_play:'sp_bearded_dragon'};},
    e=>{e.unknown={google_play:'unknown'};}
  ]) {
    const drift=structuredClone(current);mutate(drift.apps['lizard-tycoon'].entitlements);
    assert.throws(()=>mergeLizardIapCatalog(drift,candidate));
  }
});
