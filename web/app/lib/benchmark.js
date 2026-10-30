// Benchmark tab logic: drives /api/sim/* (no transform, no filesystem) to
// probe which transport/chunk/frame size gets through a proxy. Both
// directions verify a SHA-256 hash, so corruption shows up as a failed run.
import { API_BASE, state, settings, authKeyHash } from '../store.js';
import { authHeaders, jsonHeaders, encodeBody, decodeResponseJSON, assertOk,
         paramEncryptEnabled, wsBaseURL, sendWSControl, wsDecryptFor, closeWSSession } from './api.js';
import { wsOpen, wsNextJSON, sendWSBinary, receiveWSBinary } from './ws.js';
import { sha256Hex } from './util.js';

const NO_SUBTLE = 'SHA-256 hashing requires a secure context (HTTPS or localhost)';

// Mirrors simReader server-side: "<totalSize><space>" followed by 0xFF
// padding out to totalSize.
function buildExpectedSimFile(totalSize) {
  var buf = new Uint8Array(totalSize);
  buf.fill(0xff);
  buf.set(new TextEncoder().encode(String(totalSize) + ' '), 0);
  return buf.buffer;
}

function clampBenchmarkSettings() {
  var b = settings.benchmark;
  if (!(b.sizeMB >= 1))   { b.sizeMB = 1; }
  if (b.sizeMB > 1024)    { b.sizeMB = 1024; }
  if (!(b.chunkSizeMB >= 1)) { b.chunkSizeMB = 1; }
  if (b.chunkSizeMB > b.sizeMB) { b.chunkSizeMB = b.sizeMB; }
}

function addBenchmarkResult(result) {
  result.id = Date.now() + '-' + Math.random().toString(36).slice(2);
  state.benchmarkResults.unshift(result);
}

export function clearBenchmarkResults() {
  state.benchmarkResults = [];
}

function formatBenchmarkError(err) {
  return err.status ? 'HTTP ' + err.status : String(err.message || err);
}

function makeBenchmarkProgress(totalSize, hasSteps) {
  var bytesDone = 0;
  return function(n) {
    bytesDone += n;
    if (hasSteps) state.benchmarkProgress = Math.min(1, bytesDone / totalSize);
  };
}

function resetBenchmarkFlags() {
  state.benchmarkRunning = false;
  state.benchmarkProgress = null;
  state.benchmarkPreparing = false;
  state.benchmarkPrepProgress = null;
}

async function fetchSimChunkHTTP(totalSize, start, size, onProgress) {
  var res = await fetch(API_BASE + '/api/sim/download', {
    method:  'POST',
    headers: jsonHeaders(),
    body:    encodeBody({ totalSize: totalSize, start: start, size: size }),
  });
  assertOk(res);
  var buffer = await res.arrayBuffer();
  if (buffer.byteLength !== size) {
    throw new Error('expected ' + size + ' bytes, got ' + buffer.byteLength);
  }
  if (onProgress) onProgress(buffer.byteLength);
  return new Uint8Array(buffer);
}

async function fetchSimChunkWS(wsSession, totalSize, start, size, onProgress) {
  if (!wsSession.ws) {
    var ws = await wsOpen(wsBaseURL() + '/api/sim/ws/download');
    var encrypted = paramEncryptEnabled();
    sendWSControl(ws, { auth: authKeyHash.value, totalSize: totalSize, frameSize: settings.wsFrameSizeKB * 1024 });
    var ack = await wsNextJSON(ws, wsDecryptFor(encrypted));
    if (!ack.ok) { ws.close(); throw new Error(ack.error || 'download failed'); }
    wsSession.ws = ws;
    wsSession.encrypted = encrypted;
  }
  sendWSControl(wsSession.ws, { start: start, size: size });
  return receiveWSBinary(wsSession.ws, size, onProgress);
}

function fetchSimChunk(wsSession, totalSize, start, size, onProgress) {
  if (settings.backend === 'ws') { return fetchSimChunkWS(wsSession, totalSize, start, size, onProgress); }
  return fetchSimChunkHTTP(totalSize, start, size, onProgress);
}

