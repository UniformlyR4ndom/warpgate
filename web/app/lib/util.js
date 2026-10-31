// Pure helpers: no framework, no store. Byte/hex/base64 conversions, hashing,
// formatting. aesjs/sha256 globals come from the vendored classic scripts.

export function bytesToHex(bytes) {
  return Array.from(bytes).map(function(b) {
    return ('0' + b.toString(16)).slice(-2);
  }).join('');
}

export function hexToBytes(hex) {
  var bytes = new Uint8Array(hex.length / 2);
  for (var i = 0; i < bytes.length; i++) {
    bytes[i] = parseInt(hex.substr(i * 2, 2), 16);
  }
  return bytes;
}

export function randomHex(byteCount) {
  var bytes = new Uint8Array(byteCount);
  window.crypto.getRandomValues(bytes);
  return bytesToHex(bytes);
}

export async function sha256Hex(buffer) {
  var digest = await window.crypto.subtle.digest('SHA-256', buffer);
  return bytesToHex(new Uint8Array(digest));
}

export function applyXOR(data, key) {
  var result = new Uint8Array(data.length);
  for (var i = 0; i < data.length; i++) {
    result[i] = data[i] ^ key[i % key.length];
  }
  return result;
}

export function bytesToBinaryString(bytes) {
  var binary = '';
  for (var i = 0; i < bytes.length; i++) { binary += String.fromCharCode(bytes[i]); }
  return binary;
}

export function binaryStringToBytes(binary) {
  var bytes = new Uint8Array(binary.length);
  for (var i = 0; i < binary.length; i++) { bytes[i] = binary.charCodeAt(i); }
  return bytes;
}

export function textToBase64(str) {
  return btoa(bytesToBinaryString(new TextEncoder().encode(str)));
}

export function base64ToText(b64) {
  return new TextDecoder().decode(binaryStringToBytes(atob(b64)));
}

export function padIndex(i) {
  return ('000' + i).slice(-3);
}

export function formatSize(bytes) {
  if (bytes === null || bytes === undefined) return '';
  if (bytes < 1024)               return bytes.toFixed(2) + ' B';
  if (bytes < 1024 * 1024)        return (bytes / 1024).toFixed(2) + ' KB';
  if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(2) + ' MB';
  return (bytes / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
}

// Fixed 24-hour local time with explicit UTC offset, locale-independent.
export function formatTSAt(ts, offMin) {
  var d = new Date(ts * 1000 + offMin * 60000);
  var p = function(n) { return String(n).padStart(2, '0'); };
  return d.getUTCFullYear() + '-' + p(d.getUTCMonth() + 1) + '-' + p(d.getUTCDate()) + ' ' +
    p(d.getUTCHours()) + ':' + p(d.getUTCMinutes()) + ':' + p(d.getUTCSeconds());
}

export function utcOffsetLabel(offMin) {
  var p = function(n) { return String(n).padStart(2, '0'); };
  var abs = Math.abs(offMin);
  return 'UTC' + (offMin < 0 ? '-' : '+') + p(Math.floor(abs / 60)) + ':' + p(abs % 60);
}

export function currentUTCOffset() {
  return -new Date().getTimezoneOffset();
}

export function formatTS(ts) {
  var off = currentUTCOffset();
  return formatTSAt(ts, off) + ' ' + utcOffsetLabel(off);
}

export function triggerDownload(data, filename) {
  var url = URL.createObjectURL(new Blob([data]));
  saveURL(url, filename);
  URL.revokeObjectURL(url);
}

export function saveURL(url, filename) {
  var a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
}

// Replaces characters that are invalid in Windows or Unix filenames with '_'
// and whitespace runs with '-'; may return ''.
export function safeFileName(s) {
  return s.replace(/[<>:"\/\\|?*\u0000-\u001f\u007f]/g, '_').replace(/^[\s.]+|[\s.]+$/g, '')
    .replace(/\s+/g, '-').slice(0, 100);
}
