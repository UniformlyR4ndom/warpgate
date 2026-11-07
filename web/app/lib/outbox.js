// Outbox: local scripts drop text into a folder picked via the File System
// Access API; this tab pushes it into the clipboard. A script writes <name>,
// then <name>.sha256 (line 1: hex hash, optionally "<hex>  <name>"; line 2:
// optional title). This tab only reads, deletes and renames files in the folder:
// it deletes both files after a successful push and renames the hash file to
// <name>.failed otherwise.
import { state } from '../store.js';
import { createNote, reloadClipboard, sanitizeClipTitle } from './clipboard.js';
import { sha256Hex } from './util.js';
import { addToast } from './notices.js';

var MAX_BYTES = 512 * 1024;
var PICKUP_DELAY_MS = 300;
var SCAN_MS = 5000;
var FAIL_AFTER_MS = 10000;
var LEFTOVER_MS = 60 * 60 * 1000;
var REMOVE_RETRY_MS = [0, 200, 500, 1000, 2000];

var savedHandle = null;
var current = null; // the watcher of the connected folder
// Hash files this tab pushed whose files are not deleted yet: the record that
// still holds when IndexedDB fails.
var pushedHere = {};

// --- persistence of the folder handle and of pushed-but-not-yet-deleted files

var dbPromise = null;

function openDB() {
  if (!dbPromise) {
    dbPromise = new Promise(function(resolve, reject) {
      var req = indexedDB.open('warpgate-outbox', 1);
      req.onupgradeneeded = function() { req.result.createObjectStore('kv'); };
      req.onsuccess = function() { resolve(req.result); };
      req.onerror = function() { reject(req.error); };
    });
  }
  return dbPromise;
}

