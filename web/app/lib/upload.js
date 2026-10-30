// Upload tab logic: upload-directory listing and whole-file/chunked uploads
// over HTTP or WS.
import { API_BASE, state, settings, authKeyHash } from '../store.js';
import { authHeaders, encodeBody, decodeResponseJSON, assertOk, noteAuthStatus,
         paramEncryptEnabled, wsBaseURL, sendWSControl, wsDecryptFor } from './api.js';
import { wsOpen, wsNextJSON, sendWSBinary } from './ws.js';
import { encodeBytesForUpload, encodeForUpload, uploadFilename } from './transform.js';
import { addTransfer, removeTransfer, makeTransferProgress, updateIfChanged } from './transfers.js';

export async function loadUploadDir() {
  state.uploadError = null;
  try {
    var encrypted = paramEncryptEnabled();
    var url = API_BASE + '/api/files/ul-list' + (encrypted ? '?e=1' : '');
    var res = await fetch(url, { headers: authHeaders() });
    noteAuthStatus(res);
    if (!res.ok) {
      state.uploadEntries = [];
      state.uploadError = res.status === 404
        ? 'Uploads are not enabled on this server.'
        : 'Failed to load upload directory (HTTP ' + res.status + ')';
      return;
    }
    var data = (await decodeResponseJSON(res, encrypted)) || [];
    updateIfChanged('uploadEntries', data);
  } catch(err) {
    state.uploadEntries = [];
    state.uploadError = 'Failed to load upload directory (' + err.message + ')';
  }
}

export async function uploadFiles(files) {
  state.uploadError = null;
  var promises = Array.from(files).map(async function(file) {
    var transfer   = addTransfer('upload', file.name);
    var onProgress = makeTransferProgress(transfer, file.size);
    try {
      if (settings.backend === 'ws') {
        await uploadFileWS(file, onProgress);
      } else {
        await uploadFileHTTP(file, onProgress);
      }
    } catch(err) {
      state.uploadError = 'Upload of "' + file.name + '" failed: ' +
        (err.status ? 'HTTP ' + err.status : err);
    } finally {
      removeTransfer(transfer);
    }
  });
  await Promise.all(promises);
  loadUploadDir();
}

function uploadForm(spec, encrypted, data, name) {
  var fd = new FormData();
  fd.append('transform', new Blob([spec], { type: encrypted ? 'text/plain' : 'application/json' }), 'transform.json');
  fd.append('file', data, name);
  return fd;
}

// A single-request HTTP upload has no progress event (fetch doesn't expose
// upload streaming), so onProgress only fires on the chunked path.
async function uploadFileHTTP(file, onProgress) {
  if (settings.uploadChunking.enabled) { return uploadFileHTTPChunked(file, onProgress); }

  var result = await encodeForUpload(file);
  var spec   = encodeBody({ transform: settings.transform.type, args: result.args, 'ul-done': true });
  var res = await fetch(API_BASE + '/api/files/upload', {
    method: 'POST', headers: authHeaders(), body: uploadForm(spec, paramEncryptEnabled(), result.data, file.name),
  });
  assertOk(res);
}

// Chunks are sent strictly one at a time: the server rejects concurrent
// requests for a filename and only recognizes the immediately-next key.
async function uploadFileHTTPChunked(file, onProgress) {
  var chunkSize = settings.uploadChunking.chunkSizeMB * 1024 * 1024;
  var bytes     = new Uint8Array(await file.arrayBuffer());
  var filename  = uploadFilename(file);
  var encrypted = paramEncryptEnabled();

  var continuationKey = '';
  var offset = 0;
  var done   = false;
  while (!done) {
    var end   = Math.min(offset + chunkSize, bytes.length);
    var slice = bytes.subarray(offset, end);
    done      = end >= bytes.length;

    var result = await encodeBytesForUpload(slice, filename);
    var spec = encodeBody({
      transform: settings.transform.type,
      args: result.args,
      'continuation-key': continuationKey,
      'ul-done': done,
    });
    var res = await fetch(API_BASE + '/api/files/upload', {
      method: 'POST', headers: authHeaders(), body: uploadForm(spec, encrypted, result.data, file.name),
    });
    assertOk(res);
    var data = await decodeResponseJSON(res, encrypted);
    continuationKey = data['continuation-key'] || '';

    if (onProgress) onProgress(slice.byteLength);
    offset = end;
  }
}

async function uploadFileWS(file, onProgress) {
  var result    = await encodeForUpload(file);
  var buffer    = await result.data.arrayBuffer();
  var frameSize = settings.wsFrameSizeKB * 1024;
  var decrypt   = wsDecryptFor(paramEncryptEnabled());

  var ws = await wsOpen(wsBaseURL() + '/api/files/ws/upload');
  try {
    sendWSControl(ws, {
      auth:      authKeyHash.value,
      filename:  file.name,
      transform: settings.transform.type,
      args:      result.args,
      frameSize: frameSize,
    });
    var ack = await wsNextJSON(ws, decrypt);
    if (!ack.ok) throw new Error(ack.error || 'upload failed');

    await sendWSBinary(ws, buffer, frameSize, onProgress);
    sendWSControl(ws, { done: true });

    var final = await wsNextJSON(ws, decrypt);
    if (!final.ok) throw new Error(final.error || 'upload failed');
  } finally {
    ws.close();
  }
}
