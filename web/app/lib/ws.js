// A pending read fails after this long without a message: a connection that a
// middlebox silently drops never delivers a close event.
var READ_IDLE_MS = 5 * 60 * 1000;

var readers = new WeakMap();

// Listeners are attached once per socket, so a message or close arriving
// between two reads is kept for the next one, however long the caller takes.
function readerFor(ws) {
  var r = readers.get(ws);
  if (r) return r;
  r = { queue: [], waiters: [], failure: null, fail: fail };
  function fail(err) {
    if (r.failure) return;
    r.failure = err;
    r.waiters.splice(0).forEach(function(w) { w.reject(err); });
  }
  ws.addEventListener('message', function(ev) {
    var w = r.waiters.shift();
    if (w) w.resolve(ev.data); else r.queue.push(ev.data);
  });
  ws.addEventListener('close', function(ev) {
    fail(new Error('WebSocket closed unexpectedly' + (ev.reason ? ': ' + ev.reason : '')));
  });
  ws.addEventListener('error', function() { fail(new Error('WebSocket error')); });
  if (ws.readyState === WebSocket.CLOSING || ws.readyState === WebSocket.CLOSED) {
    fail(new Error('WebSocket closed unexpectedly'));
  }
  readers.set(ws, r);
  return r;
}

export function wsOpen(url) {
  return new Promise(function(resolve, reject) {
    var ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
    readerFor(ws);
    ws.addEventListener('open', function() { resolve(ws); }, { once: true });
    ws.addEventListener('error', function() { reject(new Error('WebSocket connection failed')); }, { once: true });
  });
}

export function wsNextMessage(ws) {
  var r = readerFor(ws);
  if (r.queue.length) return Promise.resolve(r.queue.shift());
  if (r.failure) return Promise.reject(r.failure);
  return new Promise(function(resolve, reject) {
    var timer = setTimeout(function() {
      r.fail(new Error('WebSocket timed out waiting for the server'));
      ws.close();
    }, READ_IDLE_MS);
    var waiter = {
      resolve: function(v) { clearTimeout(timer); resolve(v); },
      reject:  function(e) { clearTimeout(timer); reject(e); },
    };
    r.waiters.push(waiter);
  });
}

// decrypt is null for a plaintext connection, or a decrypt callback for one
// whose handshake was encrypted — decided once per connection by the caller.
export async function wsNextJSON(ws, decrypt) {
  var raw = await wsNextMessage(ws);
  if (typeof raw !== 'string') throw new Error('expected JSON control message, got binary');
  return decrypt ? decrypt(raw) : JSON.parse(raw);
}

// Pace sends to real transport throughput and bound unsent data in memory.
// Resolving via a real timer (not just a microtask) lets the browser paint
// between sends, so progress bars animate instead of jumping 0→100%.
// Rejects if the connection closes, errors, or is no longer OPEN, so a dropped
// connection can't leave the poll rescheduling forever (the send loop would
// never settle and its caller never reach cleanup).
export function wsWaitForDrain(ws, maxBuffered) {
  return new Promise(function(resolve, reject) {
    var timer = null;
    function cleanup() {
      if (timer !== null) { clearTimeout(timer); timer = null; }
      ws.removeEventListener('close', onFail);
      ws.removeEventListener('error', onFail);
    }
    function onFail() { cleanup(); reject(new Error('WebSocket closed during send')); }
    function check() {
      if (ws.readyState !== WebSocket.OPEN) { onFail(); return; }
      if (ws.bufferedAmount <= maxBuffered) { cleanup(); resolve(); return; }
      timer = setTimeout(check, 0);
    }
    ws.addEventListener('close', onFail);
    ws.addEventListener('error', onFail);
    timer = setTimeout(check, 0);
  });
}

export async function sendWSBinary(ws, buffer, frameSize, onProgress) {
  var bytes = buffer instanceof Uint8Array ? buffer : new Uint8Array(buffer);
  for (var offset = 0; offset < bytes.length; offset += frameSize) {
    await wsWaitForDrain(ws, frameSize);
    if (ws.readyState !== WebSocket.OPEN) throw new Error('WebSocket closed during send');
    var slice = bytes.slice(offset, offset + frameSize);
    ws.send(slice);
    if (onProgress) onProgress(slice.byteLength);
  }
}

// Hands each binary message of totalLen bytes in all to onFrame, in order.
export async function receiveWSFrames(ws, totalLen, onFrame) {
  var received = 0;
  while (received < totalLen) {
    var raw = await wsNextMessage(ws);
    if (typeof raw === 'string') {
      var msg = JSON.parse(raw);
      throw new Error(msg.error || 'download failed');
    }
    var piece = new Uint8Array(raw);
    received += piece.byteLength;
    await onFrame(piece);
  }
}

export async function receiveWSBinary(ws, totalLen, onProgress) {
  var out = new Uint8Array(totalLen);
  var received = 0;
  await receiveWSFrames(ws, totalLen, function(piece) {
    out.set(piece, received);
    received += piece.byteLength;
    if (onProgress) onProgress(piece.byteLength);
  });
  return out;
}

// Hands a fetch Response body to onPiece as it arrives.
export async function readBodyPieces(response, onPiece) {
  if (!response.body || !response.body.getReader) {
    await onPiece(new Uint8Array(await response.arrayBuffer()));
    return;
  }
  var reader = response.body.getReader();
  while (true) {
    var result = await reader.read();
    if (result.done) break;
    await onPiece(result.value);
  }
}
