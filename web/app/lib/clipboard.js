// Clipboard tab logic: incremental sync against /api/clip/state, note
// create/edit/delete, and image attachments.
import { API_BASE, state, settings } from '../store.js';
import { apiFetch, authHeaders, jsonHeaders, encodeBody, decodeResponseJSON, paramEncryptEnabled, assertOk } from './api.js';
import { decrypt, buildTransformArgs, perRequestSecrets, encodeBytesForUpload, snapshotTransform } from './transform.js';
import { textToBase64, base64ToText, triggerDownload, saveURL, safeFileName } from './util.js';
import { makeRequestGuard } from './loadguard.js';
import { makeClipPoller } from './clippoll.js';

// Serializes overlapping auto-refresh polls (so a delayed one can't overwrite
// newer notes or rewind the cursor) and drops a poll whose database changed
// under it (auth key / DB swap).
const clipGuard = makeRequestGuard();
// Attachment id -> its current load; a load whose entry was replaced or
// removed meanwhile is discarded.
const attachmentLoads = new Map();

export async function copyClip(b64) {
  await navigator.clipboard.writeText(base64ToText(b64));
}

export function downloadClip(item) {
  var text = base64ToText(item.data);
  triggerDownload(new TextEncoder().encode(text), (safeFileName(item.title) || 'note-' + item.id) + '.txt');
}

// The server stores only an allowlist of raster types, so a downloaded
// attachment never gets an active extension; 'img' is a neutral fallback for
// anything unexpected (e.g. a legacy row).
function imageExt(mime) {
  switch (mime) {
    case 'image/png': return 'png';
    case 'image/jpeg': return 'jpg';
    case 'image/gif': return 'gif';
    case 'image/webp': return 'webp';
    default: return 'img';
  }
}

// Download name for an image: its caption, else the note title (numbered when
// the note has several images), else note-<id>-<n>.
export function attachmentFileName(item, att) {
  var ext = imageExt(att.mime);
  var name = safeFileName(att.caption || '');
  if (name) {
    var lower = name.toLowerCase();
    if (lower.endsWith('.' + ext) || (ext === 'jpg' && lower.endsWith('.jpeg'))) return name;
    return name + '.' + ext;
  }
  var atts = item.attachments || [];
  var n = atts.findIndex(function(a) { return a.id === att.id; }) + 1;
  var base = safeFileName(item.title);
  if (!base) base = 'note-' + item.id + '-' + n;
  else if (atts.length > 1) base += '-' + n;
  return base + '.' + ext;
}

export function downloadAttachment(item, att) {
  var url = state.clipImages[att.id];
  if (isBlobURL(url)) saveURL(url, attachmentFileName(item, att));
}

// Splits str into [{t, m}] segments, m marking case-insensitive matches of q.
function highlightSegments(str, q) {
  if (!q) return [{ t: str, m: false }];
  var re = new RegExp(q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'giu');
  var segs = [];
  var last = 0;
  var match;
  while ((match = re.exec(str)) !== null) {
    if (match.index > last) segs.push({ t: str.slice(last, match.index), m: false });
    segs.push({ t: match[0], m: true });
    last = re.lastIndex;
  }
  if (last < str.length) segs.push({ t: str.slice(last), m: false });
  return segs;
}

export function visibleClipItems() {
  var q = state.clipSearch;
  var scope = settings.clipSearchScope;
  var dir = settings.clipSortDesc ? -1 : 1;
  return state.clipItems.slice()
    .sort(function(a, b) { return (a.ts - b.ts) * dir; })
    .map(function(item) {
      var text  = base64ToText(item.data);
      var title = highlightSegments(item.title, scope !== 'content' ? q : '');
      var body  = highlightSegments(text, scope !== 'title' ? q : '');
      var captions = {};
      (item.attachments || []).forEach(function(a) {
        captions[a.id] = highlightSegments(a.caption || '', scope !== 'title' ? q : '');
      });
      var matched = function(segs) { return segs.some(function(s) { return s.m; }); };
      var hit = !q || matched(title) || matched(body) || Object.values(captions).some(matched);
      return hit ? { item: item, title: title, body: body, captions: captions } : null;
    })
    .filter(Boolean);
}

