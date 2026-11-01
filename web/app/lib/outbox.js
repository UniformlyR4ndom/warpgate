// Outbox: local scripts drop text into a folder picked via the File System
// Access API; this tab pushes it into the clipboard. A script writes <name>,
// then <name>.sha256 (line 1: hex hash, optionally "<hex>  <name>"; line 2:
// optional title). This tab only reads, deletes and renames files in the folder:
// it deletes both files after a successful push and renames the hash file to
// <name>.failed otherwise.
import { state, authKeyHash } from '../store.js';
import { createNote, pollClipboard, sanitizeClipTitle } from './clipboard.js';
import { sha256Hex } from './util.js';

var MAX_BYTES = 512 * 1024;
var PICKUP_DELAY_MS = 300;
var SCAN_MS = 5000;
var FAIL_AFTER_MS = 10000;
var LEFTOVER_MS = 60 * 60 * 1000;
var REMOVE_RETRY_MS = [0, 200, 500, 1000, 2000];
var NOTICE_MS = 5000;

var dir = null;
var savedHandle = null;
var generation = 0;
var observer = null;
var releaseLock = null;
var kicked = false;
var wake = null;
var firstSeen = {};
var noticeSeq = 0;

// --- persistence of the folder handle and of pushed-but-not-yet-deleted files

var dbPromise = null;

function kvStore(mode) {
  if (!dbPromise) {
    dbPromise = new Promise(function(resolve, reject) {
      var req = indexedDB.open('warpgate-outbox', 1);
      req.onupgradeneeded = function() { req.result.createObjectStore('kv'); };
      req.onsuccess = function() { resolve(req.result); };
      req.onerror = function() { reject(req.error); };
    });
  }
  return dbPromise.then(function(db) { return db.transaction('kv', mode).objectStore('kv'); });
}

function kvRequest(mode, fn) {
  return kvStore(mode).then(function(store) {
    return new Promise(function(resolve, reject) {
      var req = fn(store);
      req.onsuccess = function() { resolve(req.result); };
      req.onerror = function() { reject(req.error); };
    });
  });
}

function kvGet(key)      { return kvRequest('readonly',  function(s) { return s.get(key); }); }
function kvSet(key, val) { return kvRequest('readwrite', function(s) { return s.put(val, key); }); }
function kvDel(key)      { return kvRequest('readwrite', function(s) { return s.delete(key); }); }

// --- lifecycle

export async function initOutbox() {
  state.outbox.supported = typeof window.showDirectoryPicker === 'function';
  if (!state.outbox.supported) return;
  authKeyHash.subscribe(function() {
    if (state.outbox.status === 'auth') { state.outbox.status = 'watching'; kick(); }
  });
  var handle = null;
  try { handle = await kvGet('dir'); } catch (e) {}
  if (!handle) return;
  state.outbox.folder = handle.name;
  if (await handle.queryPermission({ mode: 'readwrite' }) === 'granted') start(handle);
  else { savedHandle = handle; state.outbox.status = 'reconnect'; }
}

export async function connectOutbox() {
  var handle;
  try {
    handle = await window.showDirectoryPicker({ id: 'wgclip-outbox', mode: 'readwrite' });
  } catch (e) {
    if (e.name !== 'AbortError') setError(e);
    return;
  }
  await kvSet('dir', handle);
  start(handle);
}

export async function reconnectOutbox() {
  if (savedHandle && await savedHandle.requestPermission({ mode: 'readwrite' }) === 'granted') start(savedHandle);
}

export async function disconnectOutbox() {
  stop();
  savedHandle = null;
  state.outbox.status = 'off';
  state.outbox.folder = '';
  await kvDel('dir');
}

function start(handle) {
  stop();
  var gen = generation;
  dir = handle;
  savedHandle = null;
  state.outbox.folder = handle.name;
  state.outbox.status = 'other-tab';
  // Only one tab of this origin processes the outbox; others wait in the queue.
  navigator.locks.request('warpgate-outbox', function() {
    if (gen !== generation) return;
    state.outbox.status = 'watching';
    watch(gen);
    return new Promise(function(resolve) { releaseLock = resolve; });
  });
}

function stop() {
  generation++;
  dir = null;
  firstSeen = {};
  if (observer) { observer.disconnect(); observer = null; }
  if (releaseLock) { releaseLock(); releaseLock = null; }
  kick();
}

function setError(err) {
  state.outbox.status = 'error';
  state.outbox.error = err.message || String(err);
}

// --- watching

function kick() {
  kicked = true;
  if (wake) wake();
}

// Resolves after ms, or early on kick(). Called only after awaiting file system
// calls, so hidden-tab throttling never sees a chain of timers.
function sleep(ms) {
  return new Promise(function(resolve) {
    if (kicked) { resolve(); return; }
    var t = setTimeout(resolve, ms);
    wake = function() { clearTimeout(t); resolve(); };
  });
}

function delay(ms) {
  return new Promise(function(resolve) { setTimeout(resolve, ms); });
}

async function watch(gen) {
  if ('FileSystemObserver' in window) {
    observer = new FileSystemObserver(kick);
    try { await observer.observe(dir); } catch (e) { observer = null; }
  }
  while (gen === generation) {
    kicked = false;
    if (state.outbox.status === 'watching' || state.outbox.status === 'error') {
      try {
        await delay(PICKUP_DELAY_MS);
        await scan(gen);
        if (gen === generation && state.outbox.status === 'error') state.outbox.status = 'watching';
      } catch (e) {
        if (gen === generation) setError(e);
      }
    }
    await sleep(SCAN_MS);
  }
}

