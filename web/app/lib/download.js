// File browser logic for both roots (dl = download root, ul = upload
// directory): listing, navigation, and chunked/whole-file downloads over HTTP
// or WS.
import { API_BASE, state, settings } from '../store.js';
import { jsonHeaders, authHeaders, encodeBody, decodeResponseJSON, assertOk, noteAuthStatus,
         paramEncryptEnabled, wsBaseURL, sendWSControl, wsDecryptFor, closeWSSession } from './api.js';
import { wsOpen, wsNextJSON, receiveWSBinary, readBodyWithProgress } from './ws.js';
import { decrypt, buildTransformArgs, perRequestSecrets, resolveContentTypeOverride } from './transform.js';
import { authKeyHash } from '../store.js';
import { padIndex, triggerDownload, applyExtOverride } from './util.js';
import { addTransfer, removeTransfer, makeTransferProgress, updateIfChanged } from './transfers.js';

// The server fixes each route to one root.
const API_PREFIX = { dl: '/api/files/', ul: '/api/ul/' };

export async function load(root) {
  var b = state.browse[root];
  b.error = null;
  try {
    var encrypted = paramEncryptEnabled();
    var res = await fetch(API_BASE + API_PREFIX[root] + 'list', {
      method:  'POST',
      headers: jsonHeaders(),
      body:    encodeBody({ path: b.segments.join('/') }),
    });
    noteAuthStatus(res);
    if (res.status === 404 && root === 'ul' && b.segments.length === 0) {
      b.entries = [];
      b.error = 'Uploads are not enabled on this server.';
      return;
    }
    if (!res.ok) throw new Error('HTTP ' + res.status);
    var data = await decodeResponseJSON(res, encrypted);
    updateIfChanged(b, 'entries', data);
  } catch(err) {
    b.error = 'Failed to load directory (' + err.message + ')';
  }
}

export function enterDir(root, name) {
  state.browse[root].segments.push(name);
  load(root);
}

export function navigateTo(root, index) {
  var b = state.browse[root];
  b.segments = b.segments.slice(0, index + 1);
  load(root);
}

async function fetchChunk(wsSession, prefix, path, start, size, onProgress) {
  if (settings.backend === 'ws') { return fetchChunkWS(wsSession, prefix, path, start, size, onProgress); }
  return fetchChunkHTTP(prefix, path, start, size, onProgress);
}

async function fetchChunkHTTP(prefix, path, start, size, onProgress) {
  var t = settings.transform;
  var s = perRequestSecrets();
  var contentType = resolveContentTypeOverride();
  var body = {
    path: path, start: start, size: size,
    transform: t.type, args: buildTransformArgs(s.password, s.htmlB64Key),
  };
  if (contentType) body.contentType = contentType;
  var res = await fetch(API_BASE + prefix + 'download', {
    method:  'POST',
    headers: jsonHeaders(),
    body:    encodeBody(body),
  });
  assertOk(res);
  var buffer = await readBodyWithProgress(res, onProgress);
  return decrypt(buffer, s.password, s.htmlB64Key);
}

async function fetchChunkWS(wsSession, prefix, path, start, size, onProgress) {
  if (!wsSession.ws) {
    var t = settings.transform;
    var s = perRequestSecrets();
    var ws = await wsOpen(wsBaseURL() + prefix + 'ws/download');
    var encrypted = paramEncryptEnabled();
    sendWSControl(ws, {
      auth: authKeyHash.value, path: path, transform: t.type,
      args: buildTransformArgs(s.password, s.htmlB64Key),
      frameSize: settings.wsFrameSizeKB * 1024,
    });
    var ack = await wsNextJSON(ws, wsDecryptFor(encrypted));
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
  return decrypt(received.buffer, wsSession.password, wsSession.htmlB64Key);
}

export async function downloadFile(root, entry) {
  var b = state.browse[root];
  b.error = null;
  var prefix     = API_PREFIX[root];
  var path       = b.segments.concat([entry.name]).join('/');
  var chunking   = settings.chunking;
  var wsSession  = { ws: null, password: null, htmlB64Key: null };
  var transfer   = addTransfer('download', entry.name, root);
  var onProgress = makeTransferProgress(transfer, entry.size);
  var saveName   = applyExtOverride(entry.name, settings.extOverride.ext, settings.extOverride.mode);

  if (chunking.enabled) {
    var chunkBytes = chunking.chunkSize * 1024 * 1024;
    var numChunks  = Math.ceil(entry.size / chunkBytes);

    if (chunking.reassemble && state.hasFileSystemAccess) {
      try {
        var fileHandle = await window.showSaveFilePicker({ suggestedName: saveName });
        var writable   = await fileHandle.createWritable();
        try {
          for (var i = 0; i < numChunks; i++) {
            var start = i * chunkBytes;
            var size  = Math.min(chunkBytes, entry.size - start);
            var data  = await fetchChunk(wsSession, prefix, path, start, size, onProgress);
            await writable.write(data);
          }
          await writable.close();
        } catch(err) {
          writable.abort();
          throw err;
        }
      } catch(err) {
        if (err && err.name !== 'AbortError') { b.error = 'Download failed: ' + err; }
      } finally {
        closeWSSession(wsSession);
        removeTransfer(transfer);
      }
    } else {
      (async function() {
        try {
          for (var i = 0; i < numChunks; i++) {
            var start = i * chunkBytes;
            var size  = Math.min(chunkBytes, entry.size - start);
            var data  = await fetchChunk(wsSession, prefix, path, start, size, onProgress);
            triggerDownload(data, saveName + '.' + padIndex(i));
          }
        } catch(err) {
          b.error = 'Chunk failed: ' + (err.status ? 'HTTP ' + err.status : err);
        } finally {
          closeWSSession(wsSession);
          removeTransfer(transfer);
        }
      })();
    }
  } else {
    try {
      var data = await fetchChunk(wsSession, prefix, path, 0, entry.size, onProgress);
      triggerDownload(data, saveName);
    } catch(err) {
      b.error = 'Download failed: ' + (err.status ? 'HTTP ' + err.status : err);
    } finally {
      closeWSSession(wsSession);
      removeTransfer(transfer);
    }
  }
}