// Resolves once the transaction has committed, not when its request succeeds.
function kvRequest(mode, fn) {
  return openDB().then(function(db) {
    return new Promise(function(resolve, reject) {
      var tx = db.transaction('kv', mode);
      var req = fn(tx.objectStore('kv'));
      tx.oncomplete = function() { resolve(req.result); };
      tx.onerror = function() { reject(tx.error || req.error); };
      tx.onabort = function() { reject(tx.error || new Error('IndexedDB transaction aborted')); };
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
    if (e.name !== 'AbortError') setError(null, e);
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
  savedHandle = null;
  state.outbox.folder = handle.name;
  state.outbox.status = 'other-tab';
  var w = newWatcher(handle);
  current = w;
  // One watcher per origin: other tabs, and a watcher replacing this one, wait
  // for the lock, which is held until the run (and any push in it) has ended.
  navigator.locks.request('warpgate-outbox', function() {
    if (w.stopped) return;
    state.outbox.status = 'watching';
    return w.run();
  });
}

function stop() {
  if (current) { current.stop(); current = null; }
}

function setError(w, err) {
  if (w && w.stopped) return;
  state.outbox.status = 'error';
  state.outbox.error = err.message || String(err);
}

// --- watching

function newWatcher(folder) {
  var w = { folder: folder, stopped: false, kicked: false, wake: null, observer: null, firstSeen: {} };
  w.kick = function() {
    w.kicked = true;
    if (w.wake) w.wake();
  };
  w.stop = function() {
    w.stopped = true;
    if (w.observer) { w.observer.disconnect(); w.observer = null; }
    w.kick();
  };
  w.run = function() { return watch(w); };
  return w;
}

// Resolves after ms, or early on kick(). Called only after awaiting file system
// calls, so hidden-tab throttling never sees a chain of timers.
function sleep(w, ms) {
  return new Promise(function(resolve) {
    if (w.kicked) { resolve(); return; }
    var t = setTimeout(resolve, ms);
    w.wake = function() { clearTimeout(t); resolve(); };
  });
}

function delay(ms) {
  return new Promise(function(resolve) { setTimeout(resolve, ms); });
}

async function watch(w) {
  if ('FileSystemObserver' in window) {
    w.observer = new FileSystemObserver(w.kick);
    try { await w.observer.observe(w.folder); } catch (e) { w.observer = null; }
  }
  while (!w.stopped) {
    w.kicked = false;
    if (state.outbox.status === 'watching' || state.outbox.status === 'error') {
      try {
        await delay(PICKUP_DELAY_MS);
        await scan(w);
        if (!w.stopped && state.outbox.status === 'error') state.outbox.status = 'watching';
      } catch (e) {
        setError(w, e);
      }
    }
    if (!w.stopped) await sleep(w, SCAN_MS);
  }
}

async function scan(w) {
  var files = {};
  for await (var entry of w.folder.entries()) {
    if (entry[1].kind === 'file') files[entry[0]] = entry[1];
  }
  var names = Object.keys(files).sort();
  for (var i = 0; i < names.length; i++) {
    if (w.stopped) return;
    if (names[i].endsWith('.sha256')) await processPush(w, names[i].slice(0, -'.sha256'.length), files);
  }
  if (!w.stopped) await removeLeftovers(w.folder, names, files);
}

// --- one push

function parseHashFile(text) {
  var lines = text.split(/\r?\n/);
  var m = /^([0-9a-fA-F]{64})(\s|$)/.exec(lines[0].trim());
  return m ? { hash: m[1].toLowerCase(), title: lines[1] || '' } : null;
}

async function processPush(w, name, files) {
  var folder = w.folder;
  var hashName = name + '.sha256';
  var now = Date.now();
  if (!w.firstSeen[hashName]) w.firstSeen[hashName] = now;
  var waited = now - w.firstSeen[hashName];

  // TextDecoder drops a leading UTF-8 BOM, which Windows PowerShell 5.1 always writes.
  var parsed = parseHashFile(new TextDecoder().decode(await (await files[hashName].getFile()).arrayBuffer()));
  if (!parsed) return retryOrFail(w, name, files, waited, 'invalid hash file');
  if (pushedHere[hashName] === parsed.hash ||
      await kvGet('pushed:' + hashName).catch(function() { return null; }) === parsed.hash) {
    return removePushed(folder, name);
  }
  if (!files[name]) return retryOrFail(w, name, files, waited, 'content file missing');
  var file = await files[name].getFile();
  if (file.size > MAX_BYTES) return fail(w, name, files, 'too large: ' + file.size + ' bytes, limit ' + MAX_BYTES);
  var buf = await file.arrayBuffer();
  if (await sha256Hex(buf) !== parsed.hash) return retryOrFail(w, name, files, waited, 'hash mismatch');
  var text;
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(buf);
  } catch (e) {
    return fail(w, name, files, 'not UTF-8 text');
  }

  var title = sanitizeClipTitle(parsed.title);
  var id;
  try {
    id = await createNote(title, text, 'outbox');
  } catch (e) {
    if (e.status === 401) return; // the page is returning to the gate
    return retryOrFail(w, name, files, waited, 'server error (' + e.message + ')');
  }
  // Recorded before deleting, so a hash file that survives a failed delete
  // isn't pushed again: in this tab even if IndexedDB fails, after a reload
  // only if it didn't. A create whose response was lost is retried, though.
  pushedHere[hashName] = parsed.hash;
  delete w.firstSeen[hashName];
  addToast(true, 'Note #' + id + (title ? ' "' + title + '"' : '') + ' pushed from outbox');
  try {
    await kvSet('pushed:' + hashName, parsed.hash);
  } catch (e) {
    addToast(false, name + ': pushed, but recording that failed (' + (e && e.message) + '); it is pushed again after a reload if its files remain');
  }
  reloadClipboard();
  await removePushed(folder, name);
}

function retryOrFail(w, name, files, waited, msg) {
  if (waited < FAIL_AFTER_MS) return;
  return fail(w, name, files, msg);
}

async function fail(w, name, files, msg) {
  delete w.firstSeen[name + '.sha256'];
  addToast(false, name + ': ' + msg);
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
    delete pushedHere[name + '.sha256'];
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