export async function runDownloadBenchmark() {
  if (state.benchmarkRunning) return;
  clampBenchmarkSettings();
  state.benchmarkRunning = true;

  var backend   = settings.backend;
  var totalSize = settings.benchmark.sizeMB * 1024 * 1024;
  var chunkSize = Math.min(settings.benchmark.chunkSizeMB * 1024 * 1024, totalSize);
  var frameSize = backend === 'ws' ? settings.wsFrameSizeKB * 1024 : null;
  var base = { direction: 'download', backend: backend, sizeBytes: totalSize, frameBytes: frameSize, chunkBytes: chunkSize };

  if (!window.crypto.subtle) {
    addBenchmarkResult(Object.assign(base, { durationMs: 0, ok: false, error: NO_SUBTLE }));
    resetBenchmarkFlags();
    return;
  }

  var wsSession = { ws: null };
  var startTime = null; // set once the network loop actually starts

  // A bar is only shown when the run has more than one real network step.
  var hasSteps = (backend === 'ws') ? (chunkSize < totalSize || frameSize < totalSize) : (chunkSize < totalSize);
  state.benchmarkProgress = hasSteps ? 0 : null;
  var onProgress = makeBenchmarkProgress(totalSize, hasSteps);

  try {
    // Computed before the timer starts, so it isn't counted as transfer time.
    state.benchmarkPreparing = true;
    var expectedHash = await sha256Hex(buildExpectedSimFile(totalSize));
    state.benchmarkPreparing = false;

    var received = new Uint8Array(totalSize);
    startTime = performance.now();
    for (var offset = 0; offset < totalSize; offset += chunkSize) {
      var size  = Math.min(chunkSize, totalSize - offset);
      received.set(await fetchSimChunk(wsSession, totalSize, offset, size, onProgress), offset);
    }
    var durationMs = performance.now() - startTime;

    var hashMatch = (await sha256Hex(received.buffer)) === expectedHash;
    addBenchmarkResult(Object.assign(base, {
      durationMs: durationMs, ok: hashMatch,
      error: hashMatch ? null : 'hash mismatch (data corrupted in transit)',
    }));
  } catch(err) {
    addBenchmarkResult(Object.assign(base, {
      durationMs: startTime !== null ? performance.now() - startTime : 0,
      ok: false, error: formatBenchmarkError(err),
    }));
  } finally {
    closeWSSession(wsSession);
    resetBenchmarkFlags();
  }
}

// crypto.getRandomValues caps one call at 65536 bytes; yield periodically so
// filling a large buffer doesn't freeze the tab.
async function generateRandomBuffer(totalSize, onProgress) {
  var buf = new Uint8Array(totalSize);
  var CHUNK = 65536;
  var lastYield = performance.now();
  for (var offset = 0; offset < totalSize; offset += CHUNK) {
    var len = Math.min(CHUNK, totalSize - offset);
    window.crypto.getRandomValues(buf.subarray(offset, offset + len));
    if (onProgress) onProgress(len);
    if (performance.now() - lastYield > 16) {
      await new Promise(function(resolve) { setTimeout(resolve, 0); });
      lastYield = performance.now();
    }
  }
  return buf.buffer;
}

async function uploadSimHTTP(buffer) {
  var fd = new FormData();
  fd.append('file', new Blob([buffer]), 'bench.bin');
  var res = await fetch(API_BASE + '/api/sim/upload', { method: 'POST', headers: authHeaders(), body: fd });
  assertOk(res);
  return (await res.json()).hash;
}

// Same continuation-key protocol as a chunked file upload, exercising the
// server's chunked-upload session machinery.
async function uploadSimHTTPChunked(buffer, chunkSize, onProgress) {
  var bytes = new Uint8Array(buffer);
  var encrypted = paramEncryptEnabled();
  var continuationKey = '';
  var offset = 0;
  var done = false;
  var hash = null;
  while (!done) {
    var end   = Math.min(offset + chunkSize, bytes.length);
    var slice = bytes.subarray(offset, end);
    done      = end >= bytes.length;

    var spec = encodeBody({ 'continuation-key': continuationKey, 'ul-done': done });
    var fd = new FormData();
    fd.append('transform', new Blob([spec], { type: encrypted ? 'text/plain' : 'application/json' }), 'transform.json');
    fd.append('file', new Blob([slice]), 'bench.bin');
    var res = await fetch(API_BASE + '/api/sim/upload', { method: 'POST', headers: authHeaders(), body: fd });
    assertOk(res);
    var data = await decodeResponseJSON(res, encrypted);
    continuationKey = data['continuation-key'] || '';
    if (data.hash) hash = data.hash;

    if (onProgress) onProgress(slice.byteLength);
    offset = end;
  }
  return hash;
}

