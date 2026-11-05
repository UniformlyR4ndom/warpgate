// The outbox against a fake folder, Web Locks and IndexedDB: the lock is held
// through an in-flight push, and a failed marker write plus a failed delete
// don't lead to a second note.
import assert from 'node:assert/strict';
import { state } from './stub-store.mjs';
import { mock, ok } from './stub-api.mjs';

let failures = 0;
async function test(name, fn) {
  try { await fn(); console.log('ok   - ' + name); }
  catch (err) { failures++; console.log('FAIL - ' + name + '\n       ' + (err && err.stack || err)); }
}

// Exclusive Web Locks: a request waits for the previous holder's callback promise.
var lockTail = Promise.resolve();
Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { locks: {
  request(name, cb) {
    var run = lockTail.then(function() { return cb(); });
    lockTail = run.catch(function() {});
    return run;
  },
} } });

var idb = { data: new Map(), failPut: function() { return false; } };
globalThis.indexedDB = {
  open() {
    var db = { transaction() {
      var tx = { error: null };
      function op(fn) {
        var req = {};
        setTimeout(function() {
          try { req.result = fn(); req.onsuccess && req.onsuccess(); }
          catch (e) { req.error = tx.error = e; req.onerror && req.onerror(); tx.onerror && tx.onerror(); return; }
          setTimeout(function() { tx.oncomplete && tx.oncomplete(); }, 0);
        }, 0);
        return req;
      }
      tx.objectStore = function() { return {
        get(k) { return op(function() { return idb.data.get(k); }); },
        put(v, k) { return op(function() { if (idb.failPut(k)) throw new Error('quota'); idb.data.set(k, v); }); },
        delete(k) { return op(function() { idb.data.delete(k); }); },
      }; };
      return tx;
    } };
    var req = { result: db };
    setTimeout(function() { req.onsuccess(); }, 0);
    return req;
  },
};

var observers = [];
globalThis.FileSystemObserver = globalThis.window.FileSystemObserver = function(cb) {
  observers.push(cb);
  this.observe = async function() {};
  this.disconnect = function() {};
};
function kickAll() { observers.forEach(function(cb) { cb(); }); }

function fakeFolder(files, undeletable) {
  return {
    name: 'outbox',
    files: files,
    entries: async function*() {
      for (const n of Object.keys(files)) yield [n, fileHandle(n)];
    },
    async removeEntry(n) {
      if (undeletable && undeletable(n)) throw Object.assign(new Error('locked'), { name: 'NoModificationAllowedError' });
      if (!(n in files)) throw Object.assign(new Error('gone'), { name: 'NotFoundError' });
      delete files[n];
    },
  };
  function fileHandle(n) {
    return {
      kind: 'file',
      async getFile() {
        var b = new TextEncoder().encode(files[n]);
        return { size: b.length, lastModified: Date.now(), arrayBuffer: async function() { return b.buffer; } };
      },
      async move(to) { files[to] = files[n]; delete files[n]; },
    };
  }
}

async function sha256(text) {
  var d = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
  return Array.from(new Uint8Array(d)).map(function(x) { return x.toString(16).padStart(2, '0'); }).join('');
}

function until(cond, ms) {
  var end = Date.now() + (ms || 3000);
  return new Promise(function(resolve, reject) {
    (function poll() {
      if (cond()) return resolve();
      if (Date.now() > end) return reject(new Error('timed out waiting'));
      setTimeout(poll, 10);
    })();
  });
}

Object.assign(state, { outbox: { supported: true, status: 'off', folder: '', error: '', showScript: false }, toasts: [] });
const { connectOutbox, disconnectOutbox } = await import('../../web/app/lib/outbox.js');

function notesCreated() { return mock.count('/api/clip/new'); }

await test('the lock is held until an in-flight push has finished', async () => {
  var files = { 'push-a': 'hello' };
  files['push-a.sha256'] = await sha256('hello');
  var folder = fakeFolder(files);
  var release;
  var pending = new Promise(function(r) { release = r; });
  mock.reset(function(url) {
    if (url.endsWith('/api/clip/new')) return pending.then(function() { return ok({ id: 1 }); });
    return ok({ rev: 0 });
  });
  window.showDirectoryPicker = async function() { return folder; };

  await connectOutbox();
  await until(function() { return notesCreated() === 1; });
  await disconnectOutbox();
  await connectOutbox();
  kickAll();
  await new Promise(function(r) { setTimeout(r, 600); });
  assert.equal(notesCreated(), 1, 'the new watcher must wait while the push is in flight');
  assert.equal(state.outbox.status, 'other-tab');

  release();
  await until(function() { return !('push-a.sha256' in files); });
  await until(function() { return state.outbox.status === 'watching'; });
  kickAll();
  await new Promise(function(r) { setTimeout(r, 600); });
  assert.equal(notesCreated(), 1, 'pushed once');
  await disconnectOutbox();
});

await test('a failed marker write and a failed delete do not push twice', async () => {
  var files = { 'push-b': 'world' };
  files['push-b.sha256'] = await sha256('world');
  var folder = fakeFolder(files, function(n) { return n === 'push-b.sha256'; });
  idb.failPut = function(k) { return k.indexOf('pushed:') === 0; };
  state.toasts = [];
  mock.reset(function(url) {
    if (url.endsWith('/api/clip/new')) return ok({ id: 2 });
    return ok({ rev: 0 });
  });
  window.showDirectoryPicker = async function() { return folder; };

  await connectOutbox();
  await until(function() { return notesCreated() === 1; });
  await until(function() { return state.toasts.some(function(t) { return !t.ok; }); }, 6000);
  kickAll();
  await new Promise(function(r) { setTimeout(r, 4500); }); // past the delete retries
  assert.equal(notesCreated(), 1, 'pushed once');
  assert.ok('push-b.sha256' in files, 'the undeletable hash file is still there');
  await disconnectOutbox();
  idb.failPut = function() { return false; };
});

if (failures) { console.error('\n' + failures + ' test(s) failed'); process.exit(1); }
console.log('\nall outbox tests passed');
process.exit(0);
