// Clipboard tab logic: incremental sync against /api/clip/state, note
// create/edit/delete, and image attachments.
import { API_BASE, state, settings } from '../store.js';
import { authHeaders, jsonHeaders, encodeBody, decodeResponseJSON, paramEncryptEnabled, assertOk, noteAuthStatus } from './api.js';
import { decrypt, buildTransformArgs, perRequestSecrets, encodeBytesForUpload } from './transform.js';
import { textToBase64, base64ToText, triggerDownload } from './util.js';

export async function copyClip(b64) {
  await navigator.clipboard.writeText(base64ToText(b64));
}

export function downloadClip(item) {
  var text = base64ToText(item.data);
  // Strip characters that are invalid in Windows or Unix filenames.
  var name = item.title.replace(/[<>:"\/\\|?*\u0000-\u001f\u007f]/g, '_').replace(/^[\s.]+|[\s.]+$/g, '').slice(0, 100);
  triggerDownload(new TextEncoder().encode(text), (name || 'note-' + item.id) + '.txt');
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
      var hit = !q || title.some(function(s) { return s.m; }) || body.some(function(s) { return s.m; });
      return hit ? { item: item, title: title, body: body } : null;
    })
    .filter(Boolean);
}

// Full resync from scratch — the auth key may now point at a different DB.
export function resetClipSync() {
  state.clipCursor = 0;
  state.clipItems  = [];
  Object.keys(state.clipImages).forEach(revokeClipImage);
}

export async function pollClipboard() {
  state.clipError = null;
  try {
    var encrypted = paramEncryptEnabled();
    var url = API_BASE + '/api/clip/state?since=' + state.clipCursor + (encrypted ? '&e=1' : '');
    var res = await fetch(url, { headers: authHeaders() });
    noteAuthStatus(res);
    if (!res.ok) throw new Error('HTTP ' + res.status);
    applyClipState(await decodeResponseJSON(res, encrypted));
  } catch(err) {
    state.clipError = 'Failed to load clipboard (' + err.message + ')';
  }
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
// the current transform, and cache the resulting object URL.
export async function ensureAttachment(att) {
  if (state.clipImages[att.id] !== undefined) return;
  state.clipImages[att.id] = 'loading';
  try {
    var s = perRequestSecrets();
    var res = await fetch(API_BASE + '/api/clip/attachment/download', {
      method:  'POST',
      headers: jsonHeaders(),
      body:    encodeBody({
        id:        att.id,
        transform: settings.transform.type,
        args:      buildTransformArgs(s.password, s.htmlB64Key),
      }),
    });
    if (!res.ok) throw new Error('HTTP ' + res.status);
    var bytes = await decrypt(await res.arrayBuffer(), s.password, s.htmlB64Key);
    state.clipImages[att.id] = URL.createObjectURL(new Blob([bytes], { type: att.mime }));
  } catch(err) {
    state.clipImages[att.id] = 'error';
  }
}

export function openAttachment(id) {
  var url = state.clipImages[id];
  if (isBlobURL(url)) {
    state.clipLightboxZoom = false;
    state.clipLightbox = url;
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
        state.pendingAttachments.push({ blob: blob, url: URL.createObjectURL(blob), mime: it.type });
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

function clearPendingAttachments() {
  state.pendingAttachments.forEach(function(pa) {
    try { URL.revokeObjectURL(pa.url); } catch(e) {}
  });
  state.pendingAttachments = [];
}

async function uploadAttachment(noteId, pa) {
  var bytes = new Uint8Array(await pa.blob.arrayBuffer());
  var enc   = await encodeBytesForUpload(bytes, 'attachment');
  var spec  = { transform: settings.transform.type, args: enc.args, note_id: noteId, mime: pa.mime };
  var fd    = new FormData();
  fd.append('transform', new Blob([encodeBody(spec)]), 'transform.json');
  fd.append('file', enc.data, 'image');
  var res = await fetch(API_BASE + '/api/clip/attachment', { method: 'POST', headers: authHeaders(), body: fd });
  if (!res.ok) throw new Error('HTTP ' + res.status);
}

export function canSubmitClip() {
  return !!state.clipText.trim() || !!state.clipTitle.trim() || state.pendingAttachments.length > 0;
}

// The server rejects control characters and titles over 200 code points.
export function sanitizeClipTitle(title) {
  return Array.from(title.replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ').trim()).slice(0, 200).join('');
}

// Creates a note and returns its id. source: '' (typed in the UI) or 'outbox'.
export async function createNote(title, text, source) {
  var body = { title: sanitizeClipTitle(title), data: textToBase64(text) };
  if (source) { body.source = source; }
  var encrypted = paramEncryptEnabled();
  var res = await fetch(API_BASE + '/api/clip/new', { method: 'POST', headers: jsonHeaders(), body: encodeBody(body) });
  assertOk(res);
  return (await decodeResponseJSON(res, encrypted)).id;
}

export async function submitClip() {
  if (!canSubmitClip()) return;
  state.clipError = null;
  var editing = state.clipEditId !== null;
  try {
    var noteId = state.clipEditId;
    if (editing) {
      var body = { id: noteId, title: sanitizeClipTitle(state.clipTitle), data: textToBase64(state.clipText) };
      var res = await fetch(API_BASE + '/api/clip/edit', { method: 'POST', headers: jsonHeaders(), body: encodeBody(body) });
      if (!res.ok) throw new Error('HTTP ' + res.status);
    } else {
      noteId = await createNote(state.clipTitle, state.clipText, '');
    }
    for (var r = 0; r < state.clipRemovedAttachments.length; r++) {
      await apiDeleteAttachment(state.clipRemovedAttachments[r]);
    }
    for (var i = 0; i < state.pendingAttachments.length; i++) {
      await uploadAttachment(noteId, state.pendingAttachments[i]);
    }
    clearPendingAttachments();
    cancelEdit();
    await pollClipboard();
  } catch(err) {
    state.clipError = (editing ? 'Failed to update note (' : 'Failed to submit note (') + err.message + ')';
  }
}

export function editClip(item) {
  state.clipText   = base64ToText(item.data);
  state.clipTitle  = item.title;
  state.clipEditId = item.id;
  state.clipRemovedAttachments = [];
}

export function cancelEdit() {
  state.clipText   = '';
  state.clipTitle  = '';
  state.clipEditId = null;
  state.clipRemovedAttachments = [];
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
    var res = await fetch(API_BASE + '/api/clip/delete', {
      method: 'DELETE', headers: jsonHeaders(), body: encodeBody({ id: id }),
    });
    if (!res.ok) throw new Error('HTTP ' + res.status);
    await pollClipboard();
  } catch(err) {
    state.clipError = 'Failed to delete note (' + err.message + ')';
  }
}

async function apiDeleteAttachment(id) {
  var res = await fetch(API_BASE + '/api/clip/attachment/delete', {
    method: 'DELETE', headers: jsonHeaders(), body: encodeBody({ id: id }),
  });
  if (!res.ok) throw new Error('HTTP ' + res.status);
}
