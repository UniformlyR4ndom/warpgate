// Regression tests for finding #8 (code-qa-report): wsWaitForDrain only watched
// bufferedAmount, so an interrupted connection whose buffer stayed above the
// threshold left the timer rescheduling forever — sendWSBinary never settled
// and its caller never reached cleanup. It must now reject on close/error or a
// non-OPEN state and dispose its timer/listeners.
import assert from 'node:assert/strict';
import { wsWaitForDrain, sendWSBinary } from '../../web/app/lib/ws.js';

globalThis.WebSocket = { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 };

function fakeWS(bufferedAmount) {
  var listeners = { close: [], error: [], message: [] };
  return {
    readyState: WebSocket.OPEN,
    bufferedAmount: bufferedAmount || 0,
    sent: [],
    addEventListener(t, fn) { listeners[t].push(fn); },
    removeEventListener(t, fn) { listeners[t] = listeners[t].filter(function(f) { return f !== fn; }); },
    send(data) { this.sent.push(data); this.bufferedAmount += data.byteLength; },
    emit(t, ev) { listeners[t].slice().forEach(function(fn) { fn(ev || {}); }); },
    listenerCount() { return listeners.close.length + listeners.error.length + listeners.message.length; },
  };
}

let failures = 0;
async function test(name, fn) {
  try { await fn(); console.log('ok   - ' + name); }
  catch (err) { failures++; console.log('FAIL - ' + name + '\n       ' + (err && err.message)); }
}

await test('resolves once the buffer drains, and cleans up', async () => {
  var ws = fakeWS(1000);
  var p = wsWaitForDrain(ws, 10);
  setTimeout(function() { ws.bufferedAmount = 5; }, 5);
  await p;
  assert.equal(ws.listenerCount(), 0, 'listeners removed on resolve');
});

// The report's scenario: buffer stays above the threshold and the connection
// closes. The old code looped forever; this must reject (and not hang).
await test('rejects when the connection closes while the buffer stays full', async () => {
  var ws = fakeWS(1000);
  var p = wsWaitForDrain(ws, 10);
  setTimeout(function() { ws.readyState = WebSocket.CLOSED; ws.emit('close', { reason: 'boom' }); }, 5);
  await assert.rejects(p, /closed during send/);
  assert.equal(ws.listenerCount(), 0, 'listeners removed on reject');
});

await test('rejects immediately if the socket is already not OPEN', async () => {
  var ws = fakeWS(0); // buffer is fine, but the socket is gone
  ws.readyState = WebSocket.CLOSED;
  await assert.rejects(wsWaitForDrain(ws, 10), /closed during send/);
  assert.equal(ws.listenerCount(), 0);
});

await test('rejects on an error event', async () => {
  var ws = fakeWS(1000);
  var p = wsWaitForDrain(ws, 10);
  setTimeout(function() { ws.emit('error', {}); }, 5);
  await assert.rejects(p, /closed during send/);
  assert.equal(ws.listenerCount(), 0);
});

// sendWSBinary must stop and propagate when the connection drops mid-send,
// instead of hanging, so the caller's finally (ws.close, remove transfer) runs.
await test('sendWSBinary propagates a mid-send close and stops sending', async () => {
  var ws = fakeWS(0);
  var p = sendWSBinary(ws, new Uint8Array(100).buffer, 10, null); // 10 frames
  setTimeout(function() { ws.readyState = WebSocket.CLOSED; ws.emit('close', {}); }, 5);
  await assert.rejects(p, /closed during send/);
  assert.ok(ws.sent.length < 10, 'did not send all frames after the close');
  assert.equal(ws.listenerCount(), 0, 'no leaked drain listeners');
});

await test('sendWSBinary sends every frame on a healthy connection', async () => {
  var ws = fakeWS(0);
  ws.send = function(d) { this.sent.push(d); }; // never accumulates buffer
  await sendWSBinary(ws, new Uint8Array(25).buffer, 10, null);
  assert.equal(ws.sent.length, 3, '25 bytes in 10-byte frames = 3 frames');
  assert.equal(ws.listenerCount(), 0);
});

if (failures) { console.error('\n' + failures + ' test(s) failed'); process.exit(1); }
console.log('\nall ws drain tests passed');
