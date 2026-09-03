pdfjsLib.GlobalWorkerOptions.workerSrc = 'pdf.worker.min.js';

function wsOpen(url) {
  return new Promise(function(resolve, reject) {
    var ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
    ws.addEventListener('open', function() { resolve(ws); }, { once: true });
    ws.addEventListener('error', function() { reject(new Error('WebSocket connection failed')); }, { once: true });
  });
}

function wsNextMessage(ws) {
  return new Promise(function(resolve, reject) {
    function cleanup() {
      ws.removeEventListener('message', onMessage);
      ws.removeEventListener('close', onClose);
      ws.removeEventListener('error', onError);
    }
    function onMessage(ev) { cleanup(); resolve(ev.data); }
    function onClose(ev)   { cleanup(); reject(new Error('WebSocket closed unexpectedly' + (ev.reason ? ': ' + ev.reason : ''))); }
    function onError()     { cleanup(); reject(new Error('WebSocket error')); }
    ws.addEventListener('message', onMessage);
    ws.addEventListener('close', onClose);
    ws.addEventListener('error', onError);
  });
}

// decrypt is null for a plaintext connection, or a bound Alpine method
// (this.decryptParams) for one whose handshake was encrypted — decided once
// per connection by the caller, not re-detected per message, since the
// server only ever encrypts an ack in reply to an encrypted request.
async function wsNextJSON(ws, decrypt) {
  var raw = await wsNextMessage(ws);
  if (typeof raw !== 'string') throw new Error('expected JSON control message, got binary');
  return decrypt ? decrypt(raw) : JSON.parse(raw);
}

// ws.send() queues data locally and returns immediately, so a tight send
// loop with no await point runs to completion in one synchronous stretch —
// the browser never gets a chance to repaint, and the whole file piles up
// in the socket's local buffer regardless of how fast the network actually
// drains it. Waiting for bufferedAmount to fall under maxBuffered before
// each send paces sends to real transport throughput and caps how much
// unsent data can queue in memory at once.
//
// The wait always resolves via a real timer, even when the buffer is
// already drained (the common case on a fast connection): an
// already-resolved promise only yields a microtask, and a chain of
// microtask-only awaits never gives the browser a chance to paint. Without
// forcing a real macrotask here, a fast enough connection turns the whole
// send loop into one unbroken microtask stretch — the underlying progress
// data still updates correctly on every iteration, but nothing is ever
// painted until the transfer is already done, so the progress bar appears
// to jump straight from 0% to 100%.
function wsWaitForDrain(ws, maxBuffered) {
  return new Promise(function(resolve) {
    (function check() {
      if (ws.bufferedAmount <= maxBuffered) { setTimeout(resolve, 0); return; }
      setTimeout(check, 0);
    })();
  });
}

// Sent as several frameSize-sized messages instead of one, purely a
// wire-transport concern — the receiving end reassembles them before
// treating the data as a single unit.
async function sendWSBinary(ws, buffer, frameSize, onProgress) {
  var bytes = new Uint8Array(buffer);
  for (var offset = 0; offset < bytes.length; offset += frameSize) {
    await wsWaitForDrain(ws, frameSize);
    var slice = bytes.slice(offset, offset + frameSize);
    ws.send(slice);
    if (onProgress) onProgress(slice.byteLength);
  }
}

// The sending end may split the data across several binary messages instead
// of one, purely a wire-transport concern; keep reading until totalLen bytes
// have arrived.
async function receiveWSBinary(ws, totalLen, onProgress) {
  var out = new Uint8Array(totalLen);
  var received = 0;
  while (received < totalLen) {
    var raw = await wsNextMessage(ws);
    if (typeof raw === 'string') {
      var msg = JSON.parse(raw);
      throw new Error(msg.error || 'download failed');
    }
    var piece = new Uint8Array(raw);
    out.set(piece, received);
    received += piece.byteLength;
    if (onProgress) onProgress(piece.byteLength);
  }
  return out;
}

// Reads a fetch Response body incrementally so callers can report progress
// as bytes arrive, instead of only learning the total once the whole body
// has buffered (res.arrayBuffer()'s behavior).
async function readBodyWithProgress(response, onProgress) {
  if (!response.body || !response.body.getReader) {
    var buf = await response.arrayBuffer();
    if (onProgress) onProgress(buf.byteLength);
    return buf;
  }
  var reader = response.body.getReader();
  var chunks = [];
  var received = 0;
  while (true) {
    var result = await reader.read();
    if (result.done) break;
    chunks.push(result.value);
    received += result.value.byteLength;
    if (onProgress) onProgress(result.value.byteLength);
  }
  var out = new Uint8Array(received);
  var offset = 0;
  for (var i = 0; i < chunks.length; i++) {
    out.set(chunks[i], offset);
    offset += chunks[i].byteLength;
  }
  return out.buffer;
}

const SETTINGS_STORAGE_KEY = 'warpgate-settings';
const SETTINGS_VERSION = 1;
// The auth key is persisted separately, in sessionStorage rather than
// localStorage, so it doesn't outlive the browser session/tab.
const AUTH_KEY_STORAGE_KEY = 'warpgate-auth-key';

// Only copies keys that already exist on target, ignoring stale/unknown
// fields from an older schema version instead of grafting them on.
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

