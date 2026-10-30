import { html, text, num, check, sel } from './html.js';
import { useEffect } from '../vendor/hooks.module.js';
import { settings, state, backendTypes, transformTypes } from '../store.js';
import { randomHex } from '../lib/util.js';
import { OutboxSettings } from './outbox.js';

// Tooltips, shared by each setting's label and its input.
const TIP = {
  auth: "Sent as the X-Auth header on every API request. Leave empty if the server was started with -no-auth.",
  pcrypt: "Encrypt JSON request bodies and WebSocket control messages using a key derived from the auth key, so a proxy inspecting plaintext traffic can't read transform keys or paths. Requires an auth key.",
  bsize: "Total size of the synthetic file transferred by the benchmark (1-1024 MB). Nothing is read from or written to disk - all data is synthesized.",
  bchunk: "Splits the benchmark transfer into multiple requests of this size, simulating a chunked transfer: {start,size} requests for download, chunked-upload sessions for upload (HTTP backend only; Websocket upload always sends the whole file as one connection's frames).",
  dlchunk: "Split a file download into multiple sequential requests instead of one, useful for very large files or flaky connections.",
  dlsize: "Size of each downloaded chunk, in megabytes.",
  ulchunk: "Split a file upload into multiple sequential requests instead of one, useful for very large files or flaky connections. HTTP POST backend only.",
  ulsize: "Size of each uploaded chunk, in megabytes.",
  auto: "Reload the current tab's list every second.",
  confirm: "Ask for confirmation before deleting a clipboard note.",
  backend: "Transport used for downloads, uploads, and the benchmark tab. WebSocket uses one connection per file with a configurable wire frame size.",
  frame: "Maximum size of each WebSocket frame. Clamped to the range the server allows.",
  ctype: "Overrides the Content-Type header on the download response. WebSocket downloads have no HTTP headers, so this only applies to the HTTP POST backend.",
  ccustom: "Custom Content-Type value to send with the download request.",
  ttype: "How file content is disguised in transit.",
  xorkey: "Hex-encoded key, XOR-ed with the file bytes on a repeating cycle. Any length. Uncheck 'auto' to enter your own.",
  xorauto: "Generate a new random key automatically.",
  aeskey: "Hex-encoded AES key: 16, 24, or 32 bytes (32 = AES-256). Uncheck 'auto' to enter your own.",
  aeskeyauto: "Generate a new random 32-byte key automatically.",
  aesiv: "Hex-encoded initialization vector, exactly 16 bytes (one AES block). Uncheck 'auto' to enter your own.",
  aesivauto: "Generate a new random IV automatically.",
  pdfname: "Filename the file is embedded under inside the PDF. Defaults to 'attachment' if left blank.",
  pdfpw: "Encrypt the generated PDF (AES-256) with a random password generated fresh per download and never stored server-side. Uploaded PDFs are always unencrypted.",
};

function Section(props) {
  var openKey = props.openKey;
  var open = settings.panel[openKey];
  return html`
    <section class="panel-section" style=${props.hidden ? 'display:none' : ''}>
      <h2 class="section-header" onClick=${() => (settings.panel[openKey] = !settings.panel[openKey])}>
        <span class="section-arrow">${open ? '▾' : '▸'}</span>${props.title}
      </h2>
      ${open && html`<div>${props.children}</div>`}
    </section>`;
}

function regenAuto(field) {
  var t = settings.transform;
  if (field === 'xor' && t.xor.auto) t.xor.key = randomHex(32);
  if (field === 'aes-key' && t.aesCtr.autoKey) t.aesCtr.key = randomHex(32);
  if (field === 'aes-iv' && t.aesCtr.autoIv) t.aesCtr.iv = randomHex(16);
}

