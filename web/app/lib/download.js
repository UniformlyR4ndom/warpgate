// File browser logic for both roots (dl = download root, ul = upload
// directory): listing, navigation, and chunked/whole-file downloads over HTTP
// or WS.
import { API_BASE, state, settings } from '../store.js';
import { jsonHeaders, encodeBody, decodeResponseJSON, assertOk,
         paramEncryptEnabled, apiFetch, apiWSOpen, noteWSAck, sendWSControl, wsDecryptFor, closeWSSession } from './api.js';
import { wsNextJSON, receiveWSBinary, readBodyWithProgress } from './ws.js';
import { decrypt, buildTransformArgs, perRequestSecrets, resolveContentTypeOverride, snapshotTransform } from './transform.js';
import { authKeyHash } from '../store.js';
import { padIndex, triggerDownload, applyExtOverride, chunkBytesFromMB, chunkRanges } from './util.js';
import { addTransfer, removeTransfer, makeTransferProgress, updateIfChanged } from './transfers.js';
import { makeRequestGuard } from './loadguard.js';

// The server fixes each route to one root.
const API_PREFIX = { dl: '/api/files/', ul: '/api/ul/' };

// One guard per root: a listing that resolves after the user has navigated
// elsewhere (or changed the auth key) is dropped instead of overwriting the
// directory now on screen, and overlapping auto-refresh ticks are serialized.
const loadGuard = { dl: makeRequestGuard(), ul: makeRequestGuard() };

export async function load(root) {
  var g = loadGuard[root];
  var token = g.start();
  if (token === null) return; // a listing for this path is already in flight
  var b = state.browse[root];
  try {
    var encrypted = paramEncryptEnabled();
    var res = await apiFetch(API_BASE + API_PREFIX[root] + 'list', {
      method:  'POST',
      headers: jsonHeaders(),
      body:    encodeBody({ path: b.segments.join('/') }),
    });
    if (!g.fresh(token)) return;
    if (res.status === 404 && root === 'ul' && b.segments.length === 0) {
      b.entries = [];
      b.error = 'Uploads are not enabled on this server.';
      return;
    }
    if (!res.ok) throw new Error('HTTP ' + res.status);
    var data = await decodeResponseJSON(res, encrypted);
    if (!g.fresh(token)) return;
    updateIfChanged(b, 'entries', data);
    b.error = null;
  } catch(err) {
    if (!g.fresh(token)) return;
    b.error = 'Failed to load directory (' + err.message + ')';
  } finally {
    g.done(token);
  }
}

// Reload after a context change (navigation, auth key / DB change): mark the
// new context so any in-flight listing for the old one is dropped and the
// serialize check doesn't skip this reload.
export function reload(root) {
  loadGuard[root].bump();
  load(root);
}

export function enterDir(root, name) {
  state.browse[root].segments.push(name);
  reload(root);
}

export function navigateTo(root, index) {
  var b = state.browse[root];
  b.segments = b.segments.slice(0, index + 1);
  reload(root);
}

async function fetchChunk(opts, wsSession, prefix, path, start, size, onProgress) {
  if (opts.backend === 'ws') { return fetchChunkWS(opts, wsSession, prefix, path, start, size, onProgress); }
  return fetchChunkHTTP(opts, prefix, path, start, size, onProgress);
}

async function fetchChunkHTTP(opts, prefix, path, start, size, onProgress) {
  var s = perRequestSecrets(opts.tx);
  var body = {
    path: path, start: start, size: size,
    transform: opts.tx.type, args: buildTransformArgs(opts.tx, s.password, s.htmlB64Key),
  };
  if (opts.contentType) body.contentType = opts.contentType;
  var res = await apiFetch(API_BASE + prefix + 'download', {
    method:  'POST',
    headers: jsonHeaders(),
    body:    encodeBody(body),
  }, true);
  assertOk(res);
  var buffer = await readBodyWithProgress(res, onProgress);
  return decrypt(opts.tx, buffer, s.password, s.htmlB64Key);
}

