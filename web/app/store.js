import { deepSignal } from './vendor/deepsignal.module.js';
import { signal, effect } from './vendor/signals.module.js';
import { hashAuthKey, hmacSha256Bytes } from './lib/authhash.js';

export const API_BASE = window.location.pathname.replace(/\/[^/]*$/, '');

const SETTINGS_STORAGE_KEY = 'warpgate-settings';
const SETTINGS_VERSION = 1;
// The auth key is persisted separately, in sessionStorage, so it doesn't
// outlive the browser session/tab.
const AUTH_KEY_STORAGE_KEY = 'warpgate-auth-key';

// Persisted config. Keep serializable; auth.key is carved into sessionStorage.
export const settings = deepSignal({
  auth: { key: '' },
  paramCrypt: { enabled: false },
  backend: 'http',
  autoRefresh: false,
  confirmDelete: true,
  clipSortDesc: true,
  clipSearchScope: 'all',
  wsFrameSizeKB: 4096,
  contentType: { preset: 'auto', custom: '' },
  extOverride: { ext: '', mode: 'replace' },
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

// Transient UI state (never persisted).
export const state = deepSignal({
  tab: 'download',
  // One file browser per root: dl = download root, ul = upload directory.
  browse: {
    dl: { segments: [], entries: [], error: null },
    ul: { segments: [], entries: [], error: null },
  },
  newDirName: null, // null = the upload tab's "New folder" form is closed
  authFailed: false,
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
  authShowKey: false,
  wsFrameMinKB: 1,
  wsFrameMaxKB: 262144,
  hasFileSystemAccess: typeof window.showSaveFilePicker === 'function',
  // status: off | reconnect | other-tab | watching | auth | error
  outbox: { supported: false, status: 'off', folder: '', error: '', events: [], notices: [], showScript: false },
});

// Derived from settings.auth.key, recomputed on change (not per request).
export const authKeyHash = signal('');
export const paramEncKeyBytes = signal(null);
export const paramMacKeyBytes = signal(null);

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

// Only copies keys that already exist on target, ignoring stale/unknown fields.
function mergeSettings(target, source) {
  if (!source || typeof source !== 'object') return;
  for (var key in source) {
    if (!Object.prototype.hasOwnProperty.call(source, key)) continue;
    if (!Object.prototype.hasOwnProperty.call(target, key)) continue;
    var sVal = source[key];
    var tVal = target[key];
    if (sVal && typeof sVal === 'object' && !Array.isArray(sVal) && tVal && typeof tVal === 'object') {
      mergeSettings(tVal, sVal);
    } else {
      target[key] = sVal;
    }
  }
}

function loadSettings() {
  try {
    var raw = localStorage.getItem(SETTINGS_STORAGE_KEY);
    var parsed = raw ? JSON.parse(raw) : null;
    if (parsed && parsed.version === SETTINGS_VERSION && parsed.settings) {
      mergeSettings(settings, parsed.settings);
    }
  } catch(e) {}
  try {
    settings.auth.key = sessionStorage.getItem(AUTH_KEY_STORAGE_KEY) || '';
  } catch(e) {}
}

function saveSettings() {
  try {
    var toStore = JSON.parse(JSON.stringify(settings));
    toStore.auth.key = '';
    localStorage.setItem(SETTINGS_STORAGE_KEY, JSON.stringify({ version: SETTINGS_VERSION, settings: toStore }));
  } catch(e) {}
  try {
    if (settings.auth.key) sessionStorage.setItem(AUTH_KEY_STORAGE_KEY, settings.auth.key);
    else sessionStorage.removeItem(AUTH_KEY_STORAGE_KEY);
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

function recomputeDerivedKeys(key) {
  if (!key) {
    paramEncKeyBytes.value = null;
    paramMacKeyBytes.value = null;
    authKeyHash.value = '';
    return Promise.resolve();
  }
  var keyBytes = new TextEncoder().encode(key);
  paramEncKeyBytes.value = hmacSha256Bytes(keyBytes, new TextEncoder().encode('warpgate-param-enc-v1'));
  paramMacKeyBytes.value = hmacSha256Bytes(keyBytes, new TextEncoder().encode('warpgate-param-mac-v1'));
  return hashAuthKey(key).then(function(h) {
    // Guard against a race if the key changed again mid-hash.
    if (settings.auth.key === key) authKeyHash.value = h;
  });
}

// Wires persistence + derivations and fetches server settings. `onAuthKeyChange`
// (optional) runs after the derived keys are refreshed on a key change, for
// reloading listings. Returns a promise resolved once the initial auth hash is ready.
export async function initStore(onAuthKeyChange) {
  loadSettings();
  applyAuto();
  await recomputeDerivedKeys(settings.auth.key);

  // React to later key changes: recompute, then notify.
  var first = true;
  effect(function() {
    var key = settings.auth.key; // subscribe
    if (first) { first = false; return; }
    recomputeDerivedKeys(key).then(function() {
      if (onAuthKeyChange) onAuthKeyChange();
    });
  });

  // Persist on any settings change (reading the whole tree subscribes).
  var firstSave = true;
  effect(function() {
    JSON.stringify(settings); // touch every field to subscribe
    if (firstSave) { firstSave = false; return; }
    saveSettings();
  });

  fetch(API_BASE + '/api/public/checkin').catch(function() {});
  fetch(API_BASE + '/api/public/settings')
    .then(function(res) { return res.ok ? res.json() : null; })
    .then(function(data) {
      if (!data) return;
      state.ulFlat = !!data.ulFlat;
      state.wsFrameMinKB = Math.ceil(data.wsFrameMin / 1024);
      state.wsFrameMaxKB = Math.floor(data.wsFrameMax / 1024);
      if (settings.wsFrameSizeKB > state.wsFrameMaxKB) settings.wsFrameSizeKB = state.wsFrameMaxKB;
      if (settings.wsFrameSizeKB < state.wsFrameMinKB) settings.wsFrameSizeKB = state.wsFrameMinKB;
    })
    .catch(function() {});
}
