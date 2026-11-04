// Upload tab logic: the flat upload-directory listing (-ul-flat), directory
// creation, and whole-file/chunked uploads over HTTP or WS.
import { API_BASE, state, settings, authKeyHash } from '../store.js';
import { authHeaders, jsonHeaders, encodeBody, decodeResponseJSON, assertOk,
         paramEncryptEnabled, apiFetch, apiWSOpen, noteWSAck, sendWSControl, wsDecryptFor } from './api.js';
import { load } from './download.js';
import { wsNextJSON, sendWSBinary } from './ws.js';
import { encodeBytesForUpload, encodeForUpload, uploadFilename, snapshotTransform } from './transform.js';
import { addTransfer, removeTransfer, makeTransferProgress, updateIfChanged } from './transfers.js';
import { addToast } from './notices.js';
import { sha256Hex } from './util.js';

export async function loadUploadDir() {
  try {
    var encrypted = paramEncryptEnabled();
    var url = API_BASE + '/api/files/ul-list' + (encrypted ? '?e=1' : '');
    var res = await apiFetch(url, { headers: authHeaders() });
    if (!res.ok) {
      state.uploadEntries = [];
      state.uploadError = res.status === 404
        ? 'Uploads are not enabled on this server.'
        : 'Failed to load upload directory (HTTP ' + res.status + ')';
      return;
    }
    var data = (await decodeResponseJSON(res, encrypted)) || [];
    updateIfChanged(state, 'uploadEntries', data);
    state.uploadError = null;
  } catch(err) {
    state.uploadEntries = [];
    state.uploadError = 'Failed to load upload directory (' + err.message + ')';
  }
}

export function refreshUpload() {
  if (state.ulFlat) loadUploadDir();
  else load('ul');
}

// Mirrors the server's limit on how deep below the upload root a directory
// may be created.
export const MKDIR_MAX_DEPTH = 4;

export async function createDir(name) {
  var path = state.browse.ul.segments.concat([name]).join('/');
  var res = await apiFetch(API_BASE + '/api/ul/mkdir', {
    method: 'POST', headers: jsonHeaders(), body: encodeBody({ path: path }),
  });
  if (!res.ok) throw new Error((await res.text()).trim() || 'HTTP ' + res.status);
  load('ul');
}

export async function uploadFiles(files) {
  state.uploadError = null;
  // Fixed at the start, so navigating away or changing settings mid-upload
  // can't redirect the transfer or encode with a mismatched transform.
  var dir = state.ulFlat ? '' : state.browse.ul.segments.join('/');
  var tx = snapshotTransform();
  var backend = settings.backend;
  var promises = Array.from(files).map(async function(file) {
    var transfer   = addTransfer('upload', file.name, 'ul');
    var onProgress = makeTransferProgress(transfer, file.size);
    try {
      var serverHash = backend === 'ws'
        ? await uploadFileWS(tx, file, dir, onProgress)
        : await uploadFileHTTP(tx, file, dir, onProgress);
      await reportUpload(file, serverHash);
    } catch(err) {
      addToast(false, 'Upload of "' + file.name + '" failed: ' +
        (err.status ? 'HTTP ' + err.status : err.message || err));
    } finally {
      removeTransfer(transfer);
    }
  });
  await Promise.all(promises);
  refreshUpload();
}

// A green toast means the server's hash of the stored file matches the local
// file, so it was written fully and correctly. A mismatch is a red, sticky toast.
async function reportUpload(file, serverHash) {
  var label = '"' + file.name + '"';
  if (!serverHash || !(window.crypto && window.crypto.subtle)) {
    addToast(true, label + ' uploaded (unverified)');
    return;
  }
  var localHash = await sha256Hex(await file.arrayBuffer());
  if (localHash === serverHash) addToast(true, label + ' uploaded (verified)');
  else addToast(false, label + ' uploaded, but the hash does not match — the file may be corrupted on the server');
}

// dir is sent only when set, so a flat upload is accepted under -ul-flat.
function withDir(spec, dir) {
  if (dir) spec.dir = dir;
  return spec;
}

function uploadForm(spec, encrypted, data, name) {
  var fd = new FormData();
  fd.append('transform', new Blob([spec], { type: encrypted ? 'text/plain' : 'application/json' }), 'transform.json');
  fd.append('file', data, name);
  return fd;
}

// A single-request HTTP upload has no progress event (fetch doesn't expose
// upload streaming), so onProgress only fires on the chunked path.
async function uploadFileHTTP(tx, file, dir, onProgress) {
  if (settings.uploadChunking.enabled) { return uploadFileHTTPChunked(tx, file, dir, onProgress); }

  var encrypted = paramEncryptEnabled();
  var result = await encodeForUpload(tx, file);
  var spec   = encodeBody(withDir({ transform: tx.type, args: result.args, 'ul-done': true }, dir));
  var res = await apiFetch(API_BASE + '/api/files/upload', {
    method: 'POST', headers: authHeaders(), body: uploadForm(spec, encrypted, result.data, file.name),
  });
  assertOk(res);
  var data = await decodeResponseJSON(res, encrypted);
  return data && data.sha256;
}

// Chunks are sent strictly one at a time: the server rejects concurrent
// requests for a filename and only recognizes the immediately-next key.
async function uploadFileHTTPChunked(tx, file, dir, onProgress) {
  var chunkSize = settings.uploadChunking.chunkSizeMB * 1024 * 1024;
  var bytes     = new Uint8Array(await file.arrayBuffer());
  var filename  = uploadFilename(tx, file);
  var encrypted = paramEncryptEnabled();

  var continuationKey = '';
  var serverHash = '';
  var offset = 0;
  var done   = false;
  while (!done) {
    var end   = Math.min(offset + chunkSize, bytes.length);
    var slice = bytes.subarray(offset, end);
    done      = end >= bytes.length;

    var result = await encodeBytesForUpload(tx, slice, filename);
    var spec = encodeBody(withDir({
      transform: tx.type,
      args: result.args,
      'continuation-key': continuationKey,
      'ul-done': done,
    }, dir));
    var res = await apiFetch(API_BASE + '/api/files/upload', {
      method: 'POST', headers: authHeaders(), body: uploadForm(spec, encrypted, result.data, file.name),
    });
    assertOk(res);
    var data = await decodeResponseJSON(res, encrypted);
    continuationKey = data['continuation-key'] || '';
    if (done) serverHash = data.sha256 || '';

    if (onProgress) onProgress(slice.byteLength);
    offset = end;
  }
  return serverHash;
}

async function uploadFileWS(tx, file, dir, onProgress) {
  var result    = await encodeForUpload(tx, file);
  var buffer    = await result.data.arrayBuffer();
  var frameSize = settings.wsFrameSizeKB * 1024;
  var decrypt   = wsDecryptFor(paramEncryptEnabled());

  var ws = await apiWSOpen('/api/files/ws/upload');
  try {
    sendWSControl(ws, withDir({
      auth:      authKeyHash.value,
      filename:  file.name,
      transform: tx.type,
      args:      result.args,
      frameSize: frameSize,
    }, dir));
    var ack = noteWSAck(await wsNextJSON(ws, decrypt));
    if (!ack.ok) throw new Error(ack.error || 'upload failed');

    await sendWSBinary(ws, buffer, frameSize, onProgress);
    sendWSControl(ws, { done: true });

    var final = await wsNextJSON(ws, decrypt);
    if (!final.ok) throw new Error(final.error || 'upload failed');
    return final.sha256;
  } finally {
    ws.close();
  }
}
