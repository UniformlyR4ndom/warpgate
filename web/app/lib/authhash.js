// Mirrors internal/authhash: scrypt turns the passphrase into the master key
// K; the auth token and all other subkeys are labeled HMACs of K.
// sha256 is the vendored js-sha256 global.
import { scryptAsync } from '../vendor/noble-hashes/scrypt.js';
import { bytesToHex, hexToBytes } from './util.js';

const SCRYPT = { N: 1 << 17, r: 8, p: 1, dkLen: 32 };

// js-sha256 accepts Uint8Array directly; .array() returns a plain Array.
export function hmacSha256Bytes(keyBytes, msgBytes) {
  return new Uint8Array(sha256.hmac.array(keyBytes, msgBytes));
}

export function labelBytes(k, label) {
  return hmacSha256Bytes(k, new TextEncoder().encode(label));
}

export async function deriveKey(passphrase, saltHex, onProgress) {
  return scryptAsync(passphrase, hexToBytes(saltHex), Object.assign({ onProgress: onProgress }, SCRYPT));
}

export function authToken(k)   { return bytesToHex(labelBytes(k, 'warpgate-auth-token-v2')); }
export function fingerprint(k) { return bytesToHex(labelBytes(k, 'warpgate-key-fingerprint-v1').slice(0, 2)); }
