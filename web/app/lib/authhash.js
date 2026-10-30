// Mirrors internal/authhash.Hash: a labeled HMAC-SHA256 round seeds 1000
// rounds of plain SHA-256. The raw key is never sent to the server; this
// derives the X-Auth token value. sha256 is the vendored js-sha256 global.
import { bytesToHex } from './util.js';

// js-sha256 accepts Uint8Array directly; .array() returns a plain Array.
export function hmacSha256Bytes(keyBytes, msgBytes) {
  return new Uint8Array(sha256.hmac.array(keyBytes, msgBytes));
}

export async function hashAuthKey(key) {
  var keyBytes = new TextEncoder().encode(key);
  var seed = hmacSha256Bytes(keyBytes, new TextEncoder().encode('warpgate-auth-token-v1'));
  var data = new Uint8Array(await window.crypto.subtle.digest('SHA-256', seed));
  for (var i = 1; i < 1000; i++) {
    data = new Uint8Array(await window.crypto.subtle.digest('SHA-256', data));
  }
  return bytesToHex(data);
}
