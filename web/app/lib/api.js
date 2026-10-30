// Request/response plumbing shared by every tab: auth headers, the
// encode/decode choke points for parameter encryption, and WS helpers that
// depend on the store.
import { API_BASE, settings, state, authKeyHash, paramEncKeyBytes } from '../store.js';
import { encryptParams, decryptParams } from './paramcrypt.js';

export { API_BASE, decryptParams };

export function authHeaders() {
  var h = {};
  if (settings.auth.key) { h['X-Auth'] = authKeyHash.value; }
  return h;
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

// Any other status means the auth check passed (it runs before routing).
export function noteAuthStatus(res) {
  state.authFailed = res.status === 401;
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
