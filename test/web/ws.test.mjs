// Regression tests for finding #8 (code-qa-report): wsWaitForDrain only watched
// bufferedAmount, so an interrupted connection whose buffer stayed above the
// threshold left the timer rescheduling forever — sendWSBinary never settled
// and its caller never reached cleanup. It must now reject on close/error or a
// non-OPEN state and dispose its timer/listeners.
import assert from 'node:assert/strict';
import { mock } from 'node:test';
import { wsWaitForDrain, sendWSBinary, wsOpen, wsNextMessage } from '../../web/app/lib/ws.js';

var lastSocket = null;
globalThis.WebSocket = function() { lastSocket = fakeWS(0); lastSocket.readyState = WebSocket.CONNECTING; return lastSocket; };
Object.assign(globalThis.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });

function fakeWS(bufferedAmount) {
  var listeners = { close: [], error: [], message: [] };
  return {
    readyState: WebSocket.OPEN,
    bufferedAmount: bufferedAmount || 0,
    sent: [],
    addEventListener(t, fn) { (listeners[t] = listeners[t] || []).push(fn); },
    removeEventListener(t, fn) { listeners[t] = (listeners[t] || []).filter(function(f) { return f !== fn; }); },
    send(data) { this.sent.push(data); this.bufferedAmount += data.byteLength; },
    close() { this.closed = true; },
    emit(t, ev) { (listeners[t] || []).slice().forEach(function(fn) { fn(ev || {}); }); },
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

async function openFake() {
  var p = wsOpen('ws://test');
  lastSocket.readyState = WebSocket.OPEN;
  lastSocket.emit('open');
  return p;
}

function closeFake(ws) { ws.readyState = WebSocket.CLOSED; ws.emit('close', {}); }

await test('a message arriving before the read is kept for it', async () => {
  var ws = await openFake();
  ws.emit('message', { data: 'a' });
  ws.emit('message', { data: 'b' });
  assert.equal(await wsNextMessage(ws), 'a');
  assert.equal(await wsNextMessage(ws), 'b');
});

await test('a close between two reads rejects the next read', async () => {
  var ws = await openFake();
  ws.emit('message', { data: 'ack' });
  assert.equal(await wsNextMessage(ws), 'ack');
  closeFake(ws);
  await assert.rejects(wsNextMessage(ws), /closed unexpectedly/);
});

await test('messages received before a close are still delivered', async () => {
  var ws = await openFake();
  ws.emit('message', { data: 'error ack' });
  closeFake(ws);
  assert.equal(await wsNextMessage(ws), 'error ack');
  await assert.rejects(wsNextMessage(ws), /closed unexpectedly/);
});

await test('a pending read rejects on close', async () => {
  var ws = await openFake();
  var p = wsNextMessage(ws);
  closeFake(ws);
  await assert.rejects(p, /closed unexpectedly/);
});

await test('a socket already closed when first read rejects', async () => {
  var ws = fakeWS(0);
  ws.readyState = WebSocket.CLOSED;
  await assert.rejects(wsNextMessage(ws), /closed unexpectedly/);
});

await test('a read with no message times out and closes the socket', async () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  try {
    var ws = await openFake();
    var p = wsNextMessage(ws);
    mock.timers.tick(5 * 60 * 1000 - 1);
    ws.emit('message', { data: 'just in time' });
    assert.equal(await p, 'just in time');
    p = wsNextMessage(ws);
    mock.timers.tick(5 * 60 * 1000);
    await assert.rejects(p, /timed out/);
    assert.ok(ws.closed, 'socket closed on timeout');
    await assert.rejects(wsNextMessage(ws), /timed out/);
  } finally {
    mock.timers.reset();
  }
});

if (failures) { console.error('\n' + failures + ' test(s) failed'); process.exit(1); }
console.log('\nall ws tests passed');
