// File browser logic for both roots (dl = download root, ul = upload
// directory): listing, navigation, and chunked/whole-file downloads over HTTP
// or WS.
import { API_BASE, state, settings } from '../store.js';
import { jsonHeaders, encodeBody, decodeResponseJSON, assertOk,
         paramEncryptEnabled, apiFetch, apiWSOpen, noteWSAck, sendWSControl, wsDecryptFor, closeWSSession } from './api.js';
import { wsNextJSON, receiveWSFrames, readBodyPieces } from './ws.js';
import { decrypt, buildTransformArgs, perRequestSecrets, resolveContentTypeOverride, snapshotTransform,
         transformBuffers, requestLimitError, newStreamCodec } from './transform.js';
import { authKeyHash } from '../store.js';
import { padIndex, triggerDownload, applyExtOverride, chunkBytesFromMB, chunkRanges, REQUEST_LIMIT_BYTES } from './util.js';
import { addTransfer, removeTransfer, makeTransferProgress, updateIfChanged } from './transfers.js';
import { makeRequestGuard } from './loadguard.js';
import { addToast } from './notices.js';

// The server fixes each route to one root.
const API_PREFIX = { dl: '/api/files/', ul: '/api/ul/' };

// One guard per root: a listing that resolves after the user has navigated
// elsewhere is dropped instead of overwriting the directory now on screen, and
// overlapping auto-refresh ticks are serialized.
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

// Reload after a context change (navigation, a local change): mark the
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

// Fetches one range and hands its decoded bytes to emit: piecewise as they
// arrive for streaming transforms, in one piece for the others.
async function fetchChunk(opts, wsSession, prefix, path, start, size, onProgress, emit) {
  var codec = newStreamCodec(opts.tx);
  var whole = [];
  function onRaw(piece) {
    onProgress(piece.byteLength);
    if (codec) return emit(codec(piece));
    whole.push(piece);
  }
  var secrets = opts.backend === 'ws'
    ? await fetchChunkWS(opts, wsSession, prefix, path, start, size, onRaw)
    : await fetchChunkHTTP(opts, prefix, path, start, size, onRaw);
  if (!codec) await emit(await decrypt(opts.tx, await new Blob(whole).arrayBuffer(), secrets.password, secrets.htmlB64Key));
}

async function fetchChunkHTTP(opts, prefix, path, start, size, onRaw) {
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
  await readBodyPieces(res, onRaw);
  return s;
}

async function fetchChunkWS(opts, wsSession, prefix, path, start, size, onRaw) {
  if (!wsSession.ws) {
    var s = perRequestSecrets(opts.tx);
    var ws = await apiWSOpen(prefix + 'ws/download');
    var encrypted = paramEncryptEnabled();
    sendWSControl(ws, {
      auth: authKeyHash.value, path: path, transform: opts.tx.type,
      args: buildTransformArgs(opts.tx, s.password, s.htmlB64Key),
      frameSize: opts.wsFrameBytes, hash: opts.verify,
    });
    var ack = noteWSAck(await wsNextJSON(ws, wsDecryptFor(encrypted)));
    if (!ack.ok) { ws.close(); throw new Error(ack.error || 'download failed'); }
    wsSession.ws = ws;
    wsSession.password = s.password;
    wsSession.htmlB64Key = s.htmlB64Key;
    wsSession.encrypted = encrypted;
    wsSession.sha256 = ack.sha256 || '';
  }

  sendWSControl(wsSession.ws, { start: start, size: size });
  var chunkAck = await wsNextJSON(wsSession.ws, wsDecryptFor(wsSession.encrypted));
  if (!chunkAck.ok) throw new Error(chunkAck.error || 'download failed');
  await receiveWSFrames(wsSession.ws, chunkAck.chunkSize, onRaw);
  return { password: wsSession.password, htmlB64Key: wsSession.htmlB64Key };
}

// The server's SHA-256 of the file at path, '' if it sends none.
async function fetchServerHash(prefix, path) {
  var encrypted = paramEncryptEnabled();
  var res = await apiFetch(API_BASE + prefix + 'list', {
    method: 'POST', headers: jsonHeaders(), body: encodeBody({ path: path, hash: true }),
  });
  assertOk(res);
  var data = await decodeResponseJSON(res, encrypted);
  return (data && data[0] && data[0].sha256) || '';
}

var READ_BACK_BYTES = 16 * 1024 * 1024;