function filesApp() {
  let refreshTimer = null;

  return {
    pathSegments: [],
    entries: [],
    error: null,
    hasFileSystemAccess: typeof window.showSaveFilePicker === 'function',
    tab: 'download',
    uploadEntries: [],
    uploadError: null,
    transfers: [], // not persisted: active download/upload progress rows, one per in-flight file
    benchmarkResults: [],
    benchmarkRunning: false,
    benchmarkProgress: null, // not persisted: null (no bar) or a 0-1 fraction while a multi-step run is active
    benchmarkPreparing: false, // not persisted: true while generating/hashing test data, before the timed transfer starts
    benchmarkPrepProgress: null, // not persisted: 0-1 during chunked buffer generation (real signal), null during hashing (no incremental API)
    clipItems: [],
    clipText: '',
    clipEditId: null,
    clipError: null,
    authShowKey: false, // not persisted: always start masked
    authKeyHash: '', // not persisted: derived from settings.auth.key, recomputed on change
    paramEncKeyBytes: null, // not persisted: derived from settings.auth.key, recomputed on change
    paramMacKeyBytes: null, // not persisted: derived from settings.auth.key, recomputed on change
    wsFrameMinKB: 1,
    wsFrameMaxKB: 262144, // 256M; generous fallback ceiling until /api/public/settings responds

    backendTypes: [
      { id: 'http', label: 'HTTP POST' },
      { id: 'ws',   label: 'Websocket' },
    ],

    transformTypes: [
      { id: 'id',       label: 'id'           },
      { id: 'xor',      label: 'XOR'          },
      { id: 'aes-ctr',  label: 'AES'          },
      { id: 'pdf',      label: 'PDF'          },
      { id: 'html-b64', label: 'HTML (base64)' },
    ],

    // Everything here is persisted to localStorage as one JSON object — keep
    // values serializable and put new UI-configurable fields here, not top-level.
    // Exception: auth.key is carved out into sessionStorage instead — see
    // loadSettings/saveSettings.
    settings: {
      auth: { key: '' },
      paramCrypt: { enabled: false },
      backend: 'http',
      autoRefresh: false,
      confirmDelete: true,
      clipSortDesc: true, // notes tab: true = newest first (default), false = oldest first
      wsFrameSizeKB: 4096, // 4M default, clamped to the server's max
      contentType: {
        preset: 'auto', // 'auto' = let the transform pick, 'custom' = use `custom`, anything else = the literal value
        custom: '',
      },
      transform: {
        type: 'id',
        xor:    { auto: true,  key: '' },
        aesCtr: { autoKey: true, key: '', autoIv: true, iv: '' },
        pdf:    { filename: '', password: { enabled: false } },
      },
      chunking: {
        enabled:   false,
        chunkSize: 100,
        reassemble: false,
      },
      uploadChunking: {
        enabled:     false,
        chunkSizeMB: 100,
      },
      benchmark: {
        sizeMB: 10,
        chunkSizeMB: 4,
      },
      panel: {
        open: true,
        authOpen: true,
        benchmarkOpen: true,
        transformOpen: true,
        chunkingOpen: true,
        generalOpen: true,
      },
    },

    async init() {
      this.loadSettings();
      this.applyAuto();
      await this.updateAuthKeyHash();
      this.updateParamCryptKeys();
      this.load();

      // x-model on a <select> whose <option>s come from x-for tries to apply
      // the restored value before those options exist in the DOM yet; a
      // native <select>.value assignment to a not-yet-existing option is a
      // silent no-op with no automatic retry once the option appears.
      setTimeout(() => {
        this.$refs.backendSelect.value = this.settings.backend;
        this.$refs.transformTypeSelect.value = this.settings.transform.type;
      }, 0);

      fetch('/api/public/checkin').catch(() => {});
      fetch('/api/public/settings')
        .then(function(res) { return res.ok ? res.json() : null; })
        .then((data) => {
          if (!data) return;
          this.wsFrameMinKB = Math.ceil(data.wsFrameMin / 1024);
          this.wsFrameMaxKB = Math.floor(data.wsFrameMax / 1024);
          if (this.settings.wsFrameSizeKB > this.wsFrameMaxKB) { this.settings.wsFrameSizeKB = this.wsFrameMaxKB; }
          if (this.settings.wsFrameSizeKB < this.wsFrameMinKB) { this.settings.wsFrameSizeKB = this.wsFrameMinKB; }
        })
        .catch(() => {});

      this.applyAutoRefresh(this.settings.autoRefresh); // in case restored as true
      this.$watch('settings.autoRefresh', (enabled) => this.applyAutoRefresh(enabled));

      this.$watch('settings.auth.key', async () => {
        await this.updateAuthKeyHash();
        this.updateParamCryptKeys();
        this.load();
        if (this.tab === 'upload') this.loadUploadDir();
      });

      // Deep-watch the whole settings tree: evaluating JSON.stringify(settings)
      // reads every nested field, so this fires on any change anywhere in it.
      this.$watch('JSON.stringify(settings)', () => { this.saveSettings(); });
    },

    applyAutoRefresh(enabled) {
      clearInterval(refreshTimer);
      refreshTimer = enabled ? setInterval(() => this.refreshCurrentTab(), 1000) : null;
    },

    // Shared by the auto-refresh interval and the header's manual refresh button.
    refreshCurrentTab() {
      if (this.tab === 'download') this.load();
      else if (this.tab === 'upload') this.loadUploadDir();
      else if (this.tab === 'clipboard') this.loadClipboard();
    },

    loadSettings() {
      try {
        var raw = localStorage.getItem(SETTINGS_STORAGE_KEY);
        var parsed = raw ? JSON.parse(raw) : null;
        if (parsed && parsed.version === SETTINGS_VERSION && parsed.settings) {
          mergeSettings(this.settings, parsed.settings);
        }
      } catch(e) {}
      // Overwritten unconditionally (even a pre-existing localStorage blob
      // that still has a key in it must not leak the key back in).
      try {
        this.settings.auth.key = sessionStorage.getItem(AUTH_KEY_STORAGE_KEY) || '';
      } catch(e) {}
    },

    saveSettings() {
      try {
        var toStore = JSON.parse(JSON.stringify(this.settings));
        toStore.auth.key = ''; // persisted separately, see below
        localStorage.setItem(SETTINGS_STORAGE_KEY, JSON.stringify({ version: SETTINGS_VERSION, settings: toStore }));
      } catch(e) {}
      try {
        if (this.settings.auth.key) sessionStorage.setItem(AUTH_KEY_STORAGE_KEY, this.settings.auth.key);
        else sessionStorage.removeItem(AUTH_KEY_STORAGE_KEY);
      } catch(e) {}
    },

    bytesToHex(bytes) {
      return Array.from(bytes).map(function(b) {
        return ('0' + b.toString(16)).slice(-2);
      }).join('');
    },

    randomHex(byteCount) {
      var bytes = new Uint8Array(byteCount);
      window.crypto.getRandomValues(bytes);
      return this.bytesToHex(bytes);
    },

    async sha256Hex(buffer) {
      var digest = await window.crypto.subtle.digest('SHA-256', buffer);
      return this.bytesToHex(new Uint8Array(digest));
    },

    // The raw key is never sent to the server; this is the value that is.
    // Mirrors internal/authhash.Hash exactly: a labeled HMAC-SHA256 round
    // (domain-separating this from paramEncKeyBytes/paramMacKeyBytes below,
    // which are derived from the same raw key via their own distinct
    // labels) seeds 1000 rounds of plain SHA-256.
    async hashAuthKey(key) {
      var keyBytes = new TextEncoder().encode(key);
      var seed = this.hmacSha256Bytes(keyBytes, new TextEncoder().encode('warpgate-auth-token-v1'));
      var data = new Uint8Array(await window.crypto.subtle.digest('SHA-256', seed));
      for (var i = 1; i < 1000; i++) {
        data = new Uint8Array(await window.crypto.subtle.digest('SHA-256', data));
      }
      return this.bytesToHex(data);
    },

    // Cached rather than recomputed per request — 1000 sequential digest
    // calls per fetch/WS message would add real latency.
    async updateAuthKeyHash() {
      this.authKeyHash = this.settings.auth.key ? await this.hashAuthKey(this.settings.auth.key) : '';
    },

    // js-sha256 (not crypto.subtle — parameter encryption must work over
    // plain HTTP, where crypto.subtle is unavailable) accepts Uint8Array
    // directly; .array() returns a plain Array, wrapped here for callers
    // that treat it as bytes.
    hmacSha256Bytes(keyBytes, msgBytes) {
      return new Uint8Array(sha256.hmac.array(keyBytes, msgBytes));
    },

    // Derives the two subkeys used to encrypt request control-parameter
    // JSON blobs (see encryptParams below), via labeled HMAC-SHA256 from
    // the same raw auth key used for X-Auth — but with distinct labels, so
    // this derivation is unrelated to (and can't be reversed into)
    // authKeyHash. Cached like authKeyHash, recomputed only on key change.
    updateParamCryptKeys() {
      if (!this.settings.auth.key) {
        this.paramEncKeyBytes = null;
        this.paramMacKeyBytes = null;
        return;
      }
      var keyBytes = new TextEncoder().encode(this.settings.auth.key);
      this.paramEncKeyBytes = this.hmacSha256Bytes(keyBytes, new TextEncoder().encode('warpgate-param-enc-v1'));
      this.paramMacKeyBytes = this.hmacSha256Bytes(keyBytes, new TextEncoder().encode('warpgate-param-mac-v1'));
    },

    authHeaders() {
      var h = {};
      if (this.settings.auth.key) { h['X-Auth'] = this.authKeyHash; }
      return h;
    },

    jsonHeaders() {
      return Object.assign({ 'Content-Type': 'application/json' }, this.authHeaders());
    },

    paramEncryptEnabled() {
      return this.settings.paramCrypt.enabled && !!this.paramEncKeyBytes;
    },

    // Encrypts obj as the wire format the server auto-detects: base64(nonce
    // || AES-CTR(obj) || HMAC-SHA256(nonce||ciphertext)). Reuses the same
    // aes-js CTR construction as the AES-CTR file-content transform
    // (decryptAESCTR below); the nonce is drawn fresh per call since a
    // (key, nonce) pair must never repeat under CTR.
    encryptParams(obj) {
      var plaintext = new TextEncoder().encode(JSON.stringify(obj));
      var nonce = new Uint8Array(16);
      window.crypto.getRandomValues(nonce); // fine without a secure context, unlike crypto.subtle
      var ctr = new aesjs.ModeOfOperation.ctr(Array.from(this.paramEncKeyBytes), new aesjs.Counter(Array.from(nonce)));
      var combined = new Uint8Array(16 + plaintext.length);
      combined.set(nonce, 0);
      combined.set(ctr.encrypt(plaintext), 16);
      var mac = this.hmacSha256Bytes(Array.from(this.paramMacKeyBytes), Array.from(combined));
      var full = new Uint8Array(combined.length + mac.length);
      full.set(combined, 0);
      full.set(mac, combined.length);
      return btoa(this.bytesToBinaryString(full));
    },

    // Fixed-time byte comparison for MAC verification — avoids leaking how
    // many leading bytes matched via a short-circuiting compare.
    constantTimeEqualBytes(a, b) {
      if (a.length !== b.length) return false;
      var diff = 0;
      for (var i = 0; i < a.length; i++) { diff |= a[i] ^ b[i]; }
      return diff === 0;
    },

    // Reverses encryptParams: decodes+verifies+decrypts a server response
    // blob back into the JSON value it was built from. The server only ever
    // encrypts a response when the request it's answering arrived encrypted
    // (see decodeResponseJSON/sendWSControl below for how callers know to
    // call this instead of parsing plain JSON).
    decryptParams(blob) {
      var raw = this.binaryStringToBytes(atob(blob));
      if (raw.length < 16 + 32) throw new Error('response authentication failed');
      var body = raw.slice(0, raw.length - 32);
      var mac  = raw.slice(raw.length - 32);
      var expected = this.hmacSha256Bytes(Array.from(this.paramMacKeyBytes), Array.from(body));
      if (!this.constantTimeEqualBytes(expected, mac)) throw new Error('response authentication failed');
      var nonce = body.slice(0, 16);
      var ciphertext = body.slice(16);
      var ctr = new aesjs.ModeOfOperation.ctr(Array.from(this.paramEncKeyBytes), new aesjs.Counter(Array.from(nonce)));
      return JSON.parse(new TextDecoder().decode(ctr.decrypt(ciphertext)));
    },

    // The value to use as a request body for obj: an encrypted blob when
    // parameter encryption is enabled, plain JSON otherwise. Every JSON
    // request body and WS control message routes through this (or
    // sendWSControl below, which wraps it) instead of calling
    // JSON.stringify directly, so the server's auto-detect always sees a
    // consistent choice.
    encodeBody(obj) {
      return this.paramEncryptEnabled() ? this.encryptParams(obj) : JSON.stringify(obj);
    },

    sendWSControl(ws, obj) {
      ws.send(this.encodeBody(obj));
    },

    // Parses a fetch Response body as JSON, decrypting first if the request
    // it's answering was itself encrypted (the server only ever encrypts a
    // response under that condition, so callers must know and pass it in —
    // there's no auto-detection on this side, unlike the server's).
    async decodeResponseJSON(res, encrypted) {
      if (!encrypted) return res.json();
      return this.decryptParams(await res.text());
    },

    assertOk(res) {
      if (!res.ok) throw Object.assign(new Error('HTTP ' + res.status), { status: res.status });
    },

    wsBaseURL() {
      var proto = window.location.protocol === 'https:' ? 'wss://' : 'ws://';
      return proto + window.location.host;
    },

    applyAuto() {
      var xor = this.settings.transform.xor;
      if (xor.auto) { xor.key = this.randomHex(32); }

      var aes = this.settings.transform.aesCtr;
      if (aes.autoKey) { aes.key = this.randomHex(32); }
      if (aes.autoIv)  { aes.iv  = this.randomHex(16); }
    },

    onAutoChange(field) {
      var xor = this.settings.transform.xor;
      var aes = this.settings.transform.aesCtr;
      if (field === 'xor'         && xor.auto)    { xor.key = this.randomHex(32); }
      if (field === 'aes-ctr-key' && aes.autoKey) { aes.key = this.randomHex(32); }
      if (field === 'aes-ctr-iv'  && aes.autoIv)  { aes.iv  = this.randomHex(16); }
    },

    formatSize(bytes) {
      if (bytes === null || bytes === undefined) return '';
      if (bytes < 1024)               return bytes.toFixed(2) + ' B';
      if (bytes < 1024 * 1024)        return (bytes / 1024).toFixed(2) + ' KB';
      if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(2) + ' MB';
      return (bytes / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
    },

    hexToBytes(hex) {
      var bytes = new Uint8Array(hex.length / 2);
      for (var i = 0; i < bytes.length; i++) {
        bytes[i] = parseInt(hex.substr(i * 2, 2), 16);
      }
      return bytes;
    },

    applyXOR(data, key) {
      var result = new Uint8Array(data.length);
      for (var i = 0; i < data.length; i++) {
        result[i] = data[i] ^ key[i % key.length];
      }
      return result;
    },

    bytesToBinaryString(bytes) {
      var binary = '';
      for (var i = 0; i < bytes.length; i++) { binary += String.fromCharCode(bytes[i]); }
      return binary;
    },

    binaryStringToBytes(binary) {
      var bytes = new Uint8Array(binary.length);
      for (var i = 0; i < binary.length; i++) { bytes[i] = binary.charCodeAt(i); }
      return bytes;
    },

    decryptAESCTR(buffer) {
      var t    = this.settings.transform.aesCtr;
      var key  = Array.from(this.hexToBytes(t.key));
      var iv   = Array.from(this.hexToBytes(t.iv));
      var data = new Uint8Array(buffer);
      var aesCtr = new aesjs.ModeOfOperation.ctr(key, new aesjs.Counter(iv));
      return Promise.resolve(aesCtr.decrypt(data));
    },

    decryptHTMLB64(buffer, keyHex) {
      var html = new TextDecoder().decode(new Uint8Array(buffer));
      var prefix = 'src="data:image/jpeg;base64,';
      var start = html.indexOf(prefix);
      if (start === -1) { return Promise.reject(new Error('html-b64: payload not found')); }
      start += prefix.length;
      var end = html.indexOf('"', start);
      if (end === -1) { return Promise.reject(new Error('html-b64: unterminated payload')); }
      var b64 = html.slice(start, end);
      var bytes = this.binaryStringToBytes(atob(b64));
      return Promise.resolve(this.applyXOR(bytes, this.hexToBytes(keyHex)));
    },

    decryptPDF(buffer, password) {
      var opts = { data: new Uint8Array(buffer) };
      if (password) { opts.password = password; }
      return pdfjsLib.getDocument(opts).promise.then(function(pdf) {
        return pdf.getAttachments();
      }).then(function(attachments) {
        if (!attachments) throw new Error('PDF has no attachments');
        var keys = Object.keys(attachments);
        if (!keys.length) throw new Error('PDF has no attachments');
        return new Uint8Array(attachments[keys[0]].content);
      });
    },

    decrypt(buffer, password, htmlB64Key) {
      var t = this.settings.transform;
      if (t.type === 'xor')      { return Promise.resolve(this.applyXOR(new Uint8Array(buffer), this.hexToBytes(t.xor.key))); }
      if (t.type === 'aes-ctr')  { return this.decryptAESCTR(buffer); }
      if (t.type === 'pdf')      { return this.decryptPDF(buffer, password); }
      if (t.type === 'html-b64') { return this.decryptHTMLB64(buffer, htmlB64Key); }
      return Promise.resolve(new Uint8Array(buffer));
    },

    buildTransformArgs(password, htmlB64Key) {
      var t = this.settings.transform;
      if (t.type === 'xor')      return { key: t.xor.key };
      if (t.type === 'aes-ctr')  return { key: t.aesCtr.key, iv: t.aesCtr.iv };
      if (t.type === 'html-b64') return { key: htmlB64Key };
      if (t.type === 'pdf') {
        var args = { filename: t.pdf.filename || 'attachment' };
        if (password) { args.password = password; }
        return args;
      }
      return {};
    },

    padIndex(i) {
      return ('000' + i).slice(-3);
    },

    addTransfer(direction, name) {
      var t = { id: Date.now() + '-' + Math.random().toString(36).slice(2), direction: direction, name: name, progress: null };
      this.transfers.push(t);
      // Re-read the pushed element instead of returning `t` directly: Alpine's
      // reactivity wraps array elements in a proxy on access, and mutating the
      // raw `t` reference later (transfer.progress = ...) would bypass that
      // proxy's setter, silently updating the data without ever notifying
      // Alpine to re-render the bound x-show/x-text in the DOM.
      return this.transfers[this.transfers.length - 1];
    },

    removeTransfer(t) {
      var idx = this.transfers.indexOf(t);
      if (idx !== -1) { this.transfers.splice(idx, 1); }
    },

    // totalSize is the plaintext size from the listing; transforms that
    // expand their output (pdf, html-b64) make the real byte count run
    // slightly ahead of it, so the fraction is clamped rather than exact.
    // A non-positive totalSize (e.g. an empty file) has no meaningful
    // fraction, so the bar stays indeterminate instead of dividing by zero.
    makeTransferProgress(transfer, totalSize) {
      var bytesDone = 0;
      return function(n) {
        bytesDone += n;
        transfer.progress = totalSize > 0 ? Math.min(1, bytesDone / totalSize) : null;
      };
    },

    triggerDownload(data, filename) {
      var blob = new Blob([data]);
      var url  = URL.createObjectURL(blob);
      var a    = document.createElement('a');
      a.href     = url;
      a.download = filename;
      a.click();
      URL.revokeObjectURL(url);
    },

    // wsSession is a mutable cache ({ws, password, htmlB64Key}), reused across
    // chunk calls for the same file on the ws backend; unused on http.
    async fetchChunk(wsSession, path, start, size, onProgress) {
      if (this.settings.backend === 'ws') { return this.fetchChunkWS(wsSession, path, start, size, onProgress); }
      return this.fetchChunkHTTP(path, start, size, onProgress);
    },

    resolveContentTypeOverride() {
      var c = this.settings.contentType;
      if (c.preset === 'auto') return null;
      if (c.preset === 'custom') return c.custom.trim() || null;
      return c.preset;
    },

    async fetchChunkHTTP(path, start, size, onProgress) {
      var t = this.settings.transform;
      var password     = (t.type === 'pdf'      && t.pdf.password.enabled) ? this.randomHex(10) : null;
      var htmlB64Key   = (t.type === 'html-b64') ? this.randomHex(16) : null;
      var contentType  = this.resolveContentTypeOverride();
      var body = {
        path:      path,
        start:     start,
        size:      size,
        transform: t.type,
        args:      this.buildTransformArgs(password, htmlB64Key),
      };
      if (contentType) body.contentType = contentType;
      var res = await fetch('/api/files/download', {
        method:  'POST',
        headers: this.jsonHeaders(),
        body:    this.encodeBody(body),
      });
      this.assertOk(res);
      var buffer = await readBodyWithProgress(res, onProgress);
      return this.decrypt(buffer, password, htmlB64Key);
    },

    async fetchChunkWS(wsSession, path, start, size, onProgress) {
      if (!wsSession.ws) {
        var t          = this.settings.transform;
        var password   = (t.type === 'pdf'      && t.pdf.password.enabled) ? this.randomHex(10) : null;
        var htmlB64Key = (t.type === 'html-b64') ? this.randomHex(16) : null;

        var ws = await wsOpen(this.wsBaseURL() + '/api/files/ws/download');
        var encrypted = this.paramEncryptEnabled();
        this.sendWSControl(ws, {
          auth:      this.authKeyHash,
          path:      path,
          transform: t.type,
          args:      this.buildTransformArgs(password, htmlB64Key),
          frameSize: this.settings.wsFrameSizeKB * 1024,
        });
        var ack = await wsNextJSON(ws, encrypted ? this.decryptParams.bind(this) : null);
        if (!ack.ok) { ws.close(); throw new Error(ack.error || 'download failed'); }

        wsSession.ws         = ws;
        wsSession.password   = password;
        wsSession.htmlB64Key = htmlB64Key;
        wsSession.encrypted  = encrypted;
      }

      this.sendWSControl(wsSession.ws, { start: start, size: size });

      // The transformed chunk isn't always the same length as the requested
      // plaintext size (pdf/html-b64 both expand it), so the server
      // announces the actual byte count in a control message before sending
      // any frames; size it from that instead of the request's own size.
      var chunkAck = await wsNextJSON(wsSession.ws, wsSession.encrypted ? this.decryptParams.bind(this) : null);
      if (!chunkAck.ok) throw new Error(chunkAck.error || 'download failed');
      var chunkSize = chunkAck.chunkSize;

      var received = await receiveWSBinary(wsSession.ws, chunkSize, onProgress);
      return this.decrypt(received.buffer, wsSession.password, wsSession.htmlB64Key);
    },

    closeWSSession(wsSession) {
      if (!wsSession || !wsSession.ws) { return; }
      var ws = wsSession.ws;
      wsSession.ws = null;
      try { this.sendWSControl(ws, { done: true }); } catch(e) {}
      try { ws.close(); } catch(e) {}
    },

    async downloadFile(entry) {
      this.error = null;
      var path       = this.pathSegments.concat([entry.name]).join('/');
      var chunking   = this.settings.chunking;
      var wsSession  = { ws: null, password: null, htmlB64Key: null };
      var transfer   = this.addTransfer('download', entry.name);
      var onProgress = this.makeTransferProgress(transfer, entry.size);

      if (chunking.enabled) {
        var chunkBytes = chunking.chunkSize * 1024 * 1024;
        var numChunks  = Math.ceil(entry.size / chunkBytes);

        if (chunking.reassemble && this.hasFileSystemAccess) {
          try {
            var fileHandle = await window.showSaveFilePicker({ suggestedName: entry.name });
            var writable   = await fileHandle.createWritable();
            try {
              for (var i = 0; i < numChunks; i++) {
                var start = i * chunkBytes;
                var size  = Math.min(chunkBytes, entry.size - start);
                var data  = await this.fetchChunk(wsSession, path, start, size, onProgress);
                await writable.write(data);
              }
              await writable.close();
            } catch(err) {
              writable.abort();
              throw err;
            }
          } catch(err) {
            if (err && err.name !== 'AbortError') {
              this.error = 'Download failed: ' + err;
            }
          } finally {
            this.closeWSSession(wsSession);
            this.removeTransfer(transfer);
          }
        } else {
          var self = this;
          (async function() {
            try {
              for (var i = 0; i < numChunks; i++) {
                var start = i * chunkBytes;
                var size  = Math.min(chunkBytes, entry.size - start);
                var data  = await self.fetchChunk(wsSession, path, start, size, onProgress);
                self.triggerDownload(data, entry.name + '.' + self.padIndex(i));
              }
            } catch(err) {
              self.error = 'Chunk failed: ' + (err.status ? 'HTTP ' + err.status : err);
            } finally {
              self.closeWSSession(wsSession);
              self.removeTransfer(transfer);
            }
          })();
        }
      } else {
        try {
          var data = await this.fetchChunk(wsSession, path, 0, entry.size, onProgress);
          this.triggerDownload(data, entry.name);
        } catch(err) {
          this.error = 'Download failed: ' + (err.status ? 'HTTP ' + err.status : err);
        } finally {
          this.closeWSSession(wsSession);
          this.removeTransfer(transfer);
        }
      }
    },

    currentPath() {
      return this.pathSegments.join('/');
    },

    updateIfChanged(prop, data) {
      if (JSON.stringify(data) !== JSON.stringify(this[prop])) { this[prop] = data; }
    },

    async load() {
      this.error = null;
      try {
        var encrypted = this.paramEncryptEnabled();
        var res = await fetch('/api/files/list', {
          method:  'POST',
          headers: this.jsonHeaders(),
          body:    this.encodeBody({ path: this.currentPath() }),
        });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        var data = await this.decodeResponseJSON(res, encrypted);
        this.updateIfChanged('entries', data);
      } catch(err) {
        this.error = 'Failed to load directory (' + err.message + ')';
      }
    },

    enterDir(name) {
      this.pathSegments.push(name);
      this.load();
    },

    navigateTo(index) {
      this.pathSegments = this.pathSegments.slice(0, index + 1);
      this.load();
    },

    async loadUploadDir() {
      this.uploadError = null;
      try {
        var encrypted = this.paramEncryptEnabled();
        var url = '/api/files/ul-list' + (encrypted ? '?e=1' : '');
        var res = await fetch(url, { headers: this.authHeaders() });
        if (!res.ok) {
          this.uploadEntries = [];
          this.uploadError = res.status === 404
            ? 'Uploads are not enabled on this server.'
            : 'Failed to load upload directory (HTTP ' + res.status + ')';
          return;
        }
        var data = (await this.decodeResponseJSON(res, encrypted)) || [];
        this.updateIfChanged('uploadEntries', data);
      } catch(err) {
        this.uploadEntries = [];
        this.uploadError = 'Failed to load upload directory (' + err.message + ')';
      }
    },

    setTab(t) {
      this.tab = t;
      if (t === 'upload') this.loadUploadDir();
      if (t === 'clipboard') this.loadClipboard();
    },

    textToBase64(str) {
      var bytes = new TextEncoder().encode(str);
      return btoa(this.bytesToBinaryString(bytes));
    },

    base64ToText(b64) {
      return new TextDecoder().decode(this.binaryStringToBytes(atob(b64)));
    },

    formatTS(ts) {
      return new Date(ts * 1000).toLocaleString();
    },

    async copyClip(b64) {
      await navigator.clipboard.writeText(this.base64ToText(b64));
    },

    downloadClip(item) {
      var text = this.base64ToText(item.data);
      this.triggerDownload(new TextEncoder().encode(text), 'note-' + item.id + '.txt');
    },

    sortedClipItems() {
      var dir = this.settings.clipSortDesc ? -1 : 1;
      return this.clipItems.slice().sort(function(a, b) { return (a.ts - b.ts) * dir; });
    },

    async loadClipboard() {
      this.clipError = null;
      try {
        var encrypted = this.paramEncryptEnabled();
        var url = '/api/clip/list' + (encrypted ? '?e=1' : '');
        var res = await fetch(url, { headers: this.authHeaders() });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        var data = await this.decodeResponseJSON(res, encrypted);
        this.updateIfChanged('clipItems', data);
      } catch(err) {
        this.clipError = 'Failed to load clipboard (' + err.message + ')';
      }
    },

    async submitClip() {
      this.clipError = null;
      var editing = this.clipEditId !== null;
      try {
        var url  = editing ? '/api/clip/edit' : '/api/clip/new';
        var body = { data: this.textToBase64(this.clipText) };
        if (editing) { body.id = this.clipEditId; }
        var res = await fetch(url, {
          method:  'POST',
          headers: this.jsonHeaders(),
          body:    this.encodeBody(body),
        });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        this.clipText   = '';
        this.clipEditId = null;
        await this.loadClipboard();
      } catch(err) {
        this.clipError = (editing ? 'Failed to update note (' : 'Failed to submit note (') + err.message + ')';
      }
    },

    editClip(item) {
      this.clipText   = this.base64ToText(item.data);
      this.clipEditId = item.id;
      this.$refs.clipTextarea.focus();
    },

    cancelEdit() {
      this.clipText   = '';
      this.clipEditId = null;
    },


    async deleteClip(id) {
      if (this.settings.confirmDelete && !window.confirm('Delete this note?')) return;
      this.clipError = null;
      try {
        var res = await fetch('/api/clip/delete', {
          method:  'DELETE',
          headers: this.jsonHeaders(),
          body:    this.encodeBody({ id: id }),
        });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        await this.loadClipboard();
      } catch(err) {
        this.clipError = 'Failed to delete note (' + err.message + ')';
      }
    },

    createPDFWithAttachment(fileBytes, filename) {
      return PDFLib.PDFDocument.create().then(function(doc) {
        return doc.embedFont(PDFLib.StandardFonts.Helvetica).then(function(font) {
          var page = doc.addPage([595, 842]);
          page.drawText('Hello there!', { x: 100, y: 700, size: 12, font: font });
          return doc.attach(fileBytes, filename, { mimeType: 'application/octet-stream' });
        }).then(function() {
          return doc.save();
        });
      });
    },

    // Shared by the whole-file (encodeForUpload) and per-chunk upload paths:
    // each chunk of a chunked upload is transformed independently, calling
    // this once per chunk with that chunk's own byte range.
    async encodeBytesForUpload(bytes, filename) {
      var t = this.settings.transform;
      if (t.type === 'xor') {
        var encoded = this.applyXOR(bytes, this.hexToBytes(t.xor.key));
        return { data: new Blob([encoded]), args: { key: t.xor.key } };
      }
      if (t.type === 'aes-ctr') {
        var key    = Array.from(this.hexToBytes(t.aesCtr.key));
        var iv     = Array.from(this.hexToBytes(t.aesCtr.iv));
        var aesCtr = new aesjs.ModeOfOperation.ctr(key, new aesjs.Counter(iv));
        return { data: new Blob([aesCtr.encrypt(bytes)]), args: { key: t.aesCtr.key, iv: t.aesCtr.iv } };
      }
      if (t.type === 'pdf') {
        var pdfBytes = await this.createPDFWithAttachment(bytes, filename);
        return { data: new Blob([pdfBytes], { type: 'application/pdf' }), args: { filename: filename } };
      }
      if (t.type === 'html-b64') {
        var keyHex = this.randomHex(16);
        var xored  = this.applyXOR(bytes, this.hexToBytes(keyHex));
        var html = '<!DOCTYPE html><html><body>\n' +
                   '<img src="data:image/jpeg;base64,' + btoa(this.bytesToBinaryString(xored)) + '">\n' +
                   '</body></html>';
        return { data: new Blob([html], { type: 'text/html' }), args: { key: keyHex } };
      }
      return { data: new Blob([bytes]), args: {} };
    },

    async encodeForUpload(file) {
      var buf      = await file.arrayBuffer();
      var filename = this.settings.transform.pdf.filename || file.name;
      return this.encodeBytesForUpload(new Uint8Array(buf), filename);
    },

    async uploadFiles(files) {
      this.uploadError = null;
      var self = this;
      var promises = Array.from(files).map(async function(file) {
        var transfer   = self.addTransfer('upload', file.name);
        var onProgress = self.makeTransferProgress(transfer, file.size);
        try {
          if (self.settings.backend === 'ws') {
            await self.uploadFileWS(file, onProgress);
          } else {
            await self.uploadFileHTTP(file, onProgress);
          }
        } catch(err) {
          self.uploadError = 'Upload of "' + file.name + '" failed: ' +
            (err.status ? 'HTTP ' + err.status : err);
        } finally {
          self.removeTransfer(transfer);
        }
      });
      await Promise.all(promises);
      this.loadUploadDir();
    },

    // A single-request HTTP upload has no progress event (fetch doesn't
    // expose upload streaming), so onProgress only fires here when
    // forwarded to the chunked path below.
    async uploadFileHTTP(file, onProgress) {
      if (this.settings.uploadChunking.enabled) { return this.uploadFileHTTPChunked(file, onProgress); }

      var t      = this.settings.transform;
      var result = await this.encodeForUpload(file);
      var spec   = this.encodeBody({ transform: t.type, args: result.args, 'ul-done': true });
      var fd     = new FormData();
      fd.append('transform', new Blob([spec], { type: this.paramEncryptEnabled() ? 'text/plain' : 'application/json' }), 'transform.json');
      fd.append('file', result.data, file.name);
      var res = await fetch('/api/files/upload', { method: 'POST', headers: this.authHeaders(), body: fd });
      this.assertOk(res);
    },

    // Chunks are sent strictly one at a time, each awaited before the next
    // is encoded/sent — the server's per-filename lock rejects concurrent
    // requests outright rather than queuing them, and its continuation-key
    // only ever recognizes the immediately-next chunk.
    async uploadFileHTTPChunked(file, onProgress) {
      var t         = this.settings.transform;
      var chunkSize = this.settings.uploadChunking.chunkSizeMB * 1024 * 1024;
      var buf       = await file.arrayBuffer();
      var bytes     = new Uint8Array(buf);
      var filename  = t.pdf.filename || file.name;
      var encrypted = this.paramEncryptEnabled();

      var continuationKey = '';
      var offset = 0;
      var done   = false;
      while (!done) {
        var end   = Math.min(offset + chunkSize, bytes.length);
        var slice = bytes.subarray(offset, end);
        done      = end >= bytes.length;

        var result = await this.encodeBytesForUpload(slice, filename);
        var spec = this.encodeBody({
          transform: t.type,
          args: result.args,
          'continuation-key': continuationKey,
          'ul-done': done,
        });
        var fd = new FormData();
        fd.append('transform', new Blob([spec], { type: encrypted ? 'text/plain' : 'application/json' }), 'transform.json');
        fd.append('file', result.data, file.name);
        var res = await fetch('/api/files/upload', { method: 'POST', headers: this.authHeaders(), body: fd });
        this.assertOk(res);
        var data = await this.decodeResponseJSON(res, encrypted);
        continuationKey = data['continuation-key'] || '';

        if (onProgress) onProgress(slice.byteLength);
        offset = end;
      }
    },

    async uploadFileWS(file, onProgress) {
      var t         = this.settings.transform;
      var result    = await this.encodeForUpload(file);
      var buffer    = await result.data.arrayBuffer();
      var frameSize = this.settings.wsFrameSizeKB * 1024;
      var encrypted = this.paramEncryptEnabled();
      var decrypt   = encrypted ? this.decryptParams.bind(this) : null;

      var ws = await wsOpen(this.wsBaseURL() + '/api/files/ws/upload');
      try {
        this.sendWSControl(ws, {
          auth:      this.authKeyHash,
          filename:  file.name,
          transform: t.type,
          args:      result.args,
          frameSize: frameSize,
        });
        var ack = await wsNextJSON(ws, decrypt);
        if (!ack.ok) throw new Error(ack.error || 'upload failed');

        await sendWSBinary(ws, buffer, frameSize, onProgress);
        this.sendWSControl(ws, { done: true });

        var final = await wsNextJSON(ws, decrypt);
        if (!final.ok) throw new Error(final.error || 'upload failed');
      } finally {
        ws.close();
      }
    },

    triggerFileInput() {
      document.getElementById('ulFileInput').click();
    },

    // ── Benchmark tab ──
    // Exercises /api/sim/* (no transform, no filesystem access) to probe
    // which transport/frame size gets through a given proxy. Both directions
    // verify a SHA-256 hash: download's synthetic content is a pure function
    // of totalSize, so the client can compute the expected hash itself
    // without any network round trip; upload compares against the hash the
    // server reports back. A mismatch (not just a wrong byte count) is
    // reported as a failed run.

    // Mirrors simReader server-side: "<totalSize><space>" followed by 0xFF
    // padding out to totalSize. Deterministic, so the expected hash can be
    // computed locally before the download starts.
    buildExpectedSimFile(totalSize) {
      var buf = new Uint8Array(totalSize);
      buf.fill(0xff);
      var header = new TextEncoder().encode(String(totalSize) + ' ');
      buf.set(header, 0);
      return buf.buffer;
    },

    clampBenchmarkSettings() {
      var b = this.settings.benchmark;
      if (!(b.sizeMB >= 1))   { b.sizeMB = 1; }
      if (b.sizeMB > 1024)    { b.sizeMB = 1024; }
      if (!(b.chunkSizeMB >= 1)) { b.chunkSizeMB = 1; }
      if (b.chunkSizeMB > b.sizeMB) { b.chunkSizeMB = b.sizeMB; }
    },

    addBenchmarkResult(result) {
      result.id = Date.now() + '-' + Math.random().toString(36).slice(2);
      this.benchmarkResults.unshift(result);
    },

    formatBenchmarkError(err) {
      return err.status ? 'HTTP ' + err.status : String(err.message || err);
    },

    makeBenchmarkProgress(totalSize, hasSteps) {
      var self = this;
      var bytesDone = 0;
      return function(n) {
        bytesDone += n;
        if (hasSteps) self.benchmarkProgress = Math.min(1, bytesDone / totalSize);
      };
    },

    benchmarkResultBase(direction, backend, totalSize, frameSize, extra) {
      return Object.assign({ direction: direction, backend: backend, sizeBytes: totalSize, frameBytes: frameSize }, extra);
    },

    resetBenchmarkFlags() {
      this.benchmarkRunning = false;
      this.benchmarkProgress = null;
      this.benchmarkPreparing = false;
      this.benchmarkPrepProgress = null;
    },

    clearBenchmarkResults() {
      this.benchmarkResults = [];
    },

    formatDuration(ms) {
      return (ms / 1000).toFixed(2) + ' s';
    },

    formatThroughput(bytes, ms) {
      if (!(ms > 0)) return '-';
      var mbps = (bytes / (ms / 1000)) / (1024 * 1024);
      return mbps.toFixed(2) + ' MB/s';
    },

    formatChunkFrame(r) {
      var parts = [];
      if (r.chunkBytes) { parts.push('chunk ' + this.formatSize(r.chunkBytes)); }
      if (r.frameBytes) { parts.push('frame ' + this.formatSize(r.frameBytes)); }
      return parts.length ? parts.join(' / ') : '-';
    },

    async fetchSimChunkHTTP(totalSize, start, size, onProgress) {
      var res = await fetch('/api/sim/download', {
        method:  'POST',
        headers: this.jsonHeaders(),
        body:    this.encodeBody({ totalSize: totalSize, start: start, size: size }),
      });
      this.assertOk(res);
      var buffer = await res.arrayBuffer();
      if (buffer.byteLength !== size) {
        throw new Error('expected ' + size + ' bytes, got ' + buffer.byteLength);
      }
      if (onProgress) onProgress(buffer.byteLength);
      return new Uint8Array(buffer);
    },

    async fetchSimChunkWS(wsSession, totalSize, start, size, onProgress) {
      if (!wsSession.ws) {
        var ws = await wsOpen(this.wsBaseURL() + '/api/sim/ws/download');
        var encrypted = this.paramEncryptEnabled();
        this.sendWSControl(ws, {
          auth:      this.authKeyHash,
          totalSize: totalSize,
          frameSize: this.settings.wsFrameSizeKB * 1024,
        });
        var ack = await wsNextJSON(ws, encrypted ? this.decryptParams.bind(this) : null);
        if (!ack.ok) { ws.close(); throw new Error(ack.error || 'download failed'); }
        wsSession.ws = ws;
        wsSession.encrypted = encrypted;
      }

      this.sendWSControl(wsSession.ws, { start: start, size: size });

      return receiveWSBinary(wsSession.ws, size, onProgress);
    },

    async fetchSimChunk(wsSession, totalSize, start, size, onProgress) {
      if (this.settings.backend === 'ws') { return this.fetchSimChunkWS(wsSession, totalSize, start, size, onProgress); }
      return this.fetchSimChunkHTTP(totalSize, start, size, onProgress);
    },

    async runDownloadBenchmark() {
      if (this.benchmarkRunning) return;
      this.clampBenchmarkSettings();
      this.benchmarkRunning = true;

      var backend   = this.settings.backend;
      var totalSize = this.settings.benchmark.sizeMB * 1024 * 1024;
      var chunkSize = Math.min(this.settings.benchmark.chunkSizeMB * 1024 * 1024, totalSize);
      var frameSize = backend === 'ws' ? this.settings.wsFrameSizeKB * 1024 : null;
      var base      = this.benchmarkResultBase('download', backend, totalSize, frameSize, { chunkBytes: chunkSize });

      if (!window.crypto.subtle) {
        this.addBenchmarkResult(Object.assign({}, base, {
          durationMs: 0, ok: false,
          error: 'SHA-256 hashing requires a secure context (HTTPS or localhost)',
        }));
        this.resetBenchmarkFlags();
        return;
      }

      var wsSession = { ws: null };
      var startTime = null; // set once the network loop actually starts

      // A bar is only shown when the run has more than one real network
      // step: multiple HTTP chunk requests, or (WS) multiple chunks and/or
      // multiple frames within a chunk.
      var hasSteps = (backend === 'ws') ? (chunkSize < totalSize || frameSize < totalSize) : (chunkSize < totalSize);
      this.benchmarkProgress = hasSteps ? 0 : null;
      var onProgress = this.makeBenchmarkProgress(totalSize, hasSteps);

      try {
        // Deterministic from totalSize alone, so this can be computed before
        // any request goes out and isn't counted toward the transfer time.
        this.benchmarkPreparing = true;
        var expectedHash = await this.sha256Hex(this.buildExpectedSimFile(totalSize));
        this.benchmarkPreparing = false;

        var received = new Uint8Array(totalSize);
        startTime = performance.now();
        for (var offset = 0; offset < totalSize; offset += chunkSize) {
          var size  = Math.min(chunkSize, totalSize - offset);
          var chunk = await this.fetchSimChunk(wsSession, totalSize, offset, size, onProgress);
          received.set(chunk, offset);
        }
        var durationMs = performance.now() - startTime;

        var receivedHash = await this.sha256Hex(received.buffer);
        var hashMatch = receivedHash === expectedHash;

        this.addBenchmarkResult(Object.assign({}, base, {
          durationMs: durationMs, ok: hashMatch,
          error: hashMatch ? null : 'hash mismatch (data corrupted in transit)',
        }));
      } catch(err) {
        this.addBenchmarkResult(Object.assign({}, base, {
          durationMs: startTime !== null ? performance.now() - startTime : 0,
          ok: false,
          error: this.formatBenchmarkError(err),
        }));
      } finally {
        this.closeWSSession(wsSession);
        this.resetBenchmarkFlags();
      }
    },

    // crypto.getRandomValues rejects requests over 65536 bytes in one call,
    // so a large buffer needs many synchronous calls; yielding periodically
    // keeps the tab responsive instead of blocking the main thread for the
    // whole loop.
    async generateRandomBuffer(totalSize, onProgress) {
      var buf = new Uint8Array(totalSize);
      var CHUNK = 65536;
      var lastYield = performance.now();
      for (var offset = 0; offset < totalSize; offset += CHUNK) {
        var len = Math.min(CHUNK, totalSize - offset);
        window.crypto.getRandomValues(buf.subarray(offset, offset + len));
        if (onProgress) onProgress(len);
        if (performance.now() - lastYield > 16) {
          await new Promise(function(resolve) { setTimeout(resolve, 0); });
          lastYield = performance.now();
        }
      }
      return buf.buffer;
    },

    async uploadSimHTTP(buffer) {
      var fd = new FormData();
      fd.append('file', new Blob([buffer]), 'bench.bin');
      var res = await fetch('/api/sim/upload', { method: 'POST', headers: this.authHeaders(), body: fd });
      this.assertOk(res);
      var data = await res.json();
      return data.hash;
    },

    // Splits the buffer across several /api/sim/upload requests using the
    // same continuation-key protocol as uploadFileHTTPChunked, so the
    // benchmark can exercise the chunked-upload session machinery itself
    // (HTTP backend only — see CLAUDE.md's WebSocket Transport section for
    // why there's no WS equivalent of this protocol).
    async uploadSimHTTPChunked(buffer, chunkSize, onProgress) {
      var bytes = new Uint8Array(buffer);
      var encrypted = this.paramEncryptEnabled();
      var continuationKey = '';
      var offset = 0;
      var done = false;
      var hash = null;
      while (!done) {
        var end   = Math.min(offset + chunkSize, bytes.length);
        var slice = bytes.subarray(offset, end);
        done      = end >= bytes.length;

        var spec = this.encodeBody({ 'continuation-key': continuationKey, 'ul-done': done });
        var fd = new FormData();
        fd.append('transform', new Blob([spec], { type: encrypted ? 'text/plain' : 'application/json' }), 'transform.json');
        fd.append('file', new Blob([slice]), 'bench.bin');
        var res = await fetch('/api/sim/upload', { method: 'POST', headers: this.authHeaders(), body: fd });
        this.assertOk(res);
        var data = await this.decodeResponseJSON(res, encrypted);
        continuationKey = data['continuation-key'] || '';
        if (data.hash) hash = data.hash;

        if (onProgress) onProgress(slice.byteLength);
        offset = end;
      }
      return hash;
    },

    async uploadSimWS(buffer, onProgress) {
      var frameSize = this.settings.wsFrameSizeKB * 1024;
      var decrypt   = this.paramEncryptEnabled() ? this.decryptParams.bind(this) : null;
      var ws = await wsOpen(this.wsBaseURL() + '/api/sim/ws/upload');
      try {
        this.sendWSControl(ws, { auth: this.authKeyHash, frameSize: frameSize });
        var ack = await wsNextJSON(ws, decrypt);
        if (!ack.ok) throw new Error(ack.error || 'upload failed');

        await sendWSBinary(ws, buffer, frameSize, onProgress);
        this.sendWSControl(ws, { done: true });

        var final = await wsNextJSON(ws, decrypt);
        if (!final.ok) throw new Error(final.error || 'upload failed');
        return final.hash;
      } finally {
        ws.close();
      }
    },

    async runUploadBenchmark() {
      if (this.benchmarkRunning) return;
      this.clampBenchmarkSettings();
      this.benchmarkRunning = true;

      var backend   = this.settings.backend;
      var totalSize = this.settings.benchmark.sizeMB * 1024 * 1024;
      var frameSize = backend === 'ws' ? this.settings.wsFrameSizeKB * 1024 : null;
      // Chunked upload (the continuation-key session protocol) is HTTP-only.
      var chunkSize = backend === 'http' ? Math.min(this.settings.benchmark.chunkSizeMB * 1024 * 1024, totalSize) : null;
      var base      = this.benchmarkResultBase('upload', backend, totalSize, frameSize, chunkSize ? { chunkBytes: chunkSize } : {});

      if (!window.crypto.subtle) {
        this.addBenchmarkResult(Object.assign({}, base, {
          durationMs: 0, ok: false,
          error: 'SHA-256 hashing requires a secure context (HTTPS or localhost)',
        }));
        this.resetBenchmarkFlags();
        return;
      }

      var startTime = null; // set once the network operation actually starts
      var self = this;

      // Real intermediate steps: one send per WS frame, or one POST per
      // HTTP chunk. A single-request HTTP upload has no progress event.
      var hasSteps = (backend === 'ws') ? (frameSize < totalSize) : (chunkSize < totalSize);
      this.benchmarkProgress = hasSteps ? 0 : null;
      var onProgress = this.makeBenchmarkProgress(totalSize, hasSteps);

      try {
        // Computed before the timer starts, so generating and hashing the
        // buffer locally isn't counted toward the transfer time.
        this.benchmarkPreparing = true;
        this.benchmarkPrepProgress = 0;
        var prepBytesDone = 0;
        var buffer = await this.generateRandomBuffer(totalSize, function(n) {
          prepBytesDone += n;
          self.benchmarkPrepProgress = Math.min(1, prepBytesDone / totalSize);
        });
        this.benchmarkPrepProgress = null; // hashing has no incremental progress API
        var expectedHash = await this.sha256Hex(buffer);
        this.benchmarkPreparing = false;

        startTime = performance.now();
        var hash;
        if (backend === 'ws') {
          hash = await this.uploadSimWS(buffer, onProgress);
        } else if (chunkSize < totalSize) {
          hash = await this.uploadSimHTTPChunked(buffer, chunkSize, onProgress);
        } else {
          hash = await this.uploadSimHTTP(buffer);
        }
        var durationMs = performance.now() - startTime;

        var hashMatch = hash === expectedHash;
        this.addBenchmarkResult(Object.assign({}, base, {
          durationMs: durationMs, ok: hashMatch,
          error: hashMatch ? null : 'hash mismatch (data corrupted in transit)',
        }));
      } catch(err) {
        this.addBenchmarkResult(Object.assign({}, base, {
          durationMs: startTime !== null ? performance.now() - startTime : 0,
          ok: false,
          error: this.formatBenchmarkError(err),
        }));
      } finally {
        this.resetBenchmarkFlags();
      }
    },
  };
}
