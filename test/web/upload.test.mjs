// Uploads read the file in slices (never whole), hash what they read, and the
// server receives data that decodes back to the file, for every path: single
// request, chunked HTTP, and WS.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { state, settings } from './stub-store.mjs';
import { mock, ok } from './stub-api.mjs';
import { decrypt } from '../../web/app/lib/transform.js';
import { uploadFiles } from '../../web/app/lib/upload.js';

let failures = 0;
async function test(name, fn) {
  try { await fn(); console.log('ok   - ' + name); }
  catch (err) { failures++; console.log('FAIL - ' + name + '\n       ' + (err && err.stack || err)); }
}

const MB = 1024 * 1024;
const content = new Uint8Array(20 * MB + 12345).map((_, i) => (i * 7 + (i >> 11)) & 0xff);
const hex = (b) => createHash('sha256').update(b).digest('hex');

// A File whose reads are recorded: the size of every arrayBuffer() call, on
// the file or any slice of it.
class TrackedBlob extends Blob {
  constructor(parts, reads, name) { super(parts); this.reads = reads; this.name = name; }
  slice(a, b) { return new TrackedBlob([super.slice(a, b)], this.reads, this.name); }
  arrayBuffer() { this.reads.push(this.size); return super.arrayBuffer(); }
}
function trackedFile(bytes, name) { return new TrackedBlob([bytes], [], name); }

Object.assign(state, { transfers: [], toasts: [], ulFlat: true, browse: { ul: { segments: [] }, dl: { segments: [] } }, uploadEntries: [] });
Object.assign(settings, { uploadChunking: { enabled: false, chunkSizeMB: 4 }, wsFrameSizeKB: 1024 });
const tx = (type) => ({ type, xor: { key: '0123456789abcdef01' }, aesCtr: { key: '00112233445566778899aabbccddeeff', iv: '0102030405060708090a0b0c0d0e0f10' },
  pdf: { filename: '', password: { enabled: false } }, htmlB64: {} });

// HTTP: decodes each request's file part with its own args, like the server.
function httpServer() {
  let received = [];
  mock.reset(async function(url, opts) {
    if (!url.endsWith('/api/files/upload')) return ok([]);
    const spec = JSON.parse(await opts.body.get('transform').text());
    const part = opts.body.get('file');
    const raw = await Blob.prototype.arrayBuffer.call(part);
    const t = Object.assign(tx(spec.transform), { xor: { key: (spec.args || {}).key }, aesCtr: spec.args || {} });
    received.push(await decrypt(t, raw, null, (spec.args || {}).key));
    if (spec['ul-done'] === false) return ok({ 'continuation-key': 'k' + received.length });
    const all = Buffer.concat(received.map((r) => Buffer.from(r)));
    received = [];
    return ok({ sha256: hex(all), name: 'f' });
  });
}

async function upload(type, f) {
  settings.transform = tx(type);
  state.toasts = [];
  await uploadFiles([f]);
  return state.toasts.map((t) => t.text);
}

for (const type of ['id', 'xor', 'aes-ctr', 'html-b64']) {
  for (const chunked of [false, true]) {
    await test(type + (chunked ? ' chunked' : ' single request') + ': piecewise reads, verified', async () => {
      httpServer();
      settings.backend = 'http';
      settings.uploadChunking.enabled = chunked;
      const f = trackedFile(content, 'f.bin');
      const toasts = await upload(type, f);
      assert.deepEqual(toasts, ['"f.bin" uploaded (verified)']);
      const limit = type === 'html-b64' ? (chunked ? 4 * MB : content.length) : 8 * MB;
      assert.ok(Math.max(...f.reads) <= limit, 'largest read ' + Math.max(...f.reads) + ' > ' + limit);
    });
  }
}

await test('an empty file is one verified request', async () => {
  httpServer();
  settings.uploadChunking.enabled = true;
  assert.deepEqual(await upload('xor', trackedFile(new Uint8Array(0), 'e')), ['"e" uploaded (verified)']);
  assert.equal(mock.count('/api/files/upload'), 1);
});

// WS: collects frames, decodes them as one piece at done, like the server.
function wsServer(frames) {
  mock.wsOpen = async function() {
    const listeners = {};
    let spec = null;
    const ws = {
      readyState: 1, bufferedAmount: 0,
      addEventListener(t, fn) { (listeners[t] = listeners[t] || []).push(fn); },
      removeEventListener(t, fn) { listeners[t] = (listeners[t] || []).filter((f) => f !== fn); },
      close() {},
      send(d) {
        if (typeof d !== 'string') { frames.push(new Uint8Array(d)); return; }
        const msg = JSON.parse(d);
        const reply = (o) => setTimeout(() => (listeners.message || []).forEach((fn) => fn({ data: JSON.stringify(o) })), 0);
        if (!msg.done) { spec = msg; reply({ ok: true }); return; }
        const raw = Buffer.concat(frames.map((f) => Buffer.from(f)));
        const t = Object.assign(tx(spec.transform), { xor: { key: (spec.args || {}).key }, aesCtr: spec.args || {} });
        decrypt(t, raw.buffer.slice(raw.byteOffset, raw.byteOffset + raw.length), null, (spec.args || {}).key)
          .then((plain) => reply({ ok: true, name: 'f', sha256: hex(plain) }));
      },
    };
    return ws;
  };
}

for (const type of ['id', 'xor', 'aes-ctr', 'html-b64']) {
  await test(type + ' over WS: piecewise reads, full frames, verified', async () => {
    const frames = [];
    wsServer(frames);
    settings.backend = 'ws';
    const f = trackedFile(content, 'f.bin');
    assert.deepEqual(await upload(type, f), ['"f.bin" uploaded (verified)']);
    if (type !== 'html-b64') {
      assert.ok(Math.max(...f.reads) <= 8 * MB, 'largest read ' + Math.max(...f.reads));
      assert.ok(frames.slice(0, -1).every((fr) => fr.length === 1024 * 1024), 'all but the last frame are full');
    }
  });
}

if (failures) { console.error('\n' + failures + ' test(s) failed'); process.exit(1); }
console.log('\nall upload tests passed');
