// Regression tests for finding #7 (code-qa-report): a clipboard submission that
// failed partway through could, on retry, create a second note, re-upload an
// attachment that already succeeded, or fail on an already-deleted attachment;
// nothing guarded against overlapping submits. submitClip now keeps the created
// note id (retry updates, not duplicates), prunes each uploaded attachment,
// tolerates an already-deleted attachment, and guards re-entry.
//
// The network boundary (api.js) and the store are stubbed via the loader; the
// real transform/util/loadguard run.
import assert from 'node:assert/strict';
import { submitClip, editClip, resetClipDraft, resetClipSync } from '../../web/app/lib/clipboard.js';
import { state, settings } from './stub-store.mjs';
import { mock, ok, fail } from './stub-api.mjs';

let failures = 0;
async function test(name, fn) {
  try { await fn(); console.log('ok   - ' + name); }
  catch (err) { failures++; console.log('FAIL - ' + name + '\n       ' + (err && err.stack || err)); }
}

function resetState(over) {
  Object.assign(state, {
    clipItems: [], clipCursor: 0, clipImages: {},
    pendingAttachments: [], clipRemovedAttachments: [], clipCaptionEdits: {},
    clipText: '', clipTitle: '', clipEditId: null, clipError: null, clipSubmitting: false,
  }, over || {});
  settings.transform = { type: 'id', xor: { key: '' }, aesCtr: { key: '', iv: '' },
    pdf: { filename: '', password: { enabled: false } } };
}

function pendingImage(tag) {
  return { blob: new Blob([new Uint8Array([1, 2, 3])]), url: 'mock://' + tag, mime: 'image/png', caption: '' };
}

await test('a retry after an attachment failure updates the note, never duplicates it', async () => {
  resetState({ clipText: 'hello', pendingAttachments: [pendingImage('a')] });
  let attachFails = true;
  mock.reset(function(url) {
    if (url.endsWith('/api/clip/new')) return ok({ id: 7 });
    if (url.endsWith('/api/clip/attachment')) return attachFails ? fail(500) : ok({});
    if (url.endsWith('/api/clip/state')) return ok({ rev: 0 });
    return ok({});
  });

  await submitClip(); // fails on the attachment upload
  assert.equal(mock.count('/api/clip/new', 'POST'), 1);
  assert.equal(state.clipEditId, 7, 'the created note id is retained');
  assert.equal(state.pendingAttachments.length, 1, 'the failed upload is not pruned');
  assert.ok(state.clipError, 'the failure is surfaced');

  attachFails = false;
  await submitClip(); // retry
  assert.equal(mock.count('/api/clip/new', 'POST'), 1, 'no duplicate note on retry');
  assert.equal(state.pendingAttachments.length, 0, 'the attachment is pruned once it uploads');
  assert.equal(state.clipEditId, null, 'the composer is cleared on success');
  assert.equal(state.clipError, null);
});

await test('an attachment that already uploaded is not re-sent on retry', async () => {
  resetState({ clipText: 'x', pendingAttachments: [pendingImage('a'), pendingImage('b')] });
  let secondFails = true;
  let attempts = 0;
  mock.reset(function(url) {
    if (url.endsWith('/api/clip/new')) return ok({ id: 9 });
    if (url.endsWith('/api/clip/attachment')) {
      attempts += 1;
      return (secondFails && attempts === 2) ? fail(500) : ok({});
    }
    if (url.endsWith('/api/clip/state')) return ok({ rev: 0 });
    return ok({});
  });

  await submitClip(); // first image uploads, second fails
  assert.equal(state.pendingAttachments.length, 1, 'only the failed image remains');
  assert.equal(mock.count('/api/clip/attachment', 'POST'), 2);

  secondFails = false;
  await submitClip(); // retry: only the remaining image is sent
  assert.equal(mock.count('/api/clip/attachment', 'POST'), 3, 'the succeeded image is not re-uploaded');
  assert.equal(state.pendingAttachments.length, 0);
});

await test('overlapping submits do not double-create', async () => {
  resetState({ clipText: 'x' });
  mock.reset(function(url) {
    if (url.endsWith('/api/clip/new')) return ok({ id: 11 });
    if (url.endsWith('/api/clip/state')) return ok({ rev: 0 });
    return ok({});
  });
  const p1 = submitClip();
  const p2 = submitClip(); // the in-flight guard should make this a no-op
  await Promise.all([p1, p2]);
  assert.equal(mock.count('/api/clip/new', 'POST'), 1);
});

await test('a retry whose attachment was already deleted succeeds (404 tolerated)', async () => {
  resetState({ clipEditId: 5, clipText: 'x', clipRemovedAttachments: [42] });
  mock.reset(function(url) {
    if (url.endsWith('/api/clip/edit')) return ok({});
    if (url.endsWith('/api/clip/attachment/delete')) return fail(404);
    if (url.endsWith('/api/clip/state')) return ok({ rev: 0 });
    return ok({});
  });
  await submitClip();
  assert.equal(state.clipError, null, 'a 404 on an already-deleted attachment is not an error');
  assert.equal(state.clipEditId, null, 'the edit completes');
});

function trackRevokes() {
  var revoked = [];
  var orig = URL.revokeObjectURL;
  URL.revokeObjectURL = function(u) { revoked.push(u); };
  return { revoked: revoked, restore: function() { URL.revokeObjectURL = orig; } };
}

await test('cancel discards the whole draft and revokes pending images', async () => {
  resetState({ clipText: 't', clipTitle: 'x', clipEditId: 3, clipRemovedAttachments: [1],
    clipCaptionEdits: { 1: 'c' }, pendingAttachments: [pendingImage('a'), pendingImage('b')] });
  var r = trackRevokes();
  try { resetClipDraft(); } finally { r.restore(); }
  assert.deepEqual(r.revoked, ['mock://a', 'mock://b']);
  assert.equal(state.pendingAttachments.length, 0);
  assert.equal(state.clipEditId, null);
  assert.equal(state.clipText + state.clipTitle, '');
  assert.deepEqual(state.clipRemovedAttachments, []);
  assert.deepEqual(state.clipCaptionEdits, {});
});

await test('editing another note does not carry over pending images', async () => {
  resetState({ clipEditId: 1, pendingAttachments: [pendingImage('a')] });
  var r = trackRevokes();
  try {
    editClip({ id: 2, title: 'two', data: btoa('second'), attachments: [{ id: 9, caption: 'cap' }] });
  } finally { r.restore(); }
  assert.deepEqual(r.revoked, ['mock://a']);
  assert.equal(state.pendingAttachments.length, 0);
  assert.equal(state.clipEditId, 2);
  assert.equal(state.clipText, 'second');
  assert.deepEqual(state.clipCaptionEdits, { 9: 'cap' });
});

await test('a database change drops an edit draft but keeps a new-note draft', async () => {
  resetState({ clipEditId: 4, clipText: 'edit' });
  resetClipSync();
  assert.equal(state.clipEditId, null);
  assert.equal(state.clipText, '');

  resetState({ clipText: 'new note', pendingAttachments: [pendingImage('a')] });
  resetClipSync();
  assert.equal(state.clipText, 'new note');
  assert.equal(state.pendingAttachments.length, 1);
});

if (failures) { console.error('\n' + failures + ' test(s) failed'); process.exit(1); }
console.log('\nall clipboard submit tests passed');