// Full resync from scratch — the auth key may now point at a different DB.
export function resetClipSync() {
  bumpClip();
  state.clipCursor = 0;
  state.clipItems  = [];
  Object.keys(state.clipImages).forEach(revokeClipImage);
  // An edit refers to note and attachment ids of the previous database.
  if (state.clipEditId !== null) resetClipDraft();
}

function bumpClip() {
  clipGuard.bump();
  clipPoller.abort();
}

// With wait, the server holds the request until the clipboard changes or its
// timeout passes.
export async function pollClipboard(opts) {
  var wait = !!(opts && opts.wait);
  var token = clipGuard.start();
  if (token === null) return { status: 'skipped' }; // a poll is already in flight for this database
  try {
    var encrypted = paramEncryptEnabled();
    var url = API_BASE + '/api/clip/state?since=' + state.clipCursor + (encrypted ? '&e=1' : '') + (wait ? '&wait' : '');
    var res = await apiFetch(url, { headers: authHeaders(), signal: opts && opts.signal });
    if (!clipGuard.fresh(token)) return { status: 'skipped' };
    if (!res.ok) throw Object.assign(new Error('HTTP ' + res.status), { status: res.status });
    var data = await decodeResponseJSON(res, encrypted);
    if (!clipGuard.fresh(token)) return { status: 'skipped' };
    applyClipState(data);
    state.clipError = null;
    return { status: 'ok' };
  } catch(err) {
    if (!clipGuard.fresh(token) || err.name === 'AbortError') return { status: 'skipped' };
    var suspect = !err.refused && !state.authFailed && err.status !== 401 && err.status !== 429;
    // A suspect long-poll failure is checked by a plain poll before it is shown.
    if (!(wait && suspect)) state.clipError = 'Failed to load clipboard (' + err.message + ')';
    return { status: 'error', error: err.message, suspect: suspect };
  } finally {
    clipGuard.done(token);
  }
}

const clipPoller = makeClipPoller({ poll: pollClipboard });

export function startClipPolling() { clipPoller.start(); }
export function stopClipPolling() { clipPoller.stop(); }

// Poll after a local change (create/edit/delete): mark a new generation so any
// in-flight auto-refresh poll is dropped and this one isn't skipped as a
// same-generation overlap, guaranteeing the change is reflected.
export async function reloadClipboard() {
  bumpClip();
  return pollClipboard();
}

// Upsert `changed`, drop ids absent from `ids`, honor `reset`.
function applyClipState(data) {
  if (data.rev === state.clipCursor && !data.reset) return;
  var byId = {};
  if (!data.reset) {
    state.clipItems.forEach(function(n) { byId[n.id] = n; });
  }
  (data.changed || []).forEach(function(n) { byId[n.id] = n; });
  if (data.ids) {
    var live = {};
    data.ids.forEach(function(id) { live[id] = true; });
    Object.keys(byId).forEach(function(id) { if (!live[id]) delete byId[id]; });
  }
  state.clipItems = Object.keys(byId).map(function(k) { return byId[k]; });
  state.clipCursor = data.rev;
  pruneClipImages();
}

// Revoke cached object URLs for attachments no longer referenced by any note.
function pruneClipImages() {
  var referenced = {};
  state.clipItems.forEach(function(n) {
    (n.attachments || []).forEach(function(a) { referenced[a.id] = true; });
  });
  Object.keys(state.clipImages).forEach(function(id) {
    if (!referenced[id]) revokeClipImage(id);
  });
}

function revokeClipImage(id) {
  attachmentLoads.delete(String(id));
  var v = state.clipImages[id];
  if (isBlobURL(v)) {
    try { URL.revokeObjectURL(v); } catch(e) {}
  }
  delete state.clipImages[id];
}

export function isBlobURL(v) {
  return typeof v === 'string' && v.indexOf('blob:') === 0;
}

