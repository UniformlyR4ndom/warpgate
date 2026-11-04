// Regression tests for finding #5 (code-qa-report): the chunk-size settings
// that drive the upload/download loops accepted zero, negative, empty, and
// NaN values, which stalled the loop (no forward progress) or made the
// reassembly path open and close an empty file. chunkBytesFromMB guarantees a
// positive integer byte count; positiveOr sanitizes a persisted/typed setting.
import assert from 'node:assert/strict';
import { positiveOr, chunkBytesFromMB } from '../../web/app/lib/util.js';

let failures = 0;
function test(name, fn) {
  try { fn(); console.log('ok   - ' + name); }
  catch (err) { failures++; console.log('FAIL - ' + name + '\n       ' + (err && err.message)); }
}

const MB = 1024 * 1024;

test('positiveOr keeps a valid positive number', () => {
  assert.equal(positiveOr(100, 7), 100);
  assert.equal(positiveOr(0.5, 7), 0.5);
});

test('positiveOr falls back for zero, negative, empty, and NaN', () => {
  assert.equal(positiveOr(0, 7), 7);
  assert.equal(positiveOr(-3, 7), 7);
  assert.equal(positiveOr('', 7), 7);      // a cleared number input reads as ""
  assert.equal(positiveOr('abc', 7), 7);
  assert.equal(positiveOr(NaN, 7), 7);
  assert.equal(positiveOr(Infinity, 7), 7);
});

test('chunkBytesFromMB returns a positive integer byte count for valid input', () => {
  assert.equal(chunkBytesFromMB(100, 100), 100 * MB);
  assert.equal(chunkBytesFromMB(1, 100), 1 * MB);
  assert.equal(Number.isInteger(chunkBytesFromMB(0.3, 100)), true);
});

// The dangerous inputs: each must yield the default, never 0 or negative, so
// the loop always advances.
test('chunkBytesFromMB never returns a non-positive byte count', () => {
  for (const bad of [0, -5, '', 'x', NaN, null, undefined, 1e-9]) {
    const got = chunkBytesFromMB(bad, 100);
    assert.equal(got, 100 * MB, 'bad input ' + String(bad) + ' must fall back to the default');
    assert.ok(got >= 1);
  }
});

// Mirror the loop's forward-progress invariant: with a sanitized chunk size,
// Math.min(offset + chunkBytes, len) strictly advances until done.
test('a sanitized chunk size guarantees the chunk loop advances', () => {
  const chunkBytes = chunkBytesFromMB(0, 100); // worst-case setting
  const len = 5 * MB + 123;
  let offset = 0, iterations = 0;
  while (offset < len) {
    const end = Math.min(offset + chunkBytes, len);
    assert.ok(end > offset, 'each iteration must advance');
    offset = end;
    if (++iterations > 1000) throw new Error('loop did not terminate');
  }
  assert.equal(offset, len);
});

if (failures) { console.error('\n' + failures + ' test(s) failed'); process.exit(1); }
console.log('\nall chunk-size tests passed');
