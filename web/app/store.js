import { deepSignal } from './vendor/deepsignal.module.js';
import { signal, effect } from './vendor/signals.module.js';
import { deriveKey, authToken, fingerprint, labelBytes } from './lib/authhash.js';
import { bytesToHex, hexToBytes, chunkSizeMB } from './lib/util.js';

export const API_BASE = window.location.pathname.replace(/\/[^/]*$/, '');

const SETTINGS_STORAGE_KEY = 'warpgate-settings';
const SETTINGS_VERSION = 1;
// The derived key K lives in sessionStorage, so it doesn't outlive the
// browser tab; the passphrase itself is never stored.
const AUTH_K_STORAGE_KEY = 'warpgate-auth-k';
const LEGACY_AUTH_KEY_STORAGE_KEY = 'warpgate-auth-key';

// Persisted config. Keep serializable.
export const settings = deepSignal({
  paramCrypt: { enabled: false },
  backend: 'http',
  autoRefresh: false,
  confirmDelete: true,
  clipSortDesc: true,
  clipSearchScope: 'all',
  wsFrameSizeKB: 4096,
  contentType: { preset: 'auto', custom: '' },
  extOverride: { ext: '', mode: 'replace' },
  outboxPath: '',
  transform: {
    type: 'id',
    xor:    { auto: true,  key: '' },
    aesCtr: { autoKey: true, key: '', autoIv: true, iv: '' },
    pdf:    { filename: '', password: { enabled: false } },
  },
  chunking: { enabled: false, chunkSize: 100, reassemble: false },
  uploadChunking: { enabled: false, chunkSizeMB: 100 },
  benchmark: { sizeMB: 10, chunkSizeMB: 4 },
  panel: {
    open: true, authOpen: true, benchmarkOpen: true,
    transformOpen: true, chunkingOpen: true, generalOpen: true, outboxOpen: true,
  },
});

// Transient UI state; only `tab` is kept, in sessionStorage.
export const state = deepSignal({
  tab: 'download',
  // One file browser per root: dl = download root, ul = upload directory.
  browse: {
    dl: { segments: [], entries: [], error: null },
    ul: { segments: [], entries: [], error: null },
  },
  newDirName: null, // null = the upload tab's "New folder" form is closed
  authFailed: false,
  authBlocked: false,
  // Auth section: "Change" form open, input, derivation progress (0..1, null = idle).
  authEditing: false,
  authPassphrase: '',
  authDeriving: null,
  authError: '',
  kdfSalt: '',
  uploads: true,
  ulFlat: false,
  uploadEntries: [],
  uploadError: null,
  transfers: [],
  benchmarkResults: [],
  benchmarkRunning: false,
  benchmarkProgress: null,
  benchmarkPreparing: false,
  benchmarkPrepProgress: null,
  clipItems: [],
  clipCursor: 0,
  clipImages: {},
  clipLightbox: null,
  clipLightboxZoom: false,
  pendingAttachments: [],
  clipRemovedAttachments: [],
  clipCaptionEdits: {},
  clipText: '',
  clipTitle: '',
  clipEditId: null,
  clipSearch: '',
  clipError: null,
  clipSubmitting: false,
  authShowKey: false,
  wsFrameMinKB: 1,
  wsFrameMaxKB: 262144,
  hasFileSystemAccess: typeof window.showSaveFilePicker === 'function',
  // status: off | reconnect | other-tab | watching | auth | error
  outbox: { supported: false, status: 'off', folder: '', error: '', showScript: false },
  toasts: [],
});

// All derived from K by setAuthK, not per request. authToken is '' without a key.
export const authKeyHash = signal('');
export const authFingerprint = signal('');
export const paramEncKeyBytes = signal(null);
export const paramMacKeyBytes = signal(null);
export const paramRespEncKeyBytes = signal(null);
export const paramRespMacKeyBytes = signal(null);

export const backendTypes = [
  { id: 'http', label: 'HTTP POST' },
  { id: 'ws',   label: 'Websocket' },
];

export const transformTypes = [
  { id: 'id',       label: 'id'           },
  { id: 'xor',      label: 'XOR'          },
  { id: 'aes-ctr',  label: 'AES'          },
  { id: 'pdf',      label: 'PDF'          },
  { id: 'html-b64', label: 'HTML (base64)' },
];

export const clipSearchScopes = [
  { id: 'all',     label: 'Title + content' },
  { id: 'title',   label: 'Title'           },
  { id: 'content', label: 'Content'         },
];

export const extOverrideModes = [
  { id: 'replace', label: 'Replace' },
  { id: 'append',  label: 'Append'  },
];

