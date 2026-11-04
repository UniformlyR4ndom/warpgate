// Regression tests for finding #1 (code-qa-report): a transform setting changed
// mid-transfer must not affect an in-flight transfer. snapshotTransform() is
// taken once at transfer start and every encode/decode/arg call takes that
// snapshot explicitly, so later mutation of `settings` is ignored.
import assert from 'node:assert/strict';
import { settings } from './stub-store.mjs';
import { snapshotTransform, decrypt, buildTransformArgs, perRequestSecrets,
         encodeBytesForUpload } from '../../web/app/lib/transform.js';

let failures = 0;
async function test(name, fn) {
  try { await fn(); console.log('ok   - ' + name); }
  catch (err) { failures++; console.log('FAIL - ' + name + '\n       ' + (err && err.message)); }
}

function setXor(key) {
  settings.transform = { type: 'xor', xor: { key: key },
    aesCtr: { key: '', iv: '' }, pdf: { filename: '', password: { enabled: false } } };
}

// The report's exact repro: plaintext 0x65, XOR key 0x01 → wire byte 0x64.
// Decoding with the snapshot (key 01) yields 0x65; the live-settings bug would
// use the changed key 02 and yield 0x66.
await test('decrypt uses the snapshot key, not a key changed after snapshot', async () => {
  setXor('01');
  const tx = snapshotTransform();
  settings.transform.xor.key = '02';          // mutate the live setting
  const wire = Uint8Array.of(0x65 ^ 0x01);     // 0x64, what the server sent for key 01
  const out = await decrypt(tx, wire.buffer, null, null);
  assert.equal(out[0], 0x65, 'decoded byte should be the original plaintext');
});

await test('decrypt ignores a transform TYPE changed after snapshot', async () => {
  setXor('0a');
  const tx = snapshotTransform();
  settings.transform.type = 'aes-ctr';         // switch type on the live setting
  const wire = Uint8Array.of(0x65 ^ 0x0a);
  const out = await decrypt(tx, wire.buffer, null, null);
  assert.equal(out[0], 0x65);
});

await test('buildTransformArgs reflects the snapshot, not later mutation', () => {
  setXor('01');
  const tx = snapshotTransform();
  settings.transform.xor.key = '02';
  assert.deepEqual(buildTransformArgs(tx, null, null), { key: '01' });
});

await test('encodeBytesForUpload encodes with the snapshot and reports matching args', async () => {
  setXor('01');
  const tx = snapshotTransform();
  settings.transform.xor.key = '02';
  const enc = await encodeBytesForUpload(tx, Uint8Array.of(0x65), 'f');
  assert.equal(enc.args.key, '01', 'declared args must match the snapshot');
  const bytes = new Uint8Array(await enc.data.arrayBuffer());
  assert.equal(bytes[0], 0x65 ^ 0x01, 'bytes must be encoded with the snapshot key');
});

// aes-ctr round-trip through one snapshot survives a mid-transfer settings change
// (also exercises the aesjs global wiring).
await test('aes-ctr encode then decrypt round-trips via a single snapshot', async () => {
  settings.transform = { type: 'aes-ctr',
    xor: { key: '' },
    aesCtr: { key: '00112233445566778899aabbccddeeff', iv: '0102030405060708090a0b0c0d0e0f10' },
    pdf: { filename: '', password: { enabled: false } } };
  const tx = snapshotTransform();
  settings.transform.aesCtr.key = 'ffffffffffffffffffffffffffffffff';  // mutate live
  settings.transform.type = 'id';
  const plain = Uint8Array.of(1, 2, 3, 4, 5);
  const enc = await encodeBytesForUpload(tx, plain, 'f');
  const dec = await decrypt(tx, await enc.data.arrayBuffer(), null, null);
  assert.deepEqual(Array.from(dec), Array.from(plain));
});

await test('perRequestSecrets reads the snapshot for per-request key generation', () => {
  const htmlTx = { type: 'html-b64', xor: { key: '' }, aesCtr: { key: '', iv: '' },
    pdf: { filename: '', password: { enabled: false } } };
  const s = perRequestSecrets(htmlTx);
  assert.equal(typeof s.htmlB64Key, 'string');
  assert.equal(s.htmlB64Key.length, 32, 'html-b64 key is 16 random bytes as hex');
  assert.equal(s.password, null);

  const pdfTx = { type: 'pdf', xor: { key: '' }, aesCtr: { key: '', iv: '' },
    pdf: { filename: 'a.bin', passwordEnabled: true } };
  assert.equal(perRequestSecrets(pdfTx).password.length, 20, 'pdf password is 10 random bytes as hex');
});

if (failures) { console.error('\n' + failures + ' test(s) failed'); process.exit(1); }
console.log('\nall transform snapshot tests passed');
