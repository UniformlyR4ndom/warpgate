// Request/response plumbing shared by every tab: auth headers, the
// encode/decode choke points for parameter encryption, and WS helpers that
// depend on the store.
import { API_BASE, settings, state, authKeyHash, paramEncKeyBytes } from '../store.js';
import { encryptParams, decryptParams } from './paramcrypt.js';
import { wsOpen } from './ws.js';

export { API_BASE, decryptParams };

export function authHeaders() {
  var h = {};
  if (authKeyHash.value) { h['X-Auth'] = authKeyHash.value; }
  return h;
}

function authRejectedError() {
  return Object.assign(new Error('The server rejected the auth key; set it again in the settings'), { status: 401 });
}

// Every authenticated request goes through here. After a 401 nothing is sent
// until the key changes, so polling can't use up the server's failed-attempt
// budget.
export async function apiFetch(url, opts) {
  if (state.authFailed) throw authRejectedError();
  var res = await fetch(url, opts);
  if (res.status === 401) state.authFailed = true;
  state.authBlocked = res.status === 429;
  if (state.authBlocked) {
    throw Object.assign(new Error('Too many failed authentication attempts from this IP; wait up to 10 minutes or restart the server'), { status: 429 });
  }
  return res;
}

// WebSocket counterpart of apiFetch's gate.
export async function apiWSOpen(path) {
  if (state.authFailed) throw authRejectedError();
  return wsOpen(wsBaseURL() + path);
}

// Records an "unauthorized" handshake ack like an HTTP 401.
export function noteWSAck(ack) {
  if (!ack.ok && ack.error === 'unauthorized') state.authFailed = true;
  return ack;
}

export function jsonHeaders() {
  return Object.assign({ 'Content-Type': 'application/json' }, authHeaders());
}

export function paramEncryptEnabled() {
  return settings.paramCrypt.enabled && !!paramEncKeyBytes.value;
}

// The request-body value for obj: an encrypted blob when enabled, else JSON.
export function encodeBody(obj) {
  return paramEncryptEnabled() ? encryptParams(obj) : JSON.stringify(obj);
}

export function sendWSControl(ws, obj) {
  ws.send(encodeBody(obj));
}

export async function decodeResponseJSON(res, encrypted) {
  if (!encrypted) return res.json();
  return decryptParams(await res.text());
}

export function assertOk(res) {
  if (!res.ok) throw Object.assign(new Error('HTTP ' + res.status), { status: res.status });
}

export function wsBaseURL() {
  var proto = window.location.protocol === 'https:' ? 'wss://' : 'ws://';
  return proto + window.location.host + API_BASE;
}

// decrypt callback for WS acks when this connection's handshake was encrypted.
export function wsDecryptFor(encrypted) {
  return encrypted ? decryptParams : null;
}

// Ends a {ws} session object reused across chunk requests; no-op if never opened.
export function closeWSSession(wsSession) {
  if (!wsSession || !wsSession.ws) { return; }
  var ws = wsSession.ws;
  wsSession.ws = null;
  try { sendWSControl(ws, { done: true }); } catch(e) {}
  try { ws.close(); } catch(e) {}
}
