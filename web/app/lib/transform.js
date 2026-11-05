// Client-side transform decode (download path), encode (upload path), and
// per-request arg building.
import { settings } from '../store.js';
import { getDocument, GlobalWorkerOptions, PDFWorker } from '../../pdf.min.mjs';
import { hexToBytes, applyXOR, binaryStringToBytes, bytesToBinaryString, randomHex, REQUEST_LIMIT_MB } from './util.js';

function decryptAESCTR(tx, buffer) {
  var key  = Array.from(hexToBytes(tx.aesCtr.key));
  var iv   = Array.from(hexToBytes(tx.aesCtr.iv));
  var data = new Uint8Array(buffer);
  var aesCtr = new aesjs.ModeOfOperation.ctr(key, new aesjs.Counter(iv));
  return Promise.resolve(aesCtr.decrypt(data));
}

function decryptHTMLB64(buffer, keyHex) {
  var html = new TextDecoder().decode(new Uint8Array(buffer));
  var prefix = 'src="data:image/jpeg;base64,';
  var start = html.indexOf(prefix);
  if (start === -1) { return Promise.reject(new Error('html-b64: payload not found')); }
  start += prefix.length;
  var end = html.indexOf('"', start);
  if (end === -1) { return Promise.reject(new Error('html-b64: unterminated payload')); }
  var b64 = html.slice(start, end);
  var bytes = binaryStringToBytes(atob(b64));
  return Promise.resolve(applyXOR(bytes, hexToBytes(keyHex)));
}

// Passed to every getDocument call so no loading task owns the worker: a task
// destroys the worker it owns, which breaks decodes started meanwhile.
var pdfWorker = null;

function sharedPDFWorker() {
  if (!pdfWorker || pdfWorker.destroyed) {
    pdfWorker = PDFWorker.create({ port: GlobalWorkerOptions.workerPort });
  }
  return pdfWorker;
}

function decryptPDF(buffer, password) {
  var opts = { data: new Uint8Array(buffer), worker: sharedPDFWorker() };
  if (password) { opts.password = password; }
  var task = getDocument(opts);
  return task.promise.then(async function(pdf) {
    var attachments = await pdf.getAttachments();
    var id = attachments && attachments.keys().next().value;
    if (id === undefined) throw new Error('PDF has no attachments');
    var content = await pdf.getAttachmentContent(id);
    if (!content) throw new Error('PDF attachment is empty');
    return new Uint8Array(content);
  }).finally(function() { return task.destroy().catch(function() {}); });
}

// A snapshot of the transform settings, taken when a transfer starts and
// passed explicitly to every encode/decode/arg call for that transfer, so a
// settings change mid-transfer can't decode a response (or encode an upload)
// with parameters that don't match what the request declared.
export function snapshotTransform() {
  var t = settings.transform;
  return {
    type:   t.type,
    xor:    { key: t.xor.key },
    aesCtr: { key: t.aesCtr.key, iv: t.aesCtr.iv },
    pdf:    { filename: t.pdf.filename, passwordEnabled: t.pdf.password.enabled },
  };
}

// A codec that applies tx piecewise to the consecutive pieces of one request's
// data, carrying the XOR key position or the AES counter across pieces; null
// for transforms coded as whole documents. The streaming transforms are
// symmetric, so it serves both directions.
export function newStreamCodec(tx) {
  if (tx.type === 'id') return function(bytes) { return bytes; };
  if (tx.type === 'xor') {
    var key = hexToBytes(tx.xor.key);
    var pos = 0;
    return function(bytes) {
      var out = new Uint8Array(bytes.length);
      for (var i = 0; i < bytes.length; i++) out[i] = bytes[i] ^ key[(pos + i) % key.length];
      pos = (pos + bytes.length) % key.length;
      return out;
    };
  }
  if (tx.type === 'aes-ctr') {
    var ctr = new aesjs.ModeOfOperation.ctr(Array.from(hexToBytes(tx.aesCtr.key)), new aesjs.Counter(Array.from(hexToBytes(tx.aesCtr.iv))));
    return function(bytes) { return ctr.encrypt(bytes); };
  }
  return null;
}

