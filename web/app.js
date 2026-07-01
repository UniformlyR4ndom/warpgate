pdfjsLib.GlobalWorkerOptions.workerSrc = 'pdf.worker.min.js';

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
    clipItems: [],
    clipText: '',
    clipEditId: null,
    clipError: null,
    autoRefresh: false,
    confirmDelete: true,

    transformTypes: [
      { id: 'id',       label: 'id'           },
      { id: 'xor',      label: 'XOR'          },
      { id: 'aes-ctr',  label: 'AES'          },
      { id: 'pdf',      label: 'PDF'          },
      { id: 'html-b64', label: 'HTML (base64)' },
    ],

    panel: {
      open: true,
      authOpen: true,
      authShowKey: false,
      transformOpen: true,
      chunkingOpen: true,
      generalOpen: true,
    },

    settings: {
      auth: { key: localStorage.getItem('auth-key') || '' },
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
    },

    init() {
      this.applyAuto();
      this.load();
      fetch('/api/public/checkin').catch(() => {});

      this.$watch('autoRefresh', (enabled) => {
        clearInterval(refreshTimer);
        refreshTimer = enabled ? setInterval(() => {
          if (this.tab === 'download') this.load();
          else if (this.tab === 'upload') this.loadUploadDir();
          else if (this.tab === 'clipboard') this.loadClipboard();
        }, 1000) : null;
      });

      this.$watch('settings.auth.key', (newVal) => {
        localStorage.setItem('auth-key', newVal || '');
        this.load();
        if (this.tab === 'upload') this.loadUploadDir();
      });
    },

    randomHex(byteCount) {
      var bytes = new Uint8Array(byteCount);
      window.crypto.getRandomValues(bytes);
      return Array.from(bytes).map(function(b) {
        return ('0' + b.toString(16)).slice(-2);
      }).join('');
    },

    authHeaders() {
      var h = {};
      if (this.settings.auth.key) { h['X-Auth'] = this.settings.auth.key; }
      return h;
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

    async fetchChunk(path, start, size) {
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
      var res = await fetch('/api/files/get', {
        method:  'POST',
        headers: Object.assign({ 'Content-Type': 'application/json' }, this.authHeaders()),
        body:    JSON.stringify(body),
      });
      if (!res.ok) throw Object.assign(new Error('HTTP ' + res.status), { status: res.status });
      var buffer = await res.arrayBuffer();
      return this.decrypt(buffer, password, htmlB64Key);
    },

    async downloadFile(entry) {
      this.error = null;
      var path     = this.pathSegments.concat([entry.name]).join('/');
      var chunking = this.settings.chunking;

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
                var data  = await this.fetchChunk(path, start, size);
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
          }
        } else {
          var self = this;
          (async function downloadChunk(i) {
            if (i >= numChunks) return;
            var start = i * chunkBytes;
            var size  = Math.min(chunkBytes, entry.size - start);
            try {
              var data = await self.fetchChunk(path, start, size);
              self.triggerDownload(data, entry.name + '.' + self.padIndex(i));
              await downloadChunk(i + 1);
            } catch(err) {
              self.error = 'Chunk ' + i + ' failed: ' + (err.status ? 'HTTP ' + err.status : err);
            }
          })(0);
        }
      } else {
        try {
          var data = await this.fetchChunk(path, 0, entry.size);
          this.triggerDownload(data, entry.name);
        } catch(err) {
          this.error = 'Download failed: ' + (err.status ? 'HTTP ' + err.status : err);
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
      if (this.confirmDelete && !window.confirm('Delete this note?')) return;
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
      var t = this.settings.transform;
      var self = this;
      var promises = Array.from(files).map(async function(file) {
        try {
          var result = await self.encodeForUpload(file);
          var spec = JSON.stringify({ transform: t.type, args: result.args });
          var fd   = new FormData();
          fd.append('transform', new Blob([spec], { type: 'application/json' }), 'transform.json');
          fd.append('file', result.data, file.name);
          var res = await fetch('/api/files/upload', { method: 'POST', headers: self.authHeaders(), body: fd });
          if (!res.ok) throw Object.assign(new Error('HTTP ' + res.status), { status: res.status });
        } catch(err) {
          self.uploadError = 'Upload of "' + file.name + '" failed: ' +
            (err.status ? 'HTTP ' + err.status : err);
        }
      });
      await Promise.all(promises);
      this.loadUploadDir();
    },

    triggerFileInput() {
      document.getElementById('ulFileInput').click();
    },
  };
}