// Lazily fetch an attachment's bytes once (immutable per id), disguised by
// the current transform, and cache the resulting object URL. The result is
// dropped if the cache entry was pruned or reset meanwhile: after a database
// change the same id may name another image.
export async function ensureAttachment(att) {
  if (state.clipImages[att.id] !== undefined) return;
  var load = {};
  attachmentLoads.set(String(att.id), load);
  state.clipImages[att.id] = 'loading';
  var result;
  try {
    var tx = snapshotTransform();
    var s = perRequestSecrets(tx);
    var res = await apiFetch(API_BASE + '/api/clip/attachment/download', {
      method:  'POST',
      headers: jsonHeaders(),
      body:    encodeBody({
        id:        att.id,
        transform: tx.type,
        args:      buildTransformArgs(tx, s.password, s.htmlB64Key),
      }),
    }, true);
    if (!res.ok) throw new Error('HTTP ' + res.status);
    var bytes = await decrypt(tx, await res.arrayBuffer(), s.password, s.htmlB64Key);
    result = URL.createObjectURL(new Blob([bytes], { type: att.mime }));
  } catch(err) {
    result = 'error';
  }
  var current = attachmentLoads.get(String(att.id)) === load;
  if (current) attachmentLoads.delete(String(att.id));
  if (current && state.clipImages[att.id] === 'loading') {
    state.clipImages[att.id] = result;
  } else if (isBlobURL(result)) {
    try { URL.revokeObjectURL(result); } catch(e) {}
  }
}

export function retryAttachment(id) {
  if (state.clipImages[id] === 'error') delete state.clipImages[id];
}

export function openAttachment(item, att) {
  var url = state.clipImages[att.id];
  if (isBlobURL(url)) {
    state.clipLightboxZoom = false;
    state.clipLightbox = { url: url, caption: att.caption || '', fileName: attachmentFileName(item, att) };
  }
}

export function handleClipPaste(event) {
  var items = (event.clipboardData && event.clipboardData.items) || [];
  var added = false;
  for (var i = 0; i < items.length; i++) {
    var it = items[i];
    if (it.kind === 'file' && it.type.indexOf('image/') === 0) {
      var blob = it.getAsFile();
      if (blob) {
        state.pendingAttachments.push({ blob: blob, url: URL.createObjectURL(blob), mime: it.type, caption: '' });
        added = true;
      }
    }
  }
  if (added) event.preventDefault();
}

export function removePendingAttachment(i) {
  var pa = state.pendingAttachments[i];
  if (!pa) return;
  try { URL.revokeObjectURL(pa.url); } catch(e) {}
  state.pendingAttachments.splice(i, 1);
}

async function uploadAttachment(noteId, pa) {
  var tx    = snapshotTransform();
  var bytes = new Uint8Array(await pa.blob.arrayBuffer());
  var enc   = await encodeBytesForUpload(tx, bytes, 'attachment');
  var spec  = { transform: tx.type, args: enc.args, note_id: noteId, mime: pa.mime,
                caption: sanitizeClipTitle(pa.caption) };
  var fd    = new FormData();
  fd.append('transform', new Blob([encodeBody(spec)]), 'transform.json');
  fd.append('file', enc.data, 'image');
  var res = await apiFetch(API_BASE + '/api/clip/attachment', { method: 'POST', headers: authHeaders(), body: fd });
  if (!res.ok) throw new Error('HTTP ' + res.status);
}

export function canSubmitClip() {
  return state.clipEditId !== null || !!state.clipText.trim() || !!state.clipTitle.trim() || state.pendingAttachments.length > 0;
}

