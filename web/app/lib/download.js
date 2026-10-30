// Download tab logic: directory listing, navigation, and chunked/whole-file
// downloads over HTTP or WS.
import { API_BASE, state, settings } from '../store.js';
import { jsonHeaders, authHeaders, encodeBody, decodeResponseJSON, assertOk, noteAuthStatus,
         paramEncryptEnabled, wsBaseURL, sendWSControl, wsDecryptFor, closeWSSession } from './api.js';
import { wsOpen, wsNextJSON, receiveWSBinary, readBodyWithProgress } from './ws.js';
import { decrypt, buildTransformArgs, perRequestSecrets, resolveContentTypeOverride } from './transform.js';
import { authKeyHash } from '../store.js';
import { padIndex, triggerDownload } from './util.js';
import { addTransfer, removeTransfer, makeTransferProgress, updateIfChanged } from './transfers.js';

export function currentPath() {
  return state.pathSegments.join('/');
}

export async function load() {
  state.error = null;
  try {
    var encrypted = paramEncryptEnabled();
    var res = await fetch(API_BASE + '/api/files/list', {
      method:  'POST',
      headers: jsonHeaders(),
      body:    encodeBody({ path: currentPath() }),
    });
    noteAuthStatus(res);
    if (!res.ok) throw new Error('HTTP ' + res.status);
    var data = await decodeResponseJSON(res, encrypted);
    updateIfChanged('entries', data);
  } catch(err) {
    state.error = 'Failed to load directory (' + err.message + ')';
  }
}

export function enterDir(name) {
  state.pathSegments.push(name);
  load();
}

export function navigateTo(index) {
  state.pathSegments = state.pathSegments.slice(0, index + 1);
  load();
}

async function fetchChunk(wsSession, path, start, size, onProgress) {
  if (settings.backend === 'ws') { return fetchChunkWS(wsSession, path, start, size, onProgress); }
  return fetchChunkHTTP(path, start, size, onProgress);
}

async function fetchChunkHTTP(path, start, size, onProgress) {
  var t = settings.transform;
  var s = perRequestSecrets();
  var contentType = resolveContentTypeOverride();
  var body = {
    path: path, start: start, size: size,
    transform: t.type, args: buildTransformArgs(s.password, s.htmlB64Key),
  };
  if (contentType) body.contentType = contentType;
  var res = await fetch(API_BASE + '/api/files/download', {
    method:  'POST',
    headers: jsonHeaders(),
    body:    encodeBody(body),
  });
  assertOk(res);
  var buffer = await readBodyWithProgress(res, onProgress);
  return decrypt(buffer, s.password, s.htmlB64Key);
}

async function fetchChunkWS(wsSession, path, start, size, onProgress) {
  if (!wsSession.ws) {
    var t = settings.transform;
    var s = perRequestSecrets();
    var ws = await wsOpen(wsBaseURL() + '/api/files/ws/download');
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

export async function downloadFile(entry) {
  state.error = null;
  var path       = state.pathSegments.concat([entry.name]).join('/');
  var chunking   = settings.chunking;
  var wsSession  = { ws: null, password: null, htmlB64Key: null };
  var transfer   = addTransfer('download', entry.name);
  var onProgress = makeTransferProgress(transfer, entry.size);

  if (chunking.enabled) {
    var chunkBytes = chunking.chunkSize * 1024 * 1024;
    var numChunks  = Math.ceil(entry.size / chunkBytes);

    if (chunking.reassemble && state.hasFileSystemAccess) {
      try {
        var fileHandle = await window.showSaveFilePicker({ suggestedName: entry.name });
        var writable   = await fileHandle.createWritable();
        try {
          for (var i = 0; i < numChunks; i++) {
            var start = i * chunkBytes;
            var size  = Math.min(chunkBytes, entry.size - start);
            var data  = await fetchChunk(wsSession, path, start, size, onProgress);
            await writable.write(data);
          }
          await writable.close();
        } catch(err) {
          writable.abort();
          throw err;
        }
      } catch(err) {
        if (err && err.name !== 'AbortError') { state.error = 'Download failed: ' + err; }
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
            var data  = await fetchChunk(wsSession, path, start, size, onProgress);
            triggerDownload(data, entry.name + '.' + padIndex(i));
          }
        } catch(err) {
          state.error = 'Chunk failed: ' + (err.status ? 'HTTP ' + err.status : err);
        } finally {
          closeWSSession(wsSession);
          removeTransfer(transfer);
        }
      })();
    }
  } else {
    try {
      var data = await fetchChunk(wsSession, path, 0, entry.size, onProgress);
      triggerDownload(data, entry.name);
    } catch(err) {
      state.error = 'Download failed: ' + (err.status ? 'HTTP ' + err.status : err);
    } finally {
      closeWSSession(wsSession);
      removeTransfer(transfer);
    }
  }
}