// folder is captured once per scan: a push still in flight when the user
// switches folders must finish in the folder its files came from.
async function scan(gen) {
  var folder = dir;
  var files = {};
  for await (var entry of folder.entries()) {
    if (entry[1].kind === 'file') files[entry[0]] = entry[1];
  }
  var names = Object.keys(files).sort();
  for (var i = 0; i < names.length; i++) {
    if (gen !== generation || state.outbox.status === 'auth') return;
    if (names[i].endsWith('.sha256')) await processPush(folder, names[i].slice(0, -'.sha256'.length), files);
  }
  if (gen === generation) await removeLeftovers(folder, names, files);
}

// --- one push

function parseHashFile(text) {
  var lines = text.split(/\r?\n/);
  var m = /^([0-9a-fA-F]{64})(\s|$)/.exec(lines[0].trim());
  return m ? { hash: m[1].toLowerCase(), title: lines[1] || '' } : null;
}

async function processPush(folder, name, files) {
  var hashName = name + '.sha256';
  var now = Date.now();
  if (!firstSeen[hashName]) firstSeen[hashName] = now;
  var waited = now - firstSeen[hashName];

  // TextDecoder drops a leading UTF-8 BOM, which Windows PowerShell 5.1 always writes.
  var parsed = parseHashFile(new TextDecoder().decode(await (await files[hashName].getFile()).arrayBuffer()));
  if (!parsed) return retryOrFail(name, files, waited, 'invalid hash file');
  if (await kvGet('pushed:' + hashName).catch(function() { return null; }) === parsed.hash) {
    return removePushed(folder, name);
  }
  if (!files[name]) return retryOrFail(name, files, waited, 'content file missing');
  var file = await files[name].getFile();
  if (file.size > MAX_BYTES) return fail(name, files, 'too large: ' + file.size + ' bytes, limit ' + MAX_BYTES);
  var buf = await file.arrayBuffer();
  if (await sha256Hex(buf) !== parsed.hash) return retryOrFail(name, files, waited, 'hash mismatch');
  var text;
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(buf);
  } catch (e) {
    return fail(name, files, 'not UTF-8 text');
  }

  var title = sanitizeClipTitle(parsed.title);
  var id;
  try {
    id = await createNote(title, text, 'outbox');
  } catch (e) {
    if (e.status === 401) { state.outbox.status = 'auth'; return; }
    return retryOrFail(name, files, waited, 'server error (' + e.message + ')');
  }
  // Recorded before deleting, so a hash file that survives a failed delete is never pushed twice.
  await kvSet('pushed:' + hashName, parsed.hash).catch(function() {});
  delete firstSeen[hashName];
  notify(true, 'Note #' + id + (title ? ' "' + title + '"' : '') + ' pushed from outbox');
  pollClipboard();
  await removePushed(folder, name);
}

function retryOrFail(name, files, waited, msg) {
  if (waited < FAIL_AFTER_MS) return;
  return fail(name, files, msg);
}

async function fail(name, files, msg) {
  delete firstSeen[name + '.sha256'];
  notify(false, name + ': ' + msg);
  for (var i = 0; i < REMOVE_RETRY_MS.length; i++) {
    await delay(REMOVE_RETRY_MS[i]);
    try { await files[name + '.sha256'].move(name + '.failed'); return; } catch (e) {}
  }
}

async function remove(folder, name) {
  for (var i = 0; i < REMOVE_RETRY_MS.length; i++) {
    await delay(REMOVE_RETRY_MS[i]);
    try {
      await folder.removeEntry(name);
      return true;
    } catch (e) {
      if (e.name === 'NotFoundError') return true;
    }
  }
  return false;
}

// The hash file goes first: it is what triggers a push.
async function removePushed(folder, name) {
  if (await remove(folder, name + '.sha256') && await remove(folder, name)) {
    await kvDel('pushed:' + name + '.sha256').catch(function() {});
  }
}

// Failed pushes and orphaned content from the push function (push-* names) are
// deleted after an hour; anything else in the folder is left alone.
async function removeLeftovers(folder, names, files) {
  var present = {};
  names.forEach(function(n) { present[n] = true; });
  for (var i = 0; i < names.length; i++) {
    var n = names[i];
    if (!n.startsWith('push-') || n.endsWith('.sha256')) continue;
    var failed = n.endsWith('.failed');
    if (!failed && (present[n + '.sha256'] || present[n + '.failed'])) continue;
    if (Date.now() - (await files[n].getFile()).lastModified < LEFTOVER_MS) continue;
    if (await remove(folder, n) && failed) await remove(folder, n.slice(0, -'.failed'.length));
  }
}

// --- notices

function notify(ok, text) {
  var id = ++noticeSeq;
  state.outbox.notices.push({ id: id, ok: ok, text: text });
  if (ok) setTimeout(function() { dismissNotice(id); }, NOTICE_MS);
}

export function dismissNotice(id) {
  var i = state.outbox.notices.findIndex(function(n) { return n.id === id; });
  if (i >= 0) state.outbox.notices.splice(i, 1);
}