// The server rejects control characters and titles/captions over 200 code
// points.
export function sanitizeClipTitle(title) {
  return Array.from(title.replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ').trim()).slice(0, 200).join('');
}

// Creates a note and returns its id. source: '' (typed in the UI) or 'outbox'.
export async function createNote(title, text, source) {
  var body = { title: sanitizeClipTitle(title), data: textToBase64(text) };
  if (source) { body.source = source; }
  var encrypted = paramEncryptEnabled();
  var res = await apiFetch(API_BASE + '/api/clip/new', { method: 'POST', headers: jsonHeaders(), body: encodeBody(body) });
  assertOk(res);
  return (await decodeResponseJSON(res, encrypted)).id;
}

export async function submitClip() {
  if (!canSubmitClip() || state.clipSubmitting) return;
  state.clipSubmitting = true;
  state.clipError = null;
  var editing = state.clipEditId !== null;
  var removedIds = state.clipRemovedAttachments.slice();
  var captionEdits = Object.assign({}, state.clipCaptionEdits);
  var editedItem = editing && state.clipItems.find(function(it) { return it.id === state.clipEditId; });
  try {
    var noteId = state.clipEditId;
    if (editing) {
      var body = { id: noteId, title: sanitizeClipTitle(state.clipTitle), data: textToBase64(state.clipText) };
      var res = await apiFetch(API_BASE + '/api/clip/edit', { method: 'POST', headers: jsonHeaders(), body: encodeBody(body) });
      if (!res.ok) throw new Error('HTTP ' + res.status);
    } else {
      noteId = await createNote(state.clipTitle, state.clipText, '');
      // Keep the new id so a retry after a later step fails updates this note
      // instead of creating a duplicate.
      state.clipEditId = noteId;
    }
    for (var r = 0; r < removedIds.length; r++) {
      await apiDeleteAttachment(removedIds[r]);
    }
    var editedAtts = (editedItem && editedItem.attachments) || [];
    for (var c = 0; c < editedAtts.length; c++) {
      var att = editedAtts[c];
      var caption = sanitizeClipTitle(captionEdits[att.id] || '');
      if (removedIds.indexOf(att.id) === -1 && caption !== (att.caption || '')) {
        await apiEditAttachmentCaption(att.id, caption);
      }
    }
    // Drop each image as it uploads, so a retry doesn't re-upload one that
    // already succeeded.
    while (state.pendingAttachments.length) {
      await uploadAttachment(noteId, state.pendingAttachments[0]);
      removePendingAttachment(0);
    }
    resetClipDraft();
    await reloadClipboard();
  } catch(err) {
    state.clipError = (editing ? 'Failed to update note (' : 'Failed to submit note (') + err.message + ')';
  } finally {
    state.clipSubmitting = false;
  }
}

export function editClip(item) {
  if (state.clipSubmitting) return;
  resetClipDraft();
  state.clipText   = base64ToText(item.data);
  state.clipTitle  = item.title;
  state.clipEditId = item.id;
  var captions = {};
  (item.attachments || []).forEach(function(a) { captions[a.id] = a.caption || ''; });
  state.clipCaptionEdits = captions;
}

export function resetClipDraft() {
  while (state.pendingAttachments.length) removePendingAttachment(0);
  state.clipText   = '';
  state.clipTitle  = '';
  state.clipEditId = null;
  state.clipRemovedAttachments = [];
  state.clipCaptionEdits = {};
}

export function isAttachmentRemoved(id) {
  return state.clipRemovedAttachments.indexOf(id) !== -1;
}

// Mark/unmark an existing image for deletion; applied on Update.
export function toggleRemoveAttachment(id) {
  var i = state.clipRemovedAttachments.indexOf(id);
  if (i === -1) state.clipRemovedAttachments.push(id);
  else state.clipRemovedAttachments.splice(i, 1);
}

export async function deleteClip(id) {
  if (settings.confirmDelete && !window.confirm('Delete this note?')) return;
  state.clipError = null;
  try {
    var res = await apiFetch(API_BASE + '/api/clip/delete', {
      method: 'DELETE', headers: jsonHeaders(), body: encodeBody({ id: id }),
    });
    if (!res.ok) throw new Error('HTTP ' + res.status);
    await reloadClipboard();
  } catch(err) {
    state.clipError = 'Failed to delete note (' + err.message + ')';
  }
}

async function apiEditAttachmentCaption(id, caption) {
  var res = await apiFetch(API_BASE + '/api/clip/attachment/edit', {
    method: 'POST', headers: jsonHeaders(), body: encodeBody({ id: id, caption: caption }),
  });
  if (!res.ok) throw new Error('HTTP ' + res.status);
}

async function apiDeleteAttachment(id) {
  var res = await apiFetch(API_BASE + '/api/clip/attachment/delete', {
    method: 'DELETE', headers: jsonHeaders(), body: encodeBody({ id: id }),
  });
  // 404 means it's already gone, so a retried submit that re-runs this delete
  // succeeds idempotently.
  if (!res.ok && res.status !== 404) throw new Error('HTTP ' + res.status);
}
