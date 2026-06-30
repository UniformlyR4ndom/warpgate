pdfjsLib.GlobalWorkerOptions.workerSrc = 'pdf.worker.min.js';

angular.module('warpgate', [])

.controller('FilesCtrl', ['$scope', '$http', '$q', function($scope, $http, $q) {
  $scope.pathSegments = [];
  $scope.entries = [];
  $scope.error = null;
  $scope.hasFileSystemAccess = typeof window.showSaveFilePicker === 'function';
  $scope.tab = 'download';
  $scope.uploadEntries = [];
  $scope.uploadError = null;

  $scope.transformTypes = [
    { id: 'id',       label: 'id'       },
    { id: 'xor',      label: 'XOR'      },
    { id: 'aes-ctr',  label: 'AES'      },
    { id: 'pdf',      label: 'PDF'      },
    { id: 'html-b64', label: 'HTML (base64)' },
  ];

  $scope.panel = {
    open: true,
    authOpen: true,
    transformOpen: true,
    chunkingOpen: true,
  };

  $scope.settings = {
    auth: { key: localStorage.getItem('auth-key') || '' },
    transform: {
      type: 'id',
      xor:    { auto: true,  key: '' },
      aesCtr: { autoKey: true, key: '', autoIv: true, iv: '' },
      pdf:    { filename: '', password: { enabled: false } },
    },
    chunking: {
      enabled:    false,
      chunkSize:  100,
      reassemble: false,
    },
  };

  function randomHex(byteCount) {
    var bytes = new Uint8Array(byteCount);
    window.crypto.getRandomValues(bytes);
    return Array.from(bytes).map(function(b) {
      return ('0' + b.toString(16)).slice(-2);
    }).join('');
  }

  function authHeaders() {
    var h = {};
    if ($scope.settings.auth.key) { h['X-Auth'] = $scope.settings.auth.key; }
    return h;
  }

  function applyAuto() {
    var xor = $scope.settings.transform.xor;
    if (xor.auto) { xor.key = randomHex(32); }

    var aes = $scope.settings.transform.aesCtr;
    if (aes.autoKey) { aes.key = randomHex(32); }
    if (aes.autoIv)  { aes.iv  = randomHex(16); }
  }

  $scope.onAutoChange = function(field) {
    var xor = $scope.settings.transform.xor;
    var aes = $scope.settings.transform.aesCtr;
    if (field === 'xor'         && xor.auto)    { xor.key = randomHex(32); }
    if (field === 'aes-ctr-key' && aes.autoKey) { aes.key = randomHex(32); }
    if (field === 'aes-ctr-iv'  && aes.autoIv)  { aes.iv  = randomHex(16); }
  };

  $scope.formatSize = function(bytes) {
    if (bytes === null || bytes === undefined) return '';
    if (bytes < 1024)                return bytes.toFixed(2)         + ' B';
    if (bytes < 1024 * 1024)         return (bytes / 1024).toFixed(2)            + ' KB';
    if (bytes < 1024 * 1024 * 1024)  return (bytes / (1024 * 1024)).toFixed(2)   + ' MB';
    return                                   (bytes / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
  };

  function hexToBytes(hex) {
    var bytes = new Uint8Array(hex.length / 2);
    for (var i = 0; i < bytes.length; i++) {
      bytes[i] = parseInt(hex.substr(i * 2, 2), 16);
    }
    return bytes;
  }

  function applyXOR(data, key) {
    var result = new Uint8Array(data.length);
    for (var i = 0; i < data.length; i++) {
      result[i] = data[i] ^ key[i % key.length];
    }
    return result;
  }

  function decryptAESCTR(buffer) {
    var t    = $scope.settings.transform.aesCtr;
    var key  = Array.from(hexToBytes(t.key));
    var iv   = Array.from(hexToBytes(t.iv));
    var data = new Uint8Array(buffer);
    var aesCtr = new aesjs.ModeOfOperation.ctr(key, new aesjs.Counter(iv));
    return $q.resolve(aesCtr.decrypt(data));
  }

  function decryptHTMLB64(buffer, keyHex) {
    var html = new TextDecoder().decode(new Uint8Array(buffer));
    var prefix = 'src="data:image/jpeg;base64,';
    var start = html.indexOf(prefix);
    if (start === -1) { return $q.reject(new Error('html-b64: payload not found')); }
    start += prefix.length;
    var end = html.indexOf('"', start);
    if (end === -1) { return $q.reject(new Error('html-b64: unterminated payload')); }
    var b64 = html.slice(start, end);
    var binary = atob(b64);
    var bytes = new Uint8Array(binary.length);
    for (var i = 0; i < binary.length; i++) { bytes[i] = binary.charCodeAt(i); }
    return $q.resolve(applyXOR(bytes, hexToBytes(keyHex)));
  }

  function decryptPDF(buffer, password) {
    var opts = { data: new Uint8Array(buffer) };
    if (password) { opts.password = password; }
    return $q.when(pdfjsLib.getDocument(opts).promise).then(function(pdf) {
      return $q.when(pdf.getAttachments());
    }).then(function(attachments) {
      if (!attachments) throw new Error('PDF has no attachments');
      var keys = Object.keys(attachments);
      if (!keys.length) throw new Error('PDF has no attachments');
      return new Uint8Array(attachments[keys[0]].content);
    });
  }

  function decrypt(buffer, password, htmlB64Key) {
    var t = $scope.settings.transform;
    if (t.type === 'xor') {
      return $q.resolve(applyXOR(new Uint8Array(buffer), hexToBytes(t.xor.key)));
    }
    if (t.type === 'aes-ctr') {
      return decryptAESCTR(buffer);
    }
    if (t.type === 'pdf') {
      return decryptPDF(buffer, password);
    }
    if (t.type === 'html-b64') {
      return decryptHTMLB64(buffer, htmlB64Key);
    }
    return $q.resolve(new Uint8Array(buffer));
  }

  function buildTransformArgs(password, htmlB64Key) {
    var t = $scope.settings.transform;
    if (t.type === 'xor')      return { key: t.xor.key };
    if (t.type === 'aes-ctr')  return { key: t.aesCtr.key, iv: t.aesCtr.iv };
    if (t.type === 'html-b64') return { key: htmlB64Key };
    if (t.type === 'pdf') {
      var args = { filename: t.pdf.filename || 'attachment' };
      if (password) { args.password = password; }
      return args;
    }
    return {};
  }

  function padIndex(i) {
    return ('000' + i).slice(-3);
  }

  function triggerDownload(data, filename) {
    var blob = new Blob([data]);
    var url  = URL.createObjectURL(blob);
    var a    = document.createElement('a');
    a.href     = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
  }

  function fetchChunk(path, start, size) {
    var t = $scope.settings.transform;
    var password   = (t.type === 'pdf'      && t.pdf.password.enabled) ? randomHex(10) : null;
    var htmlB64Key = (t.type === 'html-b64') ? randomHex(16) : null;
    var body = {
      path:      path,
      start:     start,
      size:      size,
      transform: t.type,
      args:      buildTransformArgs(password, htmlB64Key),
    };
    return $http.post('/get', body, { responseType: 'arraybuffer', headers: authHeaders() })
      .then(function(res) { return decrypt(res.data, password, htmlB64Key); });
  }

  $scope.downloadFile = function(entry) {
    $scope.error = null;
    var path     = $scope.pathSegments.concat([entry.name]).join('/');
    var chunking = $scope.settings.chunking;

    if (chunking.enabled) {
      var chunkBytes = chunking.chunkSize * 1024 * 1024;
      var numChunks  = Math.ceil(entry.size / chunkBytes);

      if (chunking.reassemble && $scope.hasFileSystemAccess) {
        $q.when(window.showSaveFilePicker({ suggestedName: entry.name }))
          .then(function(fileHandle) {
            return $q.when(fileHandle.createWritable()).then(function(writable) {
              function writeChunk(i) {
                if (i >= numChunks) { return $q.when(writable.close()); }
                var start = i * chunkBytes;
                var size  = Math.min(chunkBytes, entry.size - start);
                return fetchChunk(path, start, size)
                  .then(function(data) { return $q.when(writable.write(data)); })
                  .then(function() { return writeChunk(i + 1); });
              }
              return writeChunk(0).catch(function(err) {
                writable.abort();
                throw err;
              });
            });
          })
          .catch(function(err) {
            if (err && err.name !== 'AbortError') {
              $scope.error = 'Download failed: ' + err;
            }
          });
      } else {
        function downloadChunk(i) {
          if (i >= numChunks) return;
          var start = i * chunkBytes;
          var size  = Math.min(chunkBytes, entry.size - start);
          fetchChunk(path, start, size)
            .then(function(data) {
              triggerDownload(data, entry.name + '.' + padIndex(i));
              downloadChunk(i + 1);
            })
            .catch(function(err) {
              $scope.error = 'Chunk ' + i + ' failed: ' + (err.status ? 'HTTP ' + err.status : err);
            });
        }
        downloadChunk(0);
      }
    } else {
      fetchChunk(path, 0, entry.size)
        .then(function(data) {
          triggerDownload(data, entry.name);
        })
        .catch(function(err) {
          $scope.error = 'Download failed: ' + (err.status ? 'HTTP ' + err.status : err);
        });
    }
  };

  function currentPath() {
    return $scope.pathSegments.join('/');
  }

  function load() {
    $scope.error = null;
    $http.post('/list', { path: currentPath() }, { headers: authHeaders() })
      .then(function(res) {
        $scope.entries = res.data;
      }, function(res) {
        $scope.error = 'Failed to load directory (HTTP ' + res.status + ')';
      });
  }

  $scope.enterDir = function(name) {
    $scope.pathSegments.push(name);
    load();
  };

  $scope.navigateTo = function(index) {
    $scope.pathSegments = $scope.pathSegments.slice(0, index + 1);
    load();
  };

  function loadUploadDir() {
    $scope.uploadError = null;
    $http.get('/ul-list', { headers: authHeaders() }).then(function(res) {
      $scope.uploadEntries = res.data || [];
    }).catch(function(res) {
      $scope.uploadEntries = [];
      $scope.uploadError = res.status === 404
        ? 'Uploads are not enabled on this server.'
        : 'Failed to load upload directory (HTTP ' + res.status + ')';
    });
  }

  $scope.setTab = function(t) {
    $scope.tab = t;
    if (t === 'upload') loadUploadDir();
  };

  function createPDFWithAttachment(fileBytes, filename) {
    return PDFLib.PDFDocument.create().then(function(doc) {
      return doc.embedFont(PDFLib.StandardFonts.Helvetica).then(function(font) {
        var page = doc.addPage([595, 842]); // A4
        page.drawText('Hello there!', { x: 100, y: 700, size: 12, font: font });
        return doc.attach(fileBytes, filename, { mimeType: 'application/octet-stream' });
      }).then(function() {
        return doc.save();
      });
    });
  }

  function encodeForUpload(file) {
    var t = $scope.settings.transform;
    if (t.type === 'xor') {
      return $q.when(file.arrayBuffer()).then(function(buf) {
        var encoded = applyXOR(new Uint8Array(buf), hexToBytes(t.xor.key));
        return { data: new Blob([encoded]), args: { key: t.xor.key } };
      });
    }
    if (t.type === 'aes-ctr') {
      return $q.when(file.arrayBuffer()).then(function(buf) {
        var key    = Array.from(hexToBytes(t.aesCtr.key));
        var iv     = Array.from(hexToBytes(t.aesCtr.iv));
        var aesCtr = new aesjs.ModeOfOperation.ctr(key, new aesjs.Counter(iv));
        var encoded = aesCtr.encrypt(new Uint8Array(buf));
        return { data: new Blob([encoded]), args: { key: t.aesCtr.key, iv: t.aesCtr.iv } };
      });
    }
    if (t.type === 'pdf') {
      var filename = t.pdf.filename || file.name;
      return $q.when(
        file.arrayBuffer().then(function(buf) {
          return createPDFWithAttachment(new Uint8Array(buf), filename);
        })
      ).then(function(pdfBytes) {
        return {
          data: new Blob([pdfBytes], { type: 'application/pdf' }),
          args: { filename: filename },
        };
      });
    }
    if (t.type === 'html-b64') {
      return $q.when(file.arrayBuffer()).then(function(buf) {
        var keyHex = randomHex(16);
        var xored  = applyXOR(new Uint8Array(buf), hexToBytes(keyHex));
        var binary = '';
        for (var i = 0; i < xored.length; i++) { binary += String.fromCharCode(xored[i]); }
        var b64  = btoa(binary);
        var html = '<!DOCTYPE html><html><body>\n' +
                   '<img src="data:image/jpeg;base64,' + b64 + '">' + '\n' +
                   '</body></html>';
        return { data: new Blob([html], { type: 'text/html' }), args: { key: keyHex } };
      });
    }
    return $q.resolve({ data: file, args: {} });
  }

  $scope.uploadFiles = function(files) {
    $scope.uploadError = null;
    var t = $scope.settings.transform;
    var promises = Array.from(files).map(function(file) {
      return encodeForUpload(file).then(function(result) {
        var spec = JSON.stringify({ transform: t.type, args: result.args });
        var fd = new FormData();
        fd.append('transform', new Blob([spec], { type: 'application/json' }), 'transform.json');
        fd.append('file', result.data, file.name);
        return $http.post('/upload', fd, {
          headers: Object.assign({ 'Content-Type': undefined }, authHeaders()),
          transformRequest: angular.identity,
        });
      }).catch(function(err) {
        $scope.uploadError = 'Upload of "' + file.name + '" failed: ' +
          (err.status ? 'HTTP ' + err.status : err);
      });
    });
    $q.all(promises).then(loadUploadDir);
  };

  $scope.triggerFileInput = function() {
    document.getElementById('ulFileInput').click();
  };

  $scope.$watch('settings.auth.key', function(newVal, oldVal) {
    if (newVal === oldVal) return;
    localStorage.setItem('auth-key', newVal || '');
    load();
    if ($scope.tab === 'upload') loadUploadDir();
  });

  applyAuto();
  load();
  $http.get('/checkin').catch(angular.noop);
}])

.directive('dropZone', function() {
  return function(scope, element) {
    element.on('dragover dragenter', function(e) {
      e.preventDefault();
      e.stopPropagation();
      element.addClass('drag-active');
    });
    element.on('dragleave', function(e) {
      if (!e.relatedTarget || !element[0].contains(e.relatedTarget)) {
        element.removeClass('drag-active');
      }
    });
    element.on('drop', function(e) {
      e.preventDefault();
      e.stopPropagation();
      element.removeClass('drag-active');
      scope.$apply(function() {
        scope.uploadFiles(e.dataTransfer.files);
      });
    });
  };
})

.directive('fileInput', function() {
  return function(scope, element) {
    element.on('change', function(e) {
      scope.$apply(function() {
        scope.uploadFiles(e.target.files);
      });
      element.val('');
    });
  };
});