function isPlainObject(v) {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

// Restores only keys that exist in the defaults, and only values of the
// default's type, so stale or damaged storage falls back per field.
function mergeSettings(target, source) {
  if (!isPlainObject(source)) return;
  for (var key in source) {
    if (!Object.prototype.hasOwnProperty.call(source, key)) continue;
    if (!Object.prototype.hasOwnProperty.call(target, key)) continue;
    var sVal = source[key];
    var tVal = target[key];
    if (isPlainObject(tVal)) {
      mergeSettings(tVal, sVal);
    } else if (typeof sVal === typeof tVal) {
      target[key] = sVal;
    }
  }
}

// The first choice is the default.
function oneOf(obj, key, choices) {
  var ids = choices.map(function(c) { return c.id; });
  if (ids.indexOf(obj[key]) === -1) obj[key] = ids[0];
}

function normalizeSettings() {
  settings.chunking.chunkSize = chunkSizeMB(settings.chunking.chunkSize, 100);
  settings.uploadChunking.chunkSizeMB = chunkSizeMB(settings.uploadChunking.chunkSizeMB, 100);
  oneOf(settings, 'backend', backendTypes);
  oneOf(settings.transform, 'type', transformTypes);
  oneOf(settings, 'clipSearchScope', clipSearchScopes);
  oneOf(settings.extOverride, 'mode', extOverrideModes);
}

function loadSettings() {
  try {
    var raw = localStorage.getItem(SETTINGS_STORAGE_KEY);
    var parsed = raw ? JSON.parse(raw) : null;
    if (parsed && parsed.version === SETTINGS_VERSION && parsed.settings) {
      mergeSettings(settings, parsed.settings);
    }
  } catch(e) {}
  normalizeSettings();
}

function loadAuthK() {
  try {
    sessionStorage.removeItem(LEGACY_AUTH_KEY_STORAGE_KEY);
    var hex = sessionStorage.getItem(AUTH_K_STORAGE_KEY) || '';
    if (/^[0-9a-f]{64}$/.test(hex)) return hexToBytes(hex);
  } catch(e) {}
  return null;
}

function saveSettings() {
  try {
    localStorage.setItem(SETTINGS_STORAGE_KEY, JSON.stringify({ version: SETTINGS_VERSION, settings: settings }));
  } catch(e) {}
}

// Auto-generate keys/IVs for transforms whose "auto" flag is set.
export function applyAuto() {
  var xor = settings.transform.xor;
  if (xor.auto) { xor.key = randomHex32(); }
  var aes = settings.transform.aesCtr;
  if (aes.autoKey) { aes.key = randomHex32(); }
  if (aes.autoIv)  { aes.iv  = randomHex16(); }
}

function randomHex32() { return hex(32); }
function randomHex16() { return hex(16); }
function hex(n) {
  var b = new Uint8Array(n);
  window.crypto.getRandomValues(b);
  return Array.from(b).map(function(x) { return ('0' + x.toString(16)).slice(-2); }).join('');
}

function applyAuthK(k) {
  authKeyHash.value = k ? authToken(k) : '';
  authFingerprint.value = k ? fingerprint(k) : '';
  paramEncKeyBytes.value = k ? labelBytes(k, 'warpgate-param-enc-v2') : null;
  paramMacKeyBytes.value = k ? labelBytes(k, 'warpgate-param-mac-v2') : null;
  paramRespEncKeyBytes.value = k ? labelBytes(k, 'warpgate-param-resp-enc-v2') : null;
  paramRespMacKeyBytes.value = k ? labelBytes(k, 'warpgate-param-resp-mac-v2') : null;
}

var onKeyChange = null;

// Installs K (null: no key), persists it and resets the auth status.
function setAuthK(k) {
  applyAuthK(k);
  try {
    if (k) sessionStorage.setItem(AUTH_K_STORAGE_KEY, bytesToHex(k));
    else sessionStorage.removeItem(AUTH_K_STORAGE_KEY);
  } catch(e) {}
  state.authFailed = false;
  state.authBlocked = false;
  if (onKeyChange) onKeyChange();
}

var settingsPromise = null;

// Resolves once /api/public/settings has been applied (or failed).
export function serverSettingsLoaded() {
  return settingsPromise;
}

// Derives K from the passphrase form's input; an empty passphrase means no
// key (servers started with -no-auth).
export async function submitPassphrase() {
  if (state.authDeriving !== null) return;
  var passphrase = state.authPassphrase;
  state.authError = '';
  if (!passphrase) {
    finishPassphrase(null);
    return;
  }
  state.authDeriving = 0;
  try {
    var salt = state.kdfSalt || (await settingsPromise, state.kdfSalt);
    if (!salt) throw new Error('could not load the key salt from the server');
    var k = await deriveKey(passphrase, salt, function(p) {
      p = Math.floor(p * 20) / 20;
      if (p !== state.authDeriving) state.authDeriving = p;
    });
    finishPassphrase(k);
  } catch (e) {
    state.authError = e.message;
  } finally {
    state.authDeriving = null;
  }
}

function finishPassphrase(k) {
  state.authPassphrase = '';
  state.authShowKey = false;
  state.authEditing = false;
  setAuthK(k);
}

export function clearAuthK() {
  state.authEditing = false;
  setAuthK(null);
}

// Wires persistence and fetches server settings. `onAuthKeyChange` (optional)
// runs after every key change, for reloading listings.
export function initStore(onAuthKeyChange) {
  loadSettings();
  applyAuto();
  var k = loadAuthK();
  applyAuthK(k);
  onKeyChange = onAuthKeyChange || null;

  // Persist on any settings change (reading the whole tree subscribes).
  var firstSave = true;
  effect(function() {
    JSON.stringify(settings); // touch every field to subscribe
    if (firstSave) { firstSave = false; return; }
    saveSettings();
  });

  fetch(API_BASE + '/api/public/checkin').catch(function() {});
  settingsPromise = fetch(API_BASE + '/api/public/settings')
    .then(function(res) { return res.ok ? res.json() : null; })
    .then(function(data) {
      if (!data) return;
      state.kdfSalt = /^[0-9a-f]+$/.test(data.kdfSalt || '') ? data.kdfSalt : '';
      state.uploads = data.uploads !== false;
      state.ulFlat = !!data.ulFlat;
      state.wsFrameMinKB = Math.ceil(data.wsFrameMin / 1024);
      state.wsFrameMaxKB = Math.floor(data.wsFrameMax / 1024);
      if (settings.wsFrameSizeKB > state.wsFrameMaxKB) settings.wsFrameSizeKB = state.wsFrameMaxKB;
      if (settings.wsFrameSizeKB < state.wsFrameMinKB) settings.wsFrameSizeKB = state.wsFrameMinKB;
    })
    .catch(function() {});
}