async function fetchChunkWS(opts, wsSession, prefix, path, start, size, onProgress) {
  if (!wsSession.ws) {
    var s = perRequestSecrets(opts.tx);
    var ws = await apiWSOpen(prefix + 'ws/download');
    var encrypted = paramEncryptEnabled();
    sendWSControl(ws, {
      auth: authKeyHash.value, path: path, transform: opts.tx.type,
      args: buildTransformArgs(opts.tx, s.password, s.htmlB64Key),
      frameSize: opts.wsFrameBytes,
    });
    var ack = noteWSAck(await wsNextJSON(ws, wsDecryptFor(encrypted)));
    if (!ack.ok) { ws.close(); throw new Error(ack.error || 'download failed'); }
    wsSession.ws = ws;
    wsSession.password = s.password;
    wsSession.htmlB64Key = s.htmlB64Key;
    wsSession.encrypted = encrypted;
  }

  sendWSControl(wsSession.ws, { start: start, size: size });
  var chunkAck = await wsNextJSON(wsSession.ws, wsDecryptFor(wsSession.encrypted));
  if (!chunkAck.ok) throw new Error(chunkAck.error || 'download failed');
  var received = await receiveWSBinary(wsSession.ws, chunkAck.chunkSize, onProgress);
  return decrypt(opts.tx, received.buffer, wsSession.password, wsSession.htmlB64Key);
}

export async function downloadFile(root, entry) {
  var b = state.browse[root];
  b.error = null;
  var prefix     = API_PREFIX[root];
  var path       = b.segments.concat([entry.name]).join('/');
  var chunking   = settings.chunking;
  // Captured once so a settings change mid-transfer can't retarget transport
  // or decode a later chunk with a mismatched transform.
  var opts       = {
    tx:           snapshotTransform(),
    backend:      settings.backend,
    wsFrameBytes: settings.wsFrameSizeKB * 1024,
    contentType:  resolveContentTypeOverride(),
  };
  var wsSession  = { ws: null, password: null, htmlB64Key: null };
  var transfer   = addTransfer('download', entry.name, root);
  var onProgress = makeTransferProgress(transfer, entry.size);
  var saveName   = applyExtOverride(entry.name, settings.extOverride.ext, settings.extOverride.mode);

  var chunkBytes = chunking.enabled ? chunkBytesFromMB(chunking.chunkSize, 100) : entry.size;
  var sink = !chunking.enabled ? wholeFileSink(saveName)
    : chunking.reassemble && state.hasFileSystemAccess ? writableSink(saveName)
    : separateChunksSink(saveName);
  try {
    await sink.open();
    var ranges = chunkRanges(entry.size, chunkBytes);
    for (var i = 0; i < ranges.length; i++) {
      await sink.write(await fetchChunk(opts, wsSession, prefix, path, ranges[i].start, ranges[i].size, onProgress), i);
    }
    await sink.close();
  } catch(err) {
    sink.abort();
    if (!(err && err.name === 'AbortError')) b.error = 'Download failed: ' + (err.status ? 'HTTP ' + err.status : err);
  } finally {
    closeWSSession(wsSession);
    removeTransfer(transfer);
  }
}

// Where downloaded chunks go. Each produces exactly one file for an empty
// download, which needs no request at all.
function wholeFileSink(saveName) {
  var data = new Uint8Array(0);
  return {
    open:  async function() {},
    write: async function(d) { data = d; },
    close: async function() { triggerDownload(data, saveName); },
    abort: function() {},
  };
}

function separateChunksSink(saveName) {
  var written = 0;
  return {
    open:  async function() {},
    write: async function(d, i) { triggerDownload(d, saveName + '.' + padIndex(i)); written++; },
    close: async function() { if (!written) triggerDownload(new Uint8Array(0), saveName); },
    abort: function() {},
  };
}

function writableSink(saveName) {
  var writable = null;
  return {
    open: async function() {
      var handle = await window.showSaveFilePicker({ suggestedName: saveName });
      writable = await handle.createWritable();
    },
    write: function(d) { return writable.write(d); },
    close: function() { return writable.close(); },
    abort: function() { if (writable) writable.abort().catch(function() {}); },
  };
}
