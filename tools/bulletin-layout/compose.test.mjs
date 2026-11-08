import assert from 'node:assert/strict';
import {test} from 'node:test';
import {composeWithDiagnostics} from './compose.mjs';

const input = {expectedContentHash: 'a'.repeat(64), submissionJSON: JSON.stringify({document: {layoutManifest: {rendererArtifactSha256: 'b'.repeat(64)}}})};
test('preserves successful immutable composer output', async () => {
  const result = {submissionJSON: 'unchanged', measurement: {}};
  assert.equal(await composeWithDiagnostics(input, {composeBulletinLayout: async () => result}), result);
});
test('returns only bounded identity and source locations for editable layout failures', async () => {
  const result = await composeWithDiagnostics(input, {composeBulletinLayout: async () => {throw new Error('page_requires_edit', {cause: {pageId: 'p2', slotId: 'title-slot', privateText: 'never export'}})}});
  assert.deepEqual(result, {failure: {contentHash: input.expectedContentHash, rendererArtifactSha256: 'b'.repeat(64), locations: [{pageId: 'p2', slotId: 'title-slot'}]}});
});
test('uses measured overflow locations when composition has no specific cause', async () => {
  const result = await composeWithDiagnostics(input, {composeBulletinLayout: async () => {throw new Error('page_requires_edit')}, measureBulletinLayout: async () => ({overflow: [{pageId: 'p3', slotId: 'paragraph', text: 'never export'}]})});
  assert.deepEqual(result.failure.locations, [{pageId: 'p3', slotId: 'paragraph'}]);
});
test('locates inherited page-only body failures and cover failures without inventing a block', async () => {
  const body = {composeBulletinLayout: async () => {throw new Error('page_requires_edit', {cause: {pageId:'p2', bottom:999}})}, measureBulletinLayout: async () => ({overflow:[{pageId:'p2',slotId:'body'}]})};
  assert.deepEqual((await composeWithDiagnostics(input, body)).failure.locations,[{pageId:'p2',slotId:'body'}]);
  body.measureBulletinLayout=async()=>({overflow:[]});
  assert.deepEqual((await composeWithDiagnostics(input, body)).failure.locations,[{pageId:'p2'}]);
  const coverInput={...input,submissionJSON:JSON.stringify({document:{layoutManifest:{rendererArtifactSha256:'b'.repeat(64),pages:[{pageId:'p1'}]}}})};
  assert.deepEqual((await composeWithDiagnostics(coverInput,{composeBulletinLayout:async()=>{throw new Error('cover_requires_edit')},measureBulletinLayout:async()=>({overflow:[]})})).failure.locations,[{pageId:'p1'}]);
});
test('never diagnoses tampering, unknown failures, invalid IDs or empty evidence as an editable problem', async () => {
  for (const error of [new Error('stale_content'), new Error('private path'), new Error('page_requires_edit', {cause: {pageId: '../private', slotId: 'a'}})]) {
    await assert.rejects(composeWithDiagnostics(input, {composeBulletinLayout: async () => {throw error}}));
  }
  await assert.rejects(composeWithDiagnostics(input, {composeBulletinLayout: async () => {throw new Error('page_requires_edit')}, measureBulletinLayout: async () => ({overflow: []})}));
});
