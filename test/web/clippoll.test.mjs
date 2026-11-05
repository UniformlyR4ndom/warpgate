// The clipboard poll loop under a virtual clock: request spacing, the
// long-poll fallback and its retry, and what gets logged.
import assert from 'node:assert/strict';
import { makeClipPoller, MIN_GAP_MS, RETRY_MS } from '../../web/app/lib/clippoll.js';
import { startClipPolling, stopClipPolling, reloadClipboard, pollClipboard } from '../../web/app/lib/clipboard.js';
import { state } from './stub-store.mjs';
import { mock, ok } from './stub-api.mjs';

let failures = 0;
async function test(name, fn) {
  try { await fn(); console.log('ok   - ' + name); }
  catch (err) { failures++; console.log('FAIL - ' + name + '\n       ' + (err && err.stack || err)); }
}

// script(i, call) returns {result, ms}: the poll's outcome and how long it took.
// Resolves with the recorded polls once `count` have run.
function runPoller(count, script) {
  let t = 0;
  const polls = [];
  const logs = [];
  return new Promise(function(resolve) {
    const poller = makeClipPoller({
      now: () => t,
      sleep: async (ms) => { t += ms; },
      log: { warn: (m) => logs.push(['warn', m]), info: (m) => logs.push(['info', m]) },
      poll: async function(opts) {
        const call = { start: t, wait: opts.wait, mode: poller.mode };
        polls.push(call);
        const s = script(polls.length - 1, call);
        t += s.ms || 0;
        if (polls.length === count) { poller.stop(); resolve({ polls, logs, poller }); }
        return s.result;
      },
    });
    poller.start();
  });
}

const OK = { result: { status: 'ok' } };
const suspectFail = (ms) => ({ result: { status: 'error', error: 'HTTP 504', suspect: true }, ms });

function gaps(polls) {
  return polls.slice(1).map((p, i) => p.start - polls[i].start);
}

await test('polls never start less than 1 s apart', async () => {
  const { polls, logs } = await runPoller(6, () => OK);
  assert.ok(polls.every((p) => p.wait), 'all are long polls');
  assert.deepEqual(gaps(polls), Array(5).fill(MIN_GAP_MS));
  assert.equal(logs.length, 0);
});

await test('after a full-length long poll the next one starts at once', async () => {
  const { polls } = await runPoller(3, () => ({ result: { status: 'ok' }, ms: 25000 }));
  assert.deepEqual(gaps(polls), [25000, 25000]);
});

await test('a long-poll failure that a plain poll does not share falls back, then recovers', async () => {
  const { polls, logs } = await runPoller(400, (i, call) => {
    if (i === 0) return suspectFail(30000);
    return OK;
  });
  assert.equal(polls[1].wait, false, 'the failure is checked by a plain poll');
  assert.equal(logs[0][0], 'warn');
  assert.match(logs[0][1], /HTTP 504 after 30\.0 s/);
  const retry = polls.findIndex((p, i) => i > 1 && p.wait);
  assert.ok(retry > 2, 'plain polls in between');
  assert.ok(polls.slice(2, retry).every((p) => !p.wait && p.mode === 'short'));
  assert.ok(polls[retry].start - polls[1].start >= RETRY_MS, 'retried only after 5 min');
  assert.ok(polls[retry].start - polls[1].start < RETRY_MS + 2 * MIN_GAP_MS);
  assert.deepEqual(logs.map((l) => l[0]), ['warn', 'info'], 'only the mode changes are logged');
  assert.ok(polls.slice(retry + 1).every((p) => p.wait), 'long polling again');
});

await test('a failed retry falls back again and logs once more', async () => {
  let retries = 0;
  const { logs, poller } = await runPoller(700, (i, call) => {
    if (i === 0) return suspectFail(1000);
    if (call.wait && call.mode === 'short' && retries++ === 0) return suspectFail(1000);
    return OK;
  });
  assert.deepEqual(logs.map((l) => l[0]), ['warn', 'warn', 'info']);
  assert.equal(poller.mode, 'long');
});

await test('a failure plain polls share is not blamed on long polling', async () => {
  const { polls, logs } = await runPoller(6, (i) => {
    if (i < 4) return { result: { status: 'error', error: 'Failed to fetch', suspect: true } };
    return OK;
  });
  assert.deepEqual(polls.map((p) => p.wait), [true, false, true, false, true, true]);
  assert.deepEqual(gaps(polls), [1000, 2000, 4000, 10000, 1000], 'errors back off, a success resets it');
  assert.equal(logs.length, 0);
});

await test('a non-suspect failure (auth, refusal) is not checked', async () => {
  const { polls, logs } = await runPoller(3, (i) =>
    i === 0 ? { result: { status: 'error', error: 'HTTP 401', suspect: false } } : OK);
  assert.deepEqual(polls.map((p) => p.wait), [true, true, true]);
  assert.equal(logs.length, 0);
});

function resetClip() {
  Object.assign(state, { clipItems: [], clipCursor: 3, clipImages: {}, clipEditId: null, clipError: null, authFailed: false });
}

await test('a local change aborts the held long poll', async () => {
  resetClip();
  let held = null;
  mock.reset(function(url, opts) {
    if (url.includes('&wait')) {
      return new Promise(function(_, reject) {
        held = opts.signal;
        opts.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
      });
    }
    return ok({ rev: 3 });
  });
  startClipPolling();
  await new Promise((r) => setTimeout(r, 0));
  assert.ok(held && !held.aborted, 'a long poll is held');
  await reloadClipboard();
  stopClipPolling();
  assert.ok(held.aborted, 'the held long poll is aborted');
  assert.equal(state.clipError, null, 'an abort is no error');
});

await test('pollClipboard classifies failures', async () => {
  resetClip();
  mock.reset(() => Promise.reject(new TypeError('Failed to fetch')));
  let r = await pollClipboard({ wait: true });
  assert.deepEqual([r.status, r.suspect], ['error', true]);
  assert.equal(state.clipError, null, 'a suspect long-poll failure awaits its check');

  mock.reset(() => Promise.reject(Object.assign(new Error('refused'), { refused: true })));
  r = await pollClipboard({ wait: true });
  assert.equal(r.suspect, false);
  assert.ok(state.clipError, 'other failures are shown at once');

  mock.reset(() => ({ ok: false, status: 504 }));
  r = await pollClipboard();
  assert.deepEqual([r.status, r.error, r.suspect], ['error', 'HTTP 504', true]);
  assert.match(state.clipError, /HTTP 504/);
});

if (failures) { console.error('\n' + failures + ' test(s) failed'); process.exit(1); }
console.log('\nall clipboard poll tests passed');
