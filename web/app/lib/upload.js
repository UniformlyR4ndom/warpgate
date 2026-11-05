// Upload tab logic: the flat upload-directory listing (-ul-flat), directory
// creation, and whole-file/chunked uploads over HTTP or WS.
import { API_BASE, state, settings, authKeyHash } from '../store.js';
import { authHeaders, jsonHeaders, encodeBody, decodeResponseJSON, assertOk,
         paramEncryptEnabled, apiFetch, apiWSOpen, noteWSAck, sendWSControl, wsDecryptFor } from './api.js';
import { load, reload } from './download.js';
import { wsNextJSON, sendWSBinary } from './ws.js';
import { encodeBytesForUpload, uploadFilename, snapshotTransform, buildTransformArgs,
         transformBuffers, requestLimitError, newStreamCodec } from './transform.js';
import { addTransfer, removeTransfer, makeTransferProgress, updateIfChanged } from './transfers.js';
import { addToast } from './notices.js';
import { chunkBytesFromMB, REQUEST_LIMIT_BYTES } from './util.js';

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

// Refresh after a local change (upload, new folder): force the tree listing so
// it isn't skipped as a same-context overlap and shows the change immediately.
export function reloadUpload() {
  if (state.ulFlat) loadUploadDir();
  else reload('ul');
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
  reload('ul');
}

export async function uploadFiles(files) {
  state.uploadError = null;
  // Fixed at the start, so navigating away or changing settings mid-upload
  // can't redirect the transfer or encode with a mismatched transform.
  var dir = state.ulFlat ? '' : state.browse.ul.segments.join('/');
  var tx = snapshotTransform();
  var backend = settings.backend;
  var whole = backend === 'ws' || !settings.uploadChunking.enabled;
  var promises = Array.from(files).map(async function(file) {
    if (whole && transformBuffers(tx.type) && file.size > REQUEST_LIMIT_BYTES) {
      addToast(false, requestLimitError(file.name, tx.type, 'turn on upload chunking (HTTP backend)'));
      return;
    }
    var transfer   = addTransfer('upload', file.name, 'ul');
    var onProgress = makeTransferProgress(transfer, file.size);
    try {
      var hasher = sha256.create();
      var serverHash = backend === 'ws'
        ? await uploadFileWS(tx, file, dir, onProgress, hasher)
        : await uploadFileHTTP(tx, file, dir, onProgress, hasher);
      reportUpload(file, serverHash, hasher.hex());
    } catch(err) {
      addToast(false, 'Upload of "' + file.name + '" failed: ' +
        (err.status ? 'HTTP ' + err.status : err.message || err));
    } finally {
      removeTransfer(transfer);
    }
  });
  await Promise.all(promises);
  reloadUpload();
}

// A green toast means the server's hash of the stored file matches the hash of
// the bytes read for the upload, so it was written fully and correctly. A
// mismatch is a red, sticky toast.
function reportUpload(file, serverHash, localHash) {
  var label = '"' + file.name + '"';
  if (!serverHash) {
    addToast(true, label + ' uploaded (unverified)');
    return;
  }
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

var READ_BYTES = 8 * 1024 * 1024;

// Reads blob in slices of at most size bytes, hashing each into hasher before
// handing it to fn, so no file is ever read into memory whole.
async function readSlices(blob, size, hasher, fn) {
  for (var off = 0; off < blob.size; off += size) {
    var bytes = new Uint8Array(await blob.slice(off, off + size).arrayBuffer());
    hasher.update(bytes);
    if (fn) await fn(bytes);
  }
}

// Encodes file[start, end) as one request's file part. id sends the file slice
// itself and reads it only to hash it; the streaming transforms encode slice
// by slice into Blob parts; pdf and html-b64 encode the range whole.
async function encodeRange(tx, file, start, end, hasher) {
  var range = file.slice(start, end);
  var codec = newStreamCodec(tx);
  if (tx.type === 'id') {
    await readSlices(range, READ_BYTES, hasher);
    return { data: range, args: {} };
  }
  if (codec) {
    var parts = [];
    await readSlices(range, READ_BYTES, hasher, function(bytes) { parts.push(new Blob([codec(bytes)])); });
    return { data: new Blob(parts), args: buildTransformArgs(tx) };
  }
  var bytes = new Uint8Array(await range.arrayBuffer());
  hasher.update(bytes);
  return encodeBytesForUpload(tx, bytes, uploadFilename(tx, file));
}

// A single-request HTTP upload has no progress event (fetch doesn't expose
// upload streaming), so onProgress only fires on the chunked path.
async function uploadFileHTTP(tx, file, dir, onProgress, hasher) {
  if (settings.uploadChunking.enabled) { return uploadFileHTTPChunked(tx, file, dir, onProgress, hasher); }

  var encrypted = paramEncryptEnabled();
  var result = await encodeRange(tx, file, 0, file.size, hasher);
  var spec   = encodeBody(withDir({ transform: tx.type, args: result.args, 'ul-done': true }, dir));
  var res = await apiFetch(API_BASE + '/api/files/upload', {
    method: 'POST', headers: authHeaders(), body: uploadForm(spec, encrypted, result.data, file.name),
  });
  assertOk(res);
  var data = await decodeResponseJSON(res, encrypted);
  return data && data.sha256;
}

// Chunks are sent strictly one at a time: the server rejects concurrent
// requests for a filename and only recognizes the immediately-next key. An
// empty file is one empty chunk.
async function uploadFileHTTPChunked(tx, file, dir, onProgress, hasher) {
  var chunkSize = chunkBytesFromMB(settings.uploadChunking.chunkSizeMB, 100);
  var encrypted = paramEncryptEnabled();

  var continuationKey = '';
  var serverHash = '';
  var offset = 0;
  var done   = false;
  while (!done) {
    var end   = Math.min(offset + chunkSize, file.size);
    done      = end >= file.size;

    var result = await encodeRange(tx, file, offset, end, hasher);
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

    if (onProgress) onProgress(end - offset);
    offset = end;
  }
  return serverHash;
}

// The server decodes a WS upload once, after the last frame, so a streaming
// transform encodes the whole connection as one piece: one codec, fed block by
// block. Blocks are a multiple of the frame size, so frames stay full.
async function uploadFileWS(tx, file, dir, onProgress, hasher) {
  var frameSize = settings.wsFrameSizeKB * 1024;
  var decrypt   = wsDecryptFor(paramEncryptEnabled());
  var codec     = newStreamCodec(tx);
  var whole     = codec ? null : await encodeRange(tx, file, 0, file.size, hasher);

  var ws = await apiWSOpen('/api/files/ws/upload');
  try {
    sendWSControl(ws, withDir({
      auth:      authKeyHash.value,
      filename:  file.name,
      transform: tx.type,
      args:      whole ? whole.args : buildTransformArgs(tx),
      frameSize: frameSize,
    }, dir));
    var ack = noteWSAck(await wsNextJSON(ws, decrypt));
    if (!ack.ok) throw new Error(ack.error || 'upload failed');

    if (whole) {
      await sendWSBinary(ws, new Uint8Array(await whole.data.arrayBuffer()), frameSize, onProgress);
    } else {
      var block = frameSize * Math.ceil(READ_BYTES / frameSize);
      await readSlices(file, block, hasher, function(bytes) { return sendWSBinary(ws, codec(bytes), frameSize, onProgress); });
    }
    sendWSControl(ws, { done: true });

    var final = await wsNextJSON(ws, decrypt);
    if (!final.ok) throw new Error(final.error || 'upload failed');
    return final.sha256;
  } finally {
    ws.close();
  }
}
