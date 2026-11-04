// Framework-agnostic WebSocket + streaming helpers. See the original app.js
// comments for the rationale behind the drain-pacing and multi-frame logic.

export function wsOpen(url) {
  return new Promise(function(resolve, reject) {
    var ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
    ws.addEventListener('open', function() { resolve(ws); }, { once: true });
    ws.addEventListener('error', function() { reject(new Error('WebSocket connection failed')); }, { once: true });
  });
}

export function wsNextMessage(ws) {
  return new Promise(function(resolve, reject) {
    function cleanup() {
      ws.removeEventListener('message', onMessage);
      ws.removeEventListener('close', onClose);
      ws.removeEventListener('error', onError);
    }
    function onMessage(ev) { cleanup(); resolve(ev.data); }
    function onClose(ev)   { cleanup(); reject(new Error('WebSocket closed unexpectedly' + (ev.reason ? ': ' + ev.reason : ''))); }
    function onError()     { cleanup(); reject(new Error('WebSocket error')); }
    ws.addEventListener('message', onMessage);
    ws.addEventListener('close', onClose);
    ws.addEventListener('error', onError);
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
  var bytes = new Uint8Array(buffer);
  for (var offset = 0; offset < bytes.length; offset += frameSize) {
    await wsWaitForDrain(ws, frameSize);
    if (ws.readyState !== WebSocket.OPEN) throw new Error('WebSocket closed during send');
    var slice = bytes.slice(offset, offset + frameSize);
    ws.send(slice);
    if (onProgress) onProgress(slice.byteLength);
  }
}

export async function receiveWSBinary(ws, totalLen, onProgress) {
  var out = new Uint8Array(totalLen);
  var received = 0;
  while (received < totalLen) {
    var raw = await wsNextMessage(ws);
    if (typeof raw === 'string') {
      var msg = JSON.parse(raw);
      throw new Error(msg.error || 'download failed');
    }
    var piece = new Uint8Array(raw);
    out.set(piece, received);
    received += piece.byteLength;
    if (onProgress) onProgress(piece.byteLength);
  }
  return out;
}

// Reads a fetch Response body incrementally so callers can report progress
// as bytes arrive.
export async function readBodyWithProgress(response, onProgress) {
  if (!response.body || !response.body.getReader) {
    var buf = await response.arrayBuffer();
    if (onProgress) onProgress(buf.byteLength);
    return buf;
  }
  var reader = response.body.getReader();
  var chunks = [];
  var received = 0;
  while (true) {
    var result = await reader.read();
    if (result.done) break;
    chunks.push(result.value);
    received += result.value.byteLength;
    if (onProgress) onProgress(result.value.byteLength);
  }
  var out = new Uint8Array(received);
  var offset = 0;
  for (var i = 0; i < chunks.length; i++) {
    out.set(chunks[i], offset);
    offset += chunks[i].byteLength;
  }
  return out.buffer;
}