export function Panel() {
  var tab = state.tab;
  var t = settings.transform;
  var tabTransport = tab === 'download' || tab === 'upload' || tab === 'benchmark';
  useEffect(() => {
    function onKey(e) {
      if (e.key !== 'Escape' || e.defaultPrevented || state.clipLightbox || state.outbox.showScript) return;
      // Esc in a field outside the panel belongs to that field.
      if (e.target.closest('input, textarea, select') && !e.target.closest('.panel')) return;
      settings.panel.open = false;
    }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  return html`
    <aside class="panel">
        <div class="panel-content">

          <${Section} title="Auth" openKey="authOpen">
            <label title=${TIP.auth}>Key (X-Auth header)</label>
            <div class="key-input-wrap">
              <input type="text" ...${text(settings.auth, 'key')} placeholder="leave empty to skip"
                     autocomplete="off" title=${TIP.auth} class=${state.authShowKey ? '' : 'key-masked'} />
              <button type="button" class="key-toggle" onClick=${() => (state.authShowKey = !state.authShowKey)}
                      title=${state.authShowKey ? 'Hide key' : 'Show key'}>👁</button>
            </div>
            <label class="auto-label ${settings.auth.key ? '' : 'disabled'}" title=${TIP.pcrypt}>
              <input type="checkbox" ...${check(settings.paramCrypt, 'enabled')} disabled=${!settings.auth.key} /> Parameter obfuscation
            </label>
          <//>

          <${Section} title="Benchmark" openKey="benchmarkOpen" hidden=${tab !== 'benchmark'}>
            <label title=${TIP.bsize}>Size (MB)</label>
            <input type="number" ...${num(settings.benchmark, 'sizeMB')} min="1" max="1024" title=${TIP.bsize} />
            <label title=${TIP.bchunk}>Chunk size (MB)</label>
            <input type="number" ...${num(settings.benchmark, 'chunkSizeMB')} min="1" max=${settings.benchmark.sizeMB} title=${TIP.bchunk} />
          <//>

          <${Section} title="Chunking" openKey="chunkingOpen" hidden=${tab !== 'download' && tab !== 'upload'}>
            ${tab === 'download' && html`<div>
              <label class="auto-label" title=${TIP.dlchunk}><input type="checkbox" ...${check(settings.chunking, 'enabled')} /> Enable chunking</label>
              ${settings.chunking.enabled && html`<div>
                <label title=${TIP.dlsize}>Chunk size (MB)</label>
                <input type="number" ...${num(settings.chunking, 'chunkSize')} min="1" title=${TIP.dlsize} />
                <label class="auto-label ${state.hasFileSystemAccess ? '' : 'disabled'}"
                       title=${state.hasFileSystemAccess ? 'Write chunks directly to a single file on disk as they arrive (File System Access API) instead of downloading each chunk as a separate file.' : 'File System Access API is required for efficient reassembly'}>
                  <input type="checkbox" ...${check(settings.chunking, 'reassemble')} disabled=${!state.hasFileSystemAccess} /> Reassemble chunks
                </label>
              </div>`}
            </div>`}
            ${tab === 'upload' && html`<div>
              ${settings.backend === 'http' ? html`<div>
                <label class="auto-label" title=${TIP.ulchunk}><input type="checkbox" ...${check(settings.uploadChunking, 'enabled')} /> Enable chunking</label>
                ${settings.uploadChunking.enabled && html`<div>
                  <label title=${TIP.ulsize}>Chunk size (MB)</label>
                  <input type="number" ...${num(settings.uploadChunking, 'chunkSizeMB')} min="1" title=${TIP.ulsize} />
                </div>`}
              </div>` : html`<div class="upload-empty">Chunked upload is only available on the HTTP POST backend.</div>`}
            </div>`}
          <//>

          <${Section} title="General" openKey="generalOpen">
            <label class="auto-label" title=${TIP.auto}><input type="checkbox" ...${check(settings, 'autoRefresh')} /> Auto-refresh</label>
            <label class="auto-label" title=${TIP.confirm}><input type="checkbox" ...${check(settings, 'confirmDelete')} /> Confirm note delete</label>
            ${tabTransport && html`<div>
              <label title=${TIP.backend}>Backend</label>
              <select ...${sel(settings, 'backend')} title=${TIP.backend}>
                ${backendTypes.map((b) => html`<option value=${b.id}>${b.label}</option>`)}
              </select>
            </div>`}
            ${tabTransport && settings.backend === 'ws' && html`<div>
              <label title=${TIP.frame}>WS frame size (KB)</label>
              <input type="number" ...${num(settings, 'wsFrameSizeKB')} min=${state.wsFrameMinKB} max=${state.wsFrameMaxKB} title=${TIP.frame} />
            </div>`}
            ${tab === 'download' && settings.backend === 'http' && html`<div>
              <label title=${TIP.ctype}>Content-Type</label>
              <select ...${sel(settings.contentType, 'preset')} title=${TIP.ctype}>
                <option value="auto">Auto (transform default)</option>
                <option value="application/octet-stream">application/octet-stream</option>
                <option value="application/pdf">application/pdf</option>
                <option value="image/jpeg">image/jpeg</option>
                <option value="image/png">image/png</option>
                <option value="application/zip">application/zip</option>
                <option value="application/json">application/json</option>
                <option value="text/html">text/html</option>
                <option value="video/mp4">video/mp4</option>
                <option value="custom">Custom…</option>
              </select>
              ${settings.contentType.preset === 'custom' && html`
                <input type="text" ...${text(settings.contentType, 'custom')} placeholder="e.g. application/vnd.custom-type" title=${TIP.ccustom} />`}
            </div>`}
          <//>

          <${Section} title="Outbox" openKey="outboxOpen" hidden=${!state.outbox.supported}>
            <${OutboxSettings} />
          <//>

          <${Section} title="Transform" openKey="transformOpen">
            <label title=${TIP.ttype}>Type</label>
            <select ...${sel(settings.transform, 'type')} title=${TIP.ttype}>
              ${transformTypes.map((tt) => html`<option value=${tt.id}>${tt.label}</option>`)}
            </select>

            ${t.type === 'xor' && html`<div>
              <div class="auto-row">
                <label title=${TIP.xorkey}>Key (hex)</label>
                <label class="auto-label" title=${TIP.xorauto}><input type="checkbox" ...${check(t.xor, 'auto')} onChange=${(e) => { t.xor.auto = e.target.checked; regenAuto('xor'); }} /> auto</label>
              </div>
              <input type="text" ...${text(t.xor, 'key')} readonly=${t.xor.auto} class=${t.xor.auto ? 'readonly' : ''} title=${TIP.xorkey} />
            </div>`}

            ${t.type === 'aes-ctr' && html`<div>
              <div class="auto-row">
                <label title=${TIP.aeskey}>Key (hex, 32 bytes)</label>
                <label class="auto-label" title=${TIP.aeskeyauto}><input type="checkbox" ...${check(t.aesCtr, 'autoKey')} onChange=${(e) => { t.aesCtr.autoKey = e.target.checked; regenAuto('aes-key'); }} /> auto</label>
              </div>
              <input type="text" ...${text(t.aesCtr, 'key')} readonly=${t.aesCtr.autoKey} class=${t.aesCtr.autoKey ? 'readonly' : ''} title=${TIP.aeskey} />
              <div class="auto-row">
                <label title=${TIP.aesiv}>IV (hex, 16 bytes)</label>
                <label class="auto-label" title=${TIP.aesivauto}><input type="checkbox" ...${check(t.aesCtr, 'autoIv')} onChange=${(e) => { t.aesCtr.autoIv = e.target.checked; regenAuto('aes-iv'); }} /> auto</label>
              </div>
              <input type="text" ...${text(t.aesCtr, 'iv')} readonly=${t.aesCtr.autoIv} class=${t.aesCtr.autoIv ? 'readonly' : ''} title=${TIP.aesiv} />
            </div>`}

            ${t.type === 'pdf' && html`<div>
              <label title=${TIP.pdfname}>Attachment name</label>
              <input type="text" ...${text(t.pdf, 'filename')} placeholder="attachment" title=${TIP.pdfname} />
              ${tab === 'download' && html`<div class="auto-row">
                <label class="auto-label" title=${TIP.pdfpw}><input type="checkbox" ...${check(t.pdf.password, 'enabled')} /> Password protect</label>
              </div>`}
            </div>`}
          <//>

        </div>
    </aside>`;
}