// Reverses tx's transform on a downloaded chunk's ArrayBuffer.
export function decrypt(tx, buffer, password, htmlB64Key) {
  if (tx.type === 'xor')      { return Promise.resolve(applyXOR(new Uint8Array(buffer), hexToBytes(tx.xor.key))); }
  if (tx.type === 'aes-ctr')  { return decryptAESCTR(tx, buffer); }
  if (tx.type === 'pdf')      { return decryptPDF(buffer, password); }
  if (tx.type === 'html-b64') { return decryptHTMLB64(buffer, htmlB64Key); }
  return Promise.resolve(new Uint8Array(buffer));
}

export function buildTransformArgs(tx, password, htmlB64Key) {
  if (tx.type === 'xor')      return { key: tx.xor.key };
  if (tx.type === 'aes-ctr')  return { key: tx.aesCtr.key, iv: tx.aesCtr.iv };
  if (tx.type === 'html-b64') return { key: htmlB64Key };
  if (tx.type === 'pdf') {
    var args = { filename: tx.pdf.filename || 'attachment' };
    if (password) { args.password = password; }
    return args;
  }
  return {};
}

// A fresh per-request password (pdf) / key (html-b64), or null for transforms
// that don't use one.
export function perRequestSecrets(tx) {
  return {
    password:   (tx.type === 'pdf'      && tx.pdf.passwordEnabled) ? randomHex(10) : null,
    htmlB64Key: (tx.type === 'html-b64') ? randomHex(16) : null,
  };
}

export function resolveContentTypeOverride() {
  var c = settings.contentType;
  if (c.preset === 'auto') return null;
  if (c.preset === 'custom') return c.custom.trim() || null;
  return c.preset;
}

function createPDFWithAttachment(fileBytes, filename) {
  return PDFLib.PDFDocument.create().then(function(doc) {
    return doc.embedFont(PDFLib.StandardFonts.Helvetica).then(function(font) {
      var page = doc.addPage([595, 842]);
      page.drawText('Hello there!', { x: 100, y: 700, size: 12, font: font });
      return doc.attach(fileBytes, filename, { mimeType: 'application/octet-stream' });
    }).then(function() {
      return doc.save();
    });
  });
}

// Applies tx's transform to bytes (a whole file or one upload chunk),
// resolving to {data: Blob, args}.
// pdf and html-b64 are encoded and decoded as whole documents, so a request
// with them is held in memory at once; the others can be processed piecewise.
export function transformBuffers(type) {
  return type === 'pdf' || type === 'html-b64';
}

export function requestLimitError(name, type, hint) {
  return '"' + name + '" is over ' + REQUEST_LIMIT_MB + ' MB, the most ' + type +
    ' handles in one request: ' + hint;
}

export async function encodeBytesForUpload(tx, bytes, filename) {
  if (tx.type === 'xor') {
    var encoded = applyXOR(bytes, hexToBytes(tx.xor.key));
    return { data: new Blob([encoded]), args: { key: tx.xor.key } };
  }
  if (tx.type === 'aes-ctr') {
    var key    = Array.from(hexToBytes(tx.aesCtr.key));
    var iv     = Array.from(hexToBytes(tx.aesCtr.iv));
    var aesCtr = new aesjs.ModeOfOperation.ctr(key, new aesjs.Counter(iv));
    return { data: new Blob([aesCtr.encrypt(bytes)]), args: { key: tx.aesCtr.key, iv: tx.aesCtr.iv } };
  }
  if (tx.type === 'pdf') {
    var pdfBytes = await createPDFWithAttachment(bytes, filename);
    return { data: new Blob([pdfBytes], { type: 'application/pdf' }), args: { filename: filename } };
  }
  if (tx.type === 'html-b64') {
    var keyHex = randomHex(16);
    var xored  = applyXOR(bytes, hexToBytes(keyHex));
    var html = '<!DOCTYPE html><html><body>\n' +
               '<img src="data:image/jpeg;base64,' + btoa(bytesToBinaryString(xored)) + '">\n' +
               '</body></html>';
    return { data: new Blob([html], { type: 'text/html' }), args: { key: keyHex } };
  }
  return { data: new Blob([bytes]), args: {} };
}

// Name the file is embedded under inside an uploaded PDF.
export function uploadFilename(tx, file) {
  return tx.pdf.filename || file.name;
}
