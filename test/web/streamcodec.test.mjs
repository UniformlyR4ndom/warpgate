// newStreamCodec over arbitrary piece boundaries must match the whole-buffer
// coding the server applies per request, for every streaming transform.
import assert from 'node:assert/strict';
import { newStreamCodec, decrypt, encodeBytesForUpload } from '../../web/app/lib/transform.js';

let failures = 0;
async function test(name, fn) {
  try { await fn(); console.log('ok   - ' + name); }
  catch (err) { failures++; console.log('FAIL - ' + name + '\n       ' + (err && err.stack || err)); }
}

const tx = {
  type: '', xor: { key: '0123456789abcdef01' }, aesCtr: { key: '00112233445566778899aabbccddeeff', iv: '0102030405060708090a0b0c0d0e0f10' },
  pdf: { filename: '', passwordEnabled: false },
};
const data = new Uint8Array(100003).map((_, i) => (i * 31 + 7) & 0xff);

function piecewise(codec, bytes, seed) {
  const out = new Uint8Array(bytes.length);
  let off = 0, x = seed;
  while (off < bytes.length) {
    x = (x * 1103515245 + 12345) & 0x7fffffff;
    const n = Math.min(bytes.length - off, 1 + (x % 5000));
    out.set(codec(bytes.subarray(off, off + n)), off);
    off += n;
  }
  return out;
}

for (const type of ['id', 'xor', 'aes-ctr']) {
  await test(type + ': piecewise coding equals whole-buffer coding', async () => {
    const t = Object.assign({}, tx, { type });
    const whole = new Uint8Array(await (await encodeBytesForUpload(t, data, 'f')).data.arrayBuffer());
    for (const seed of [1, 2, 3]) {
      assert.deepEqual(piecewise(newStreamCodec(t), data, seed), whole, 'encode, seed ' + seed);
      assert.deepEqual(piecewise(newStreamCodec(t), whole, seed), await decrypt(t, whole.buffer), 'decode, seed ' + seed);
    }
  });
}

await test('pdf and html-b64 have no stream codec', async () => {
  assert.equal(newStreamCodec(Object.assign({}, tx, { type: 'pdf' })), null);
  assert.equal(newStreamCodec(Object.assign({}, tx, { type: 'html-b64' })), null);
});

if (failures) { console.error('\n' + failures + ' test(s) failed'); process.exit(1); }
console.log('\nall stream codec tests passed');
