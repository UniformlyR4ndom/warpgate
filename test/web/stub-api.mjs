// Stand-in for web/app/lib/api.js: records requests and lets a test program the
// response per URL, so submitClip can be driven through failure and retry
// without a real server.
export const mock = {
  calls: [],
  responder: null,
  reset(responder) { this.calls = []; this.responder = responder; },
  count(suffix, method) {
    return this.calls.filter(function(c) {
      return c.url.endsWith(suffix) && (!method || c.method === method);
    }).length;
  },
};

export function authHeaders() { return {}; }
export function jsonHeaders() { return {}; }
export function encodeBody(o) { return JSON.stringify(o); }
export function paramEncryptEnabled() { return false; }
export async function decodeResponseJSON(res) { return res._json; }
export function assertOk(res) {
  if (!res.ok) throw Object.assign(new Error('HTTP ' + res.status), { status: res.status });
}
export async function apiFetch(url, opts) {
  mock.calls.push({ url: url, method: (opts && opts.method) || 'GET' });
  return mock.responder(url, opts);
}

export function ok(json) { return { ok: true, status: 200, _json: json || {} }; }
export function fail(status) { return { ok: false, status: status }; }

export async function apiWSOpen(path) { return mock.wsOpen(path); }
export function noteWSAck(ack) { return ack; }
export function sendWSControl(ws, obj) { ws.send(JSON.stringify(obj)); }
export function wsDecryptFor() { return null; }
export function closeWSSession() {}