// Hashes what was actually stored. Reading it back also detects a Blob the
// browser couldn't hold (its blob storage limit), reported as unreadable.
async function hashStored(blob, unreadable) {
  var h = sha256.create();
  try {
    for (var off = 0; off < blob.size; off += READ_BACK_BYTES) {
      h.update(new Uint8Array(await blob.slice(off, off + READ_BACK_BYTES).arrayBuffer()));
    }
  } catch(err) {
    if (err && err.name === 'NotReadableError') throw new Error(unreadable);
    throw err;
  }
  return h.hex();
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
    verify:       !chunking.enabled || chunking.reassemble,
  };
  if (!chunking.enabled && transformBuffers(opts.tx.type) && entry.size > REQUEST_LIMIT_BYTES) {
    b.error = requestLimitError(entry.name, opts.tx.type, 'turn on chunking');
    return;
  }
  var wsSession  = { ws: null, password: null, htmlB64Key: null, sha256: '' };
  var transfer   = addTransfer('download', entry.name, root);
  var onProgress = makeTransferProgress(transfer, entry.size);
  var saveName   = applyExtOverride(entry.name, settings.extOverride.ext, settings.extOverride.mode);

  var chunkBytes = chunking.enabled ? chunkBytesFromMB(chunking.chunkSize, 100) : entry.size;
  var sink = !chunking.enabled ? blobSink(saveName)
    : !chunking.reassemble ? separateChunksSink(saveName)
    : state.hasFileSystemAccess ? writableSink(saveName)
    : blobSink(saveName);
  try {
    await sink.open();
    var ranges = chunkRanges(entry.size, chunkBytes);
    var expected = opts.verify && ranges.length && opts.backend !== 'ws' ? await fetchServerHash(prefix, path) : '';
    for (var i = 0; i < ranges.length; i++) {
      var r = ranges[i];
      var decoded = 0;
      var stage = makeStager(sink.write);
      await fetchChunk(opts, wsSession, prefix, path, r.start, r.size, onProgress, function(bytes) {
        decoded += bytes.byteLength;
        return stage.push(bytes);
      });
      if (decoded !== r.size) {
        throw new Error('chunk at offset ' + r.start + ' decoded to ' + decoded + ' bytes instead of ' + r.size +
          ': the file changed on the server; download again');
      }
      await stage.drain();
      await sink.endChunk(i);
    }
    var stored = await sink.close();
    if (opts.verify && ranges.length) {
      if (opts.backend === 'ws') expected = wsSession.sha256;
      var got = await hashStored(stored, sink.unreadable);
      if (expected && got !== expected) {
        var changed = opts.backend !== 'ws' && await fetchServerHash(prefix, path).then(function(h) { return h !== expected; }, function() { return false; });
        throw new Error((changed ? 'the file changed on the server during the download' : 'the data was altered in transit') +
          ' (SHA-256 mismatch)' + (sink.keepsMismatch ? '; the saved file is corrupt' : '; nothing was saved'));
      }
    }
    await sink.commit();
    if (opts.verify) addToast(true, '"' + entry.name + '" downloaded' + (expected || !ranges.length ? ' (verified)' : ' (unverified)'));
  } catch(err) {
    sink.abort();
    if (!(err && err.name === 'AbortError')) b.error = 'Download failed: ' + (err.status ? 'HTTP ' + err.status : err);
  } finally {
    closeWSSession(wsSession);
    removeTransfer(transfer);
  }
}

var STAGE_BYTES = 4 * 1024 * 1024;

// Collects decoded pieces, which can be a few KB each, into blocks of
// STAGE_BYTES for flush.
function makeStager(flush) {
  var buf = new Uint8Array(STAGE_BYTES);
  var n = 0;
  return {
    push: async function(bytes) {
      for (var off = 0; off < bytes.length;) {
        var k = Math.min(bytes.length - off, buf.length - n);
        buf.set(bytes.subarray(off, off + k), n);
        n += k;
        off += k;
        if (n === buf.length) await this.drain();
      }
    },
    drain: async function() {
      if (!n) return;
      var full = buf.subarray(0, n);
      buf = new Uint8Array(STAGE_BYTES);
      n = 0;
      await flush(full);
    },
  };
}

// Where downloaded data goes, in blocks (write) and per chunk (endChunk). close
// returns what was stored as one file, for verification, and commit then
// saves it. Each produces exactly one file for an empty download, which needs
// no request. Data kept for one file is held as Blob parts, which the browser
// may move out of the page's memory.
function blobSink(saveName) {
  var parts = [];
  var blob = null;
  return {
    unreadable: 'the browser could not hold the whole file (its blob storage limit); turn off ' +
      'reassembly to save separate chunks, or use a browser with File System Access',
    open:     async function() {},
    write:    async function(d) { parts.push(new Blob([d])); },
    endChunk: async function() {},
    close:    async function() { blob = new Blob(parts); parts = []; return blob; },
    commit:   async function() { triggerDownload(blob, saveName); },
    abort:    function() { parts = []; blob = null; },
  };
}

function separateChunksSink(saveName) {
  var parts = [];
  var written = 0;
  return {
    open:     async function() {},
    write:    async function(d) { parts.push(new Blob([d])); },
    endChunk: async function(i) {
      triggerDownload(new Blob(parts), saveName + '.' + padIndex(i));
      parts = [];
      written++;
    },
    close:    async function() { return null; },
    commit:   async function() { if (!written) triggerDownload(new Uint8Array(0), saveName); },
    abort:    function() { parts = []; },
  };
}

function writableSink(saveName) {
  var handle = null;
  var writable = null;
  return {
    keepsMismatch: true,
    unreadable: 'the saved file could not be read back for verification',
    open: async function() {
      handle = await window.showSaveFilePicker({ suggestedName: saveName });
      writable = await handle.createWritable();
    },
    write:    function(d) { return writable.write(d); },
    endChunk: async function() {},
    close:    async function() { await writable.close(); writable = null; return handle.getFile(); },
    commit:   async function() {},
    abort:    function() { if (writable) writable.abort().catch(function() {}); },
  };
}
