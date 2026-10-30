// Client-side parameter encryption: base64(nonce[16] || AES-CTR(json) ||
// HMAC-SHA256[32]). Reuses the vendored aesjs; keys come from the store.
import { paramEncKeyBytes, paramMacKeyBytes } from '../store.js';
import { hmacSha256Bytes } from './authhash.js';
import { bytesToBinaryString, binaryStringToBytes } from './util.js';

export function encryptParams(obj) {
  var plaintext = new TextEncoder().encode(JSON.stringify(obj));
  var nonce = new Uint8Array(16);
  window.crypto.getRandomValues(nonce); // fine without a secure context, unlike crypto.subtle
  var ctr = new aesjs.ModeOfOperation.ctr(Array.from(paramEncKeyBytes.value), new aesjs.Counter(Array.from(nonce)));
  var combined = new Uint8Array(16 + plaintext.length);
  combined.set(nonce, 0);
  combined.set(ctr.encrypt(plaintext), 16);
  var mac = hmacSha256Bytes(Array.from(paramMacKeyBytes.value), Array.from(combined));
  var full = new Uint8Array(combined.length + mac.length);
  full.set(combined, 0);
  full.set(mac, combined.length);
  return btoa(bytesToBinaryString(full));
}

function constantTimeEqualBytes(a, b) {
  if (a.length !== b.length) return false;
  var diff = 0;
  for (var i = 0; i < a.length; i++) { diff |= a[i] ^ b[i]; }
  return diff === 0;
}

export function decryptParams(blob) {
  var raw = binaryStringToBytes(atob(blob));
  if (raw.length < 16 + 32) throw new Error('response authentication failed');
  var body = raw.slice(0, raw.length - 32);
  var mac  = raw.slice(raw.length - 32);
  var expected = hmacSha256Bytes(Array.from(paramMacKeyBytes.value), Array.from(body));
  if (!constantTimeEqualBytes(expected, mac)) throw new Error('response authentication failed');
  var nonce = body.slice(0, 16);
  var ciphertext = body.slice(16);
  var ctr = new aesjs.ModeOfOperation.ctr(Array.from(paramEncKeyBytes.value), new aesjs.Counter(Array.from(nonce)));
  return JSON.parse(new TextDecoder().decode(ctr.decrypt(ciphertext)));
}