async function uploadSimWS(buffer, onProgress) {
  var frameSize = settings.wsFrameSizeKB * 1024;
  var decrypt   = wsDecryptFor(paramEncryptEnabled());
  var ws = await wsOpen(wsBaseURL() + '/api/sim/ws/upload');
  try {
    sendWSControl(ws, { auth: authKeyHash.value, frameSize: frameSize });
    var ack = await wsNextJSON(ws, decrypt);
    if (!ack.ok) throw new Error(ack.error || 'upload failed');

    await sendWSBinary(ws, buffer, frameSize, onProgress);
    sendWSControl(ws, { done: true });

    var final = await wsNextJSON(ws, decrypt);
    if (!final.ok) throw new Error(final.error || 'upload failed');
    return final.hash;
  } finally {
    ws.close();
  }
}

export async function runUploadBenchmark() {
  if (state.benchmarkRunning) return;
  clampBenchmarkSettings();
  state.benchmarkRunning = true;

  var backend   = settings.backend;
  var totalSize = settings.benchmark.sizeMB * 1024 * 1024;
  var frameSize = backend === 'ws' ? settings.wsFrameSizeKB * 1024 : null;
  // Chunked upload (the continuation-key session protocol) is HTTP-only.
  var chunkSize = backend === 'http' ? Math.min(settings.benchmark.chunkSizeMB * 1024 * 1024, totalSize) : null;
  var base = { direction: 'upload', backend: backend, sizeBytes: totalSize, frameBytes: frameSize };
  if (chunkSize) base.chunkBytes = chunkSize;

  if (!window.crypto.subtle) {
    addBenchmarkResult(Object.assign(base, { durationMs: 0, ok: false, error: NO_SUBTLE }));
    resetBenchmarkFlags();
    return;
  }

  var startTime = null; // set once the network operation actually starts

  // One send per WS frame, or one POST per HTTP chunk; a single-request HTTP
  // upload has no progress event.
  var hasSteps = (backend === 'ws') ? (frameSize < totalSize) : (chunkSize < totalSize);
  state.benchmarkProgress = hasSteps ? 0 : null;
  var onProgress = makeBenchmarkProgress(totalSize, hasSteps);

  try {
    // Computed before the timer starts, so it isn't counted as transfer time.
    state.benchmarkPreparing = true;
    state.benchmarkPrepProgress = 0;
    var prepBytesDone = 0;
    var buffer = await generateRandomBuffer(totalSize, function(n) {
      prepBytesDone += n;
      state.benchmarkPrepProgress = Math.min(1, prepBytesDone / totalSize);
    });
    state.benchmarkPrepProgress = null; // hashing has no incremental progress API
    var expectedHash = await sha256Hex(buffer);
    state.benchmarkPreparing = false;

    startTime = performance.now();
    var hash;
    if (backend === 'ws') {
      hash = await uploadSimWS(buffer, onProgress);
    } else if (chunkSize < totalSize) {
      hash = await uploadSimHTTPChunked(buffer, chunkSize, onProgress);
    } else {
      hash = await uploadSimHTTP(buffer);
    }
    var durationMs = performance.now() - startTime;

    var hashMatch = hash === expectedHash;
    addBenchmarkResult(Object.assign(base, {
      durationMs: durationMs, ok: hashMatch,
      error: hashMatch ? null : 'hash mismatch (data corrupted in transit)',
    }));
  } catch(err) {
    addBenchmarkResult(Object.assign(base, {
      durationMs: startTime !== null ? performance.now() - startTime : 0,
      ok: false, error: formatBenchmarkError(err),
    }));
  } finally {
    resetBenchmarkFlags();
  }
}
