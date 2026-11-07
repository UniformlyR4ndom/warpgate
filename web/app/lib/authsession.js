// Shared by the login gate and the app. Must not import the store: the gate
// runs before the app's modules are loaded.
import { bytesToHex, hexToBytes } from './util.js';

export const API_BASE = window.location.pathname.replace(/\/[^/]*$/, '');

// The derived key K lives in sessionStorage, so it doesn't outlive the
// browser tab; the passphrase itself is never stored.
const AUTH_K_STORAGE_KEY = 'warpgate-auth-k';
const LEGACY_AUTH_KEY_STORAGE_KEY = 'warpgate-auth-key';
const REJECTED_STORAGE_KEY = 'warpgate-auth-rejected';

export function loadAuthK() {
  try {
    sessionStorage.removeItem(LEGACY_AUTH_KEY_STORAGE_KEY);
    var hex = sessionStorage.getItem(AUTH_K_STORAGE_KEY) || '';
    if (/^[0-9a-f]{64}$/.test(hex)) return hexToBytes(hex);
  } catch(e) {}
  return null;
}

export function saveAuthK(k) {
  try { sessionStorage.setItem(AUTH_K_STORAGE_KEY, bytesToHex(k)); } catch(e) {}
}

export function dropAuthK() {
  try { sessionStorage.removeItem(AUTH_K_STORAGE_KEY); } catch(e) {}
}

// Back to the gate. Reloading unloads the app with all its state.
export function logout(rejected) {
  dropAuthK();
  try { if (rejected) sessionStorage.setItem(REJECTED_STORAGE_KEY, '1'); } catch(e) {}
  window.location.reload();
}

// Whether the app was left because the server rejected the key; reads once.
export function takeRejected() {
  try {
    var v = sessionStorage.getItem(REJECTED_STORAGE_KEY) === '1';
    sessionStorage.removeItem(REJECTED_STORAGE_KEY);
    return v;
  } catch(e) {
    return false;
  }
}

// The server's uniform refusal (wrong path, IP firewall): empty 200 text/html.
export function isBlankRefusal(res) {
  return res.status === 200 &&
    res.headers.get('Content-Length') === '0' &&
    res.headers.get('Content-Type') === 'text/html; charset=utf-8';
}

var reRegistering = null;

// POSTs to the base path, which registers this IP when the firewall registers
// there and registration is open; a 303 means admitted.
export function reRegister() {
  if (!reRegistering) {
    reRegistering = fetch(API_BASE + '/', { method: 'POST', redirect: 'manual', cache: 'no-store' })
      .then(function(res) { return res.type === 'opaqueredirect'; }, function() { return false; })
      .finally(function() { reRegistering = null; });
  }
  return reRegistering;
}
