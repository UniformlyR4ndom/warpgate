import { html } from './html.js';
import { useState, useEffect } from '../vendor/hooks.module.js';
import { state } from '../store.js';
import { connectOutbox, reconnectOutbox, disconnectOutbox, dismissNotice } from '../lib/outbox.js';

const SCRIPT_URL = 'push-to-wgclip.ps1';
// The push function's default outbox: ~\wgclip-outbox.
const DEFAULT_FOLDER = 'wgclip-outbox';

function statusText(o) {
  switch (o.status) {
    case 'reconnect': return `Folder "${o.folder}" needs permission again.`;
    case 'other-tab': return 'Another warpgate tab handles this outbox.';
    case 'watching':  return `Watching "${o.folder}".`;
    case 'auth':      return 'Paused: the server rejected the auth key. Enter the right key to resume.';
    case 'error':     return `Error: ${o.error}`;
    default:          return 'Not connected.';
  }
}

export function OutboxSettings() {
  var o = state.outbox;
  var connected = o.status !== 'off';
  return html`
    <div class="outbox-status ${o.status === 'error' || o.status === 'auth' ? 'outbox-warn' : ''}">${statusText(o)}</div>
    ${o.status === 'off' && html`
      <div class="outbox-hint">push-to-wgclip writes to <code>~\\${DEFAULT_FOLDER}</code> by default; connect that folder.</div>`}
    ${o.status !== 'off' && o.folder !== DEFAULT_FOLDER && html`
      <div class="outbox-hint outbox-warn">
        push-to-wgclip writes to <code>~\\${DEFAULT_FOLDER}</code> by default. For "${o.folder}", set
        <code>$env:WGCLIP_OUTBOX</code> to its full path first.
      </div>`}
    <div class="outbox-buttons">
      ${o.status === 'off' && html`
        <button class="choose-btn" onClick=${connectOutbox} title="Pick the folder push-to-wgclip writes to, e.g. wgclip-outbox in your home directory. Needs read/write access.">Connect folder</button>`}
      ${o.status === 'reconnect' && html`
        <button class="choose-btn" onClick=${reconnectOutbox} title="Grant access to the previously picked folder again.">Reconnect</button>`}
      ${connected && html`
        <button class="choose-btn" onClick=${disconnectOutbox} title="Stop watching and forget the folder.">Disconnect</button>`}
      <button class="choose-btn" onClick=${() => (state.outbox.showScript = true)} title="Show the PowerShell function that pushes text into the outbox.">PowerShell function</button>
    </div>
    ${o.events.length > 0 && html`
      <ul class="outbox-events">
        ${o.events.map((ev) => html`<li key=${ev.t + ev.text} class=${ev.ok ? '' : 'outbox-warn'}>${ev.text}</li>`)}
      </ul>`}`;
}

export function OutboxNotices() {
  return html`
    <div class="notices">
      ${state.outbox.notices.map((n) => html`
        <div key=${n.id} class="notice ${n.ok ? '' : 'notice-error'}">
          <span>${n.text}</span>
          <button class="notice-close" onClick=${() => dismissNotice(n.id)} title="Dismiss">✕</button>
        </div>`)}
    </div>`;
}

export function OutboxScriptDialog() {
  var [script, setScript] = useState('');
  var [copied, setCopied] = useState(false);
  useEffect(() => {
    fetch(SCRIPT_URL).then((r) => r.text()).then(setScript).catch((e) => setScript('Failed to load: ' + e.message));
    function onKey(e) { if (e.key === 'Escape') state.outbox.showScript = false; }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  async function copy() {
    await navigator.clipboard.writeText(script);
    setCopied(true);
  }
  return html`
    <div class="lightbox" onClick=${(e) => { if (e.target === e.currentTarget) state.outbox.showScript = false; }}>
      <div class="script-dialog">
        <p>
          Click Copy, then run <code>Get-Clipboard -Raw | Invoke-Expression</code> in PowerShell to define
          <code>push-to-wgclip</code> for the session. Pasting the code directly can crash Windows PowerShell
          5.1 when it is taller than the console window.
        </p>
        <p>
          Usage: <code>Get-ChildItem | push-to-wgclip -Title 'listing'</code>. It writes to
          <code>~\\${DEFAULT_FOLDER}</code> unless <code>$env:WGCLIP_OUTBOX</code> is set; connect the same
          folder here.
        </p>
        <pre class="script-code">${script}</pre>
        <div class="outbox-buttons">
          <button class="choose-btn" onClick=${copy} disabled=${!script} title="Copy the function to the system clipboard.">${copied ? 'Copied' : 'Copy'}</button>
          <button class="choose-btn" onClick=${() => (state.outbox.showScript = false)} title="Close (Esc)">Close</button>
        </div>
      </div>
    </div>`;
}
