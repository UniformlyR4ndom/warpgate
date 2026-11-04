// Regression tests for finding #6 (code-qa-report): a stale async read must not
// clobber current UI state. makeRequestGuard is the shared mechanism used by the
// directory listing and the clipboard poll; these cover its invariants in terms
// of the two reported scenarios.
import assert from 'node:assert/strict';
import { makeRequestGuard } from '../../web/app/lib/loadguard.js';

let failures = 0;
function test(name, fn) {
  try { fn(); console.log('ok   - ' + name); }
  catch (err) { failures++; console.log('FAIL - ' + name + '\n       ' + (err && err.message)); }
}

// Report scenario A (directory): a slow listing for path A resolves after the
// user navigated to B; it must be dropped, not applied over B's entries.
test('a response whose context changed while in flight is not fresh', () => {
  const g = makeRequestGuard();
  const a = g.start();           // load path A
  assert.notEqual(a, null);
  g.bump();                      // user navigates to path B
  const b = g.start();           // load path B proceeds despite A still "running"
  assert.notEqual(b, null);
  assert.equal(g.fresh(a), false, 'the stale path-A response must be dropped');
  assert.equal(g.fresh(b), true, 'the current path-B response applies');
});

// Report scenario B (clipboard): overlapping auto-refresh polls are serialized,
// so a delayed one can't overwrite newer notes or rewind the cursor.
test('a same-context request is skipped while one is already in flight', () => {
  const g = makeRequestGuard();
  const first = g.start();
  assert.notEqual(first, null);
  assert.equal(g.start(), null, 'the overlapping tick is skipped');
  g.done(first);
  assert.notEqual(g.start(), null, 'after completion a fresh request proceeds');
});

// A stale request completing must not release the slot owned by the newer one.
test('done() from a superseded request does not release the current slot', () => {
  const g = makeRequestGuard();
  const a = g.start();
  g.bump();
  const b = g.start();           // b now owns the slot
  g.done(a);                     // stale A finishing must be a no-op
  assert.equal(g.start(), null, 'b still holds the slot, so a new start is skipped');
  g.done(b);
  assert.notEqual(g.start(), null, 'releasing b frees the slot');
});

// bump() with nothing in flight just advances the context; the next request is
// fresh and proceeds.
test('bump() with no request in flight still lets the next request proceed', () => {
  const g = makeRequestGuard();
  g.bump();
  const t = g.start();
  assert.notEqual(t, null);
  assert.equal(g.fresh(t), true);
});

if (failures) { console.error('\n' + failures + ' test(s) failed'); process.exit(1); }
console.log('\nall loadguard tests passed');
