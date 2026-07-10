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

async function wsNextJSON(ws) {
  var raw = await wsNextMessage(ws);
  if (typeof raw !== 'string') throw new Error('expected JSON control message, got binary');
  return JSON.parse(raw);
}

const SETTINGS_STORAGE_KEY = 'warpgate-settings';
const SETTINGS_VERSION = 1;

// mergeSettings copies fields from source onto target, recursing into
// nested plain objects, but only for keys that already exist on target —
// this ignores stale/unknown fields from an older schema version instead
// of blindly grafting them on.
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
    benchmarkResults: [],
    benchmarkRunning: false,
    clipItems: [],
    clipText: '',
    clipEditId: null,
    clipError: null,
    authShowKey: false, // not persisted: always start masked
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

    // Everything in `settings` is persisted to localStorage as one JSON
    // object (see loadSettings/saveSettings) — keep it that way, i.e. don't
    // add non-serializable values (functions, DOM refs) here, and put any
    // new UI-configurable value under this object rather than top-level.
    settings: {
      auth: { key: '' },
      backend: 'http',
      autoRefresh: false,
      confirmDelete: true,
      wsFrameSizeKB: 4096, // 4M default, clamped to the server's max in init()
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

    init() {
      this.loadSettings();
      this.applyAuto();
      this.load();

      // x-model on a <select> whose <option>s come from x-for tries to apply
      // the restored value before those options exist in the DOM yet; a
      // native <select>.value assignment to a not-yet-existing option is a
      // silent no-op with no automatic retry once the option appears.
      // $nextTick alone isn't late enough to guarantee this; a 0ms timeout
      // (a full macrotask later) reliably is. Force a resync once settled.
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

      this.$watch('settings.auth.key', () => {
        this.load();
        if (this.tab === 'upload') this.loadUploadDir();
      });

      // Deep-watch the whole settings tree: evaluating JSON.stringify(settings)
      // reads every nested field, so this fires on any change anywhere in it.
      this.$watch('JSON.stringify(settings)', () => { this.saveSettings(); });
    },

    applyAutoRefresh(enabled) {
      clearInterval(refreshTimer);
      refreshTimer = enabled ? setInterval(() => {
        if (this.tab === 'download') this.load();
        else if (this.tab === 'upload') this.loadUploadDir();
        else if (this.tab === 'clipboard') this.loadClipboard();
      }, 1000) : null;
    },

    loadSettings() {
      try {
        var raw = localStorage.getItem(SETTINGS_STORAGE_KEY);
        if (!raw) return;
        var parsed = JSON.parse(raw);
        if (!parsed || parsed.version !== SETTINGS_VERSION || !parsed.settings) return;
        mergeSettings(this.settings, parsed.settings);
      } catch(e) {}
    },

    saveSettings() {
      try {
        localStorage.setItem(SETTINGS_STORAGE_KEY, JSON.stringify({ version: SETTINGS_VERSION, settings: this.settings }));
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

    authHeaders() {
      var h = {};
      if (this.settings.auth.key) { h['X-Auth'] = this.settings.auth.key; }
      return h;
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
      var binary = atob(b64);
      var bytes = new Uint8Array(binary.length);
      for (var i = 0; i < binary.length; i++) { bytes[i] = binary.charCodeAt(i); }
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

    triggerDownload(data, filename) {
      var blob = new Blob([data]);
      var url  = URL.createObjectURL(blob);
      var a    = document.createElement('a');
      a.href     = url;
      a.download = filename;
      a.click();
      URL.revokeObjectURL(url);
    },

    // fetchChunk fetches one chunk of a file. wsSession is a plain object
    // ({ws, password, htmlB64Key}, all initially null) owned by the caller
    // and threaded through repeated calls for the same file; when
    // this.settings.backend === 'ws' the first call opens a WebSocket connection and
    // caches it there, subsequent calls reuse it, and the caller must call
    // closeWSSession(wsSession) once done. Ignored for the HTTP backend.
    async fetchChunk(wsSession, path, start, size) {
      if (this.settings.backend === 'ws') { return this.fetchChunkWS(wsSession, path, start, size); }
      return this.fetchChunkHTTP(path, start, size);
    },

    async fetchChunkHTTP(path, start, size) {
      var t = this.settings.transform;
      var password   = (t.type === 'pdf'      && t.pdf.password.enabled) ? this.randomHex(10) : null;
      var htmlB64Key = (t.type === 'html-b64') ? this.randomHex(16) : null;
      var body = {
        path:      path,
        start:     start,
        size:      size,
        transform: t.type,
        args:      this.buildTransformArgs(password, htmlB64Key),
      };
      var res = await fetch('/api/files/download', {
        method:  'POST',
        headers: Object.assign({ 'Content-Type': 'application/json' }, this.authHeaders()),
        body:    JSON.stringify(body),
      });
      if (!res.ok) throw Object.assign(new Error('HTTP ' + res.status), { status: res.status });
      var buffer = await res.arrayBuffer();
      return this.decrypt(buffer, password, htmlB64Key);
    },

    async fetchChunkWS(wsSession, path, start, size) {
      if (!wsSession.ws) {
        var t          = this.settings.transform;
        var password   = (t.type === 'pdf'      && t.pdf.password.enabled) ? this.randomHex(10) : null;
        var htmlB64Key = (t.type === 'html-b64') ? this.randomHex(16) : null;

        var ws = await wsOpen(this.wsBaseURL() + '/api/files/ws/download');
        ws.send(JSON.stringify({
          auth:      this.settings.auth.key,
          path:      path,
          transform: t.type,
          args:      this.buildTransformArgs(password, htmlB64Key),
          frameSize: this.settings.wsFrameSizeKB * 1024,
        }));
        var ack = await wsNextJSON(ws);
        if (!ack.ok) { ws.close(); throw new Error(ack.error || 'download failed'); }

        wsSession.ws         = ws;
        wsSession.password   = password;
        wsSession.htmlB64Key = htmlB64Key;
      }

      wsSession.ws.send(JSON.stringify({ start: start, size: size }));

      // The transformed chunk isn't always the same length as the requested
      // plaintext size (pdf/html-b64 both expand it), so the server
      // announces the actual byte count in a control message before sending
      // any frames; size it from that instead of the request's own size.
      var chunkAck = await wsNextJSON(wsSession.ws);
      if (!chunkAck.ok) throw new Error(chunkAck.error || 'download failed');
      var chunkSize = chunkAck.chunkSize;

      // The server may split one chunk across several frameSize-sized
      // binary messages instead of sending it as a single message; keep
      // reading until the full chunk has arrived.
      var received = new Uint8Array(chunkSize);
      var offset = 0;
      while (offset < chunkSize) {
        var raw = await wsNextMessage(wsSession.ws);
        if (typeof raw === 'string') {
          var msg = JSON.parse(raw);
          throw new Error(msg.error || 'download failed');
        }
        var piece = new Uint8Array(raw);
        received.set(piece, offset);
        offset += piece.byteLength;
      }
      return this.decrypt(received.buffer, wsSession.password, wsSession.htmlB64Key);
    },

    closeWSSession(wsSession) {
      if (!wsSession || !wsSession.ws) { return; }
      var ws = wsSession.ws;
      wsSession.ws = null;
      try { ws.send(JSON.stringify({ done: true })); } catch(e) {}
      try { ws.close(); } catch(e) {}
    },

    async downloadFile(entry) {
      this.error = null;
      var path      = this.pathSegments.concat([entry.name]).join('/');
      var chunking  = this.settings.chunking;
      var wsSession = { ws: null, password: null, htmlB64Key: null };

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
                var data  = await this.fetchChunk(wsSession, path, start, size);
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
          }
        } else {
          var self = this;
          (async function() {
            try {
              for (var i = 0; i < numChunks; i++) {
                var start = i * chunkBytes;
                var size  = Math.min(chunkBytes, entry.size - start);
                var data  = await self.fetchChunk(wsSession, path, start, size);
                self.triggerDownload(data, entry.name + '.' + self.padIndex(i));
              }
            } catch(err) {
              self.error = 'Chunk failed: ' + (err.status ? 'HTTP ' + err.status : err);
            } finally {
              self.closeWSSession(wsSession);
            }
          })();
        }
      } else {
        try {
          var data = await this.fetchChunk(wsSession, path, 0, entry.size);
          this.triggerDownload(data, entry.name);
        } catch(err) {
          this.error = 'Download failed: ' + (err.status ? 'HTTP ' + err.status : err);
        } finally {
          this.closeWSSession(wsSession);
        }
      }
    },

    currentPath() {
      return this.pathSegments.join('/');
    },

    async load() {
      this.error = null;
      try {
        var res = await fetch('/api/files/list', {
          method:  'POST',
          headers: Object.assign({ 'Content-Type': 'application/json' }, this.authHeaders()),
          body:    JSON.stringify({ path: this.currentPath() }),
        });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        var data = await res.json();
        if (JSON.stringify(data) !== JSON.stringify(this.entries)) {
          this.entries = data;
        }
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
        var res = await fetch('/api/files/ul-list', { headers: this.authHeaders() });
        if (!res.ok) {
          this.uploadEntries = [];
          this.uploadError = res.status === 404
            ? 'Uploads are not enabled on this server.'
            : 'Failed to load upload directory (HTTP ' + res.status + ')';
          return;
        }
        var data = (await res.json()) || [];
        if (JSON.stringify(data) !== JSON.stringify(this.uploadEntries)) {
          this.uploadEntries = data;
        }
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
      var binary = '';
      for (var i = 0; i < bytes.length; i++) { binary += String.fromCharCode(bytes[i]); }
      return btoa(binary);
    },

    base64ToText(b64) {
      var binary = atob(b64);
      var bytes = new Uint8Array(binary.length);
      for (var i = 0; i < binary.length; i++) { bytes[i] = binary.charCodeAt(i); }
      return new TextDecoder().decode(bytes);
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

    async loadClipboard() {
      this.clipError = null;
      try {
        var res = await fetch('/api/clip/list', { headers: this.authHeaders() });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        var data = await res.json();
        if (JSON.stringify(data) !== JSON.stringify(this.clipItems)) {
          this.clipItems = data;
        }
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
          headers: Object.assign({ 'Content-Type': 'application/json' }, this.authHeaders()),
          body:    JSON.stringify(body),
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
          headers: Object.assign({ 'Content-Type': 'application/json' }, this.authHeaders()),
          body:    JSON.stringify({ id: id }),
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

    async encodeForUpload(file) {
      var t = this.settings.transform;
      if (t.type === 'xor') {
        var buf     = await file.arrayBuffer();
        var encoded = this.applyXOR(new Uint8Array(buf), this.hexToBytes(t.xor.key));
        return { data: new Blob([encoded]), args: { key: t.xor.key } };
      }
      if (t.type === 'aes-ctr') {
        var buf    = await file.arrayBuffer();
        var key    = Array.from(this.hexToBytes(t.aesCtr.key));
        var iv     = Array.from(this.hexToBytes(t.aesCtr.iv));
        var aesCtr = new aesjs.ModeOfOperation.ctr(key, new aesjs.Counter(iv));
        return { data: new Blob([aesCtr.encrypt(new Uint8Array(buf))]), args: { key: t.aesCtr.key, iv: t.aesCtr.iv } };
      }
      if (t.type === 'pdf') {
        var filename = t.pdf.filename || file.name;
        var buf      = await file.arrayBuffer();
        var pdfBytes = await this.createPDFWithAttachment(new Uint8Array(buf), filename);
        return { data: new Blob([pdfBytes], { type: 'application/pdf' }), args: { filename: filename } };
      }
      if (t.type === 'html-b64') {
        var buf    = await file.arrayBuffer();
        var keyHex = this.randomHex(16);
        var xored  = this.applyXOR(new Uint8Array(buf), this.hexToBytes(keyHex));
        var binary = '';
        for (var i = 0; i < xored.length; i++) { binary += String.fromCharCode(xored[i]); }
        var html = '<!DOCTYPE html><html><body>\n' +
                   '<img src="data:image/jpeg;base64,' + btoa(binary) + '">\n' +
                   '</body></html>';
        return { data: new Blob([html], { type: 'text/html' }), args: { key: keyHex } };
      }
      return { data: file, args: {} };
    },

    async uploadFiles(files) {
      this.uploadError = null;
      var self = this;
      var promises = Array.from(files).map(async function(file) {
        try {
          if (self.settings.backend === 'ws') {
            await self.uploadFileWS(file);
          } else {
            await self.uploadFileHTTP(file);
          }
        } catch(err) {
          self.uploadError = 'Upload of "' + file.name + '" failed: ' +
            (err.status ? 'HTTP ' + err.status : err);
        }
      });
      await Promise.all(promises);
      this.loadUploadDir();
    },

    async uploadFileHTTP(file) {
      var t      = this.settings.transform;
      var result = await this.encodeForUpload(file);
      var spec   = JSON.stringify({ transform: t.type, args: result.args });
      var fd     = new FormData();
      fd.append('transform', new Blob([spec], { type: 'application/json' }), 'transform.json');
      fd.append('file', result.data, file.name);
      var res = await fetch('/api/files/upload', { method: 'POST', headers: this.authHeaders(), body: fd });
      if (!res.ok) throw Object.assign(new Error('HTTP ' + res.status), { status: res.status });
    },

    async uploadFileWS(file) {
      var t         = this.settings.transform;
      var result    = await this.encodeForUpload(file);
      var buffer    = await result.data.arrayBuffer();
      var frameSize = this.settings.wsFrameSizeKB * 1024;

      var ws = await wsOpen(this.wsBaseURL() + '/api/files/ws/upload');
      try {
        ws.send(JSON.stringify({
          auth:      this.settings.auth.key,
          filename:  file.name,
          transform: t.type,
          args:      result.args,
          frameSize: frameSize,
        }));
        var ack = await wsNextJSON(ws);
        if (!ack.ok) throw new Error(ack.error || 'upload failed');

        // Send as several frameSize-sized messages instead of one, purely a
        // wire-transport concern — the server reassembles them before
        // applying Untransform once.
        var bytes = new Uint8Array(buffer);
        for (var offset = 0; offset < bytes.length; offset += frameSize) {
          ws.send(bytes.slice(offset, offset + frameSize));
        }
        ws.send(JSON.stringify({ done: true }));

        var final = await wsNextJSON(ws);
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
      if (b.sizeMB > 100)     { b.sizeMB = 100; }
      if (!(b.chunkSizeMB >= 1)) { b.chunkSizeMB = 1; }
      if (b.chunkSizeMB > b.sizeMB) { b.chunkSizeMB = b.sizeMB; }
    },

    addBenchmarkResult(result) {
      result.id = Date.now() + '-' + Math.random().toString(36).slice(2);
      this.benchmarkResults.unshift(result);
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

    async fetchSimChunkHTTP(totalSize, start, size) {
      var res = await fetch('/api/sim/download', {
        method:  'POST',
        headers: Object.assign({ 'Content-Type': 'application/json' }, this.authHeaders()),
        body:    JSON.stringify({ totalSize: totalSize, start: start, size: size }),
      });
      if (!res.ok) throw Object.assign(new Error('HTTP ' + res.status), { status: res.status });
      var buffer = await res.arrayBuffer();
      if (buffer.byteLength !== size) {
        throw new Error('expected ' + size + ' bytes, got ' + buffer.byteLength);
      }
      return new Uint8Array(buffer);
    },

    async fetchSimChunkWS(wsSession, totalSize, start, size) {
      if (!wsSession.ws) {
        var ws = await wsOpen(this.wsBaseURL() + '/api/sim/ws/download');
        ws.send(JSON.stringify({
          auth:      this.settings.auth.key,
          totalSize: totalSize,
          frameSize: this.settings.wsFrameSizeKB * 1024,
        }));
        var ack = await wsNextJSON(ws);
        if (!ack.ok) { ws.close(); throw new Error(ack.error || 'download failed'); }
        wsSession.ws = ws;
      }

      wsSession.ws.send(JSON.stringify({ start: start, size: size }));

      var chunk = new Uint8Array(size);
      var received = 0;
      while (received < size) {
        var raw = await wsNextMessage(wsSession.ws);
        if (typeof raw === 'string') {
          var msg = JSON.parse(raw);
          throw new Error(msg.error || 'download failed');
        }
        var piece = new Uint8Array(raw);
        chunk.set(piece, received);
        received += piece.byteLength;
      }
      if (received !== size) {
        throw new Error('expected ' + size + ' bytes, got ' + received);
      }
      return chunk;
    },

    async fetchSimChunk(wsSession, totalSize, start, size) {
      if (this.settings.backend === 'ws') { return this.fetchSimChunkWS(wsSession, totalSize, start, size); }
      return this.fetchSimChunkHTTP(totalSize, start, size);
    },

    async runDownloadBenchmark() {
      if (this.benchmarkRunning) return;
      this.clampBenchmarkSettings();
      this.benchmarkRunning = true;

      var backend   = this.settings.backend;
      var totalSize = this.settings.benchmark.sizeMB * 1024 * 1024;
      var chunkSize = Math.min(this.settings.benchmark.chunkSizeMB * 1024 * 1024, totalSize);
      var frameSize = backend === 'ws' ? this.settings.wsFrameSizeKB * 1024 : null;

      if (!window.crypto.subtle) {
        this.addBenchmarkResult({
          direction: 'download', backend: backend, sizeBytes: totalSize,
          chunkBytes: chunkSize, frameBytes: frameSize,
          durationMs: 0, ok: false,
          error: 'SHA-256 hashing requires a secure context (HTTPS or localhost)',
        });
        this.benchmarkRunning = false;
        return;
      }

      var wsSession = { ws: null };
      var startTime = null; // set once the network loop actually starts

      try {
        // Deterministic from totalSize alone, so this can be computed before
        // any request goes out and isn't counted toward the transfer time.
        var expectedHash = await this.sha256Hex(this.buildExpectedSimFile(totalSize));

        var received = new Uint8Array(totalSize);
        startTime = performance.now();
        for (var offset = 0; offset < totalSize; offset += chunkSize) {
          var size  = Math.min(chunkSize, totalSize - offset);
          var chunk = await this.fetchSimChunk(wsSession, totalSize, offset, size);
          received.set(chunk, offset);
        }
        var durationMs = performance.now() - startTime;

        var receivedHash = await this.sha256Hex(received.buffer);
        var hashMatch = receivedHash === expectedHash;

        this.addBenchmarkResult({
          direction: 'download', backend: backend, sizeBytes: totalSize,
          chunkBytes: chunkSize, frameBytes: frameSize,
          durationMs: durationMs, ok: hashMatch,
          error: hashMatch ? null : 'hash mismatch (data corrupted in transit)',
        });
      } catch(err) {
        this.addBenchmarkResult({
          direction: 'download', backend: backend, sizeBytes: totalSize,
          chunkBytes: chunkSize, frameBytes: frameSize,
          durationMs: startTime !== null ? performance.now() - startTime : 0,
          ok: false,
          error: err.status ? 'HTTP ' + err.status : String(err.message || err),
        });
      } finally {
        this.closeWSSession(wsSession);
        this.benchmarkRunning = false;
      }
    },

    // crypto.getRandomValues rejects requests over 65536 bytes in one call.
    generateRandomBuffer(totalSize) {
      var buf = new Uint8Array(totalSize);
      var CHUNK = 65536;
      for (var offset = 0; offset < totalSize; offset += CHUNK) {
        var len = Math.min(CHUNK, totalSize - offset);
        window.crypto.getRandomValues(buf.subarray(offset, offset + len));
      }
      return buf.buffer;
    },

    async uploadSimHTTP(buffer) {
      var fd = new FormData();
      fd.append('file', new Blob([buffer]), 'bench.bin');
      var res = await fetch('/api/sim/upload', { method: 'POST', headers: this.authHeaders(), body: fd });
      if (!res.ok) throw Object.assign(new Error('HTTP ' + res.status), { status: res.status });
      var data = await res.json();
      return data.hash;
    },

    async uploadSimWS(buffer) {
      var frameSize = this.settings.wsFrameSizeKB * 1024;
      var ws = await wsOpen(this.wsBaseURL() + '/api/sim/ws/upload');
      try {
        ws.send(JSON.stringify({ auth: this.settings.auth.key, frameSize: frameSize }));
        var ack = await wsNextJSON(ws);
        if (!ack.ok) throw new Error(ack.error || 'upload failed');

        var bytes = new Uint8Array(buffer);
        for (var offset = 0; offset < bytes.length; offset += frameSize) {
          ws.send(bytes.slice(offset, offset + frameSize));
        }
        ws.send(JSON.stringify({ done: true }));

        var final = await wsNextJSON(ws);
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

      if (!window.crypto.subtle) {
        this.addBenchmarkResult({
          direction: 'upload', backend: backend, sizeBytes: totalSize, frameBytes: frameSize,
          durationMs: 0, ok: false,
          error: 'SHA-256 hashing requires a secure context (HTTPS or localhost)',
        });
        this.benchmarkRunning = false;
        return;
      }

      var startTime = null; // set once the network operation actually starts

      try {
        // Computed before the timer starts, so generating and hashing the
        // buffer locally isn't counted toward the transfer time.
        var buffer       = this.generateRandomBuffer(totalSize);
        var expectedHash = await this.sha256Hex(buffer);

        startTime = performance.now();
        var hash = (backend === 'ws') ? await this.uploadSimWS(buffer) : await this.uploadSimHTTP(buffer);
        var durationMs = performance.now() - startTime;

        var hashMatch = hash === expectedHash;
        this.addBenchmarkResult({
          direction: 'upload', backend: backend, sizeBytes: totalSize, frameBytes: frameSize,
          durationMs: durationMs, ok: hashMatch,
          error: hashMatch ? null : 'hash mismatch (data corrupted in transit)',
        });
      } catch(err) {
        this.addBenchmarkResult({
          direction: 'upload', backend: backend, sizeBytes: totalSize, frameBytes: frameSize,
          durationMs: startTime !== null ? performance.now() - startTime : 0,
          ok: false,
          error: err.status ? 'HTTP ' + err.status : String(err.message || err),
        });
      } finally {
        this.benchmarkRunning = false;
      }
    },
  };
}
