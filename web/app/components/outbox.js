import { html, text } from './html.js';
import { useState, useEffect } from '../vendor/hooks.module.js';
import { state, settings } from '../store.js';
import { connectOutbox, reconnectOutbox, disconnectOutbox } from '../lib/outbox.js';

const SHELLS = [
  { id: 'ps', label: 'PowerShell', url: 'static/push-to-wgclip.ps1' },
  { id: 'sh', label: 'Bash / zsh', url: 'static/push-to-wgclip.sh' },
];
// The push functions' default outbox folder, in the home directory.
const DEFAULT_FOLDER = 'wgclip-outbox';
const DEFAULT_PATH = (isWindows() ? '~\\' : '~/') + DEFAULT_FOLDER;
const ENV_SYNTAX = isWindows() ? '$env:WGCLIP_OUTBOX' : 'WGCLIP_OUTBOX';

// The configured outbox path (settings.outboxPath), trimmed; '' means the default.
function outboxPath() { return settings.outboxPath.trim(); }

// Last path segment of a '/'- or '\'-separated path, the folder push2wgclip
// writes into; falls back to the default so the mismatch check has something to compare.
function targetFolder() {
  var parts = outboxPath().replace(/[\/\\]+$/, '').split(/[\/\\]/);
  return parts[parts.length - 1] || DEFAULT_FOLDER;
}

// A WGCLIP_OUTBOX assignment baking the configured path into the copied function,
// quoted for the shell ('' = PowerShell literal escape, '\'' = bash literal escape).
function envAssignment(shell) {
  var p = outboxPath();
  if (!p) return '';
  return shell === 'ps'
    ? "$env:WGCLIP_OUTBOX = '" + p.replace(/'/g, "''") + "'"
    : "export WGCLIP_OUTBOX='" + p.replace(/'/g, "'\\''") + "'";
}

function statusText(o) {
  switch (o.status) {
    case 'reconnect': return 'Needs permission';
    case 'other-tab': return 'Handled by another warpgate tab';
    case 'watching':  return `Watching "${o.folder}"`;
    case 'auth':      return 'Paused: auth key rejected';
    case 'error':     return `Error: ${o.error}`;
    default:          return 'Not connected';
  }
}

function statusTitle(o) {
  switch (o.status) {
    case 'reconnect': return `The browser needs permission to access "${o.folder}" again. Click Reconnect.`;
    case 'other-tab': return 'Another warpgate tab is watching this outbox. This tab takes over when that one closes.';
    case 'auth':      return 'The server rejected the auth key. Enter the right key to resume.';
    default:          return '';
  }
}

export function OutboxSettings() {
  var o = state.outbox;
  var connected = o.status !== 'off';
  return html`
    <div class="outbox-status outbox-st-${o.status}" title=${statusTitle(o)}>
      <span class="outbox-dot"></span><span>${statusText(o)}</span>
    </div>
    ${o.status === 'off' && html`
      <div class="outbox-hint">Script writes to <code>${outboxPath() || DEFAULT_PATH}</code></div>`}
    ${o.status !== 'off' && o.folder !== targetFolder() && html`
      <div class="outbox-hint outbox-mismatch"
           title=${`push2wgclip writes to "${targetFolder()}" but this tab watches "${o.folder}". Set the outbox path to the folder you connected (and copy the Push function again), or connect "${targetFolder()}".`}>
        Watching "${o.folder}", but script writes to "${targetFolder()}"
      </div>`}
    <label title="The folder push2wgclip writes to. Baked into the copied Push function as ${ENV_SYNTAX} so you don't set it by hand. Use an absolute path (a leading ~ is not expanded). Leave empty for the default ${DEFAULT_PATH}.">Script outbox path</label>
    <input type="text" class="outbox-path" ...${text(settings, 'outboxPath')} placeholder=${DEFAULT_PATH}
      title="The folder push2wgclip writes to. Baked into the copied Push function as ${ENV_SYNTAX} so you don't set it by hand. Use an absolute path (a leading ~ is not expanded). Leave empty for the default ${DEFAULT_PATH}." />
    <div class="outbox-buttons outbox-stacked">
      ${o.status === 'off' && html`
        <button class="choose-btn" onClick=${connectOutbox} title="Pick the folder push2wgclip writes to, e.g. wgclip-outbox in your home directory. Needs read/write access.">Connect folder</button>`}
      ${o.status === 'reconnect' && html`
        <button class="choose-btn" onClick=${reconnectOutbox} title="Grant access to the previously picked folder again.">Reconnect</button>`}
      ${connected && html`
        <button class="choose-btn" onClick=${disconnectOutbox} title="Stop watching and forget the folder.">Disconnect</button>`}
      <button class="choose-btn" onClick=${() => (state.outbox.showScript = true)} title="Show the PowerShell and bash/zsh functions that push text into the outbox.">Push function</button>
    </div>`;
}

function isWindows() {
  var p = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || '';
  return /win/i.test(p);
}

export function OutboxScriptDialog() {
  var [shell, setShell] = useState(() => (isWindows() ? 'ps' : 'sh'));
  var [scripts, setScripts] = useState({});
  var [copied, setCopied] = useState(false);
  useEffect(() => {
    function onKey(e) { if (e.key === 'Escape') state.outbox.showScript = false; }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  useEffect(() => {
    if (scripts[shell] !== undefined) return;
    var url = SHELLS.find((s) => s.id === shell).url;
    fetch(url)
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error('HTTP ' + r.status))))
      .then((text) => setScripts((m) => ({ ...m, [shell]: text })))
      .catch((e) => setScripts((m) => ({ ...m, [shell]: 'Failed to load: ' + e.message })));
  }, [shell]);
  var script = scripts[shell] || '';
  var loaded = script && !script.startsWith('Failed to load:');
  var assign = envAssignment(shell);
  var shown = assign && loaded ? assign + '\n\n' + script : script;
  var path = outboxPath();
  function pick(id) { setShell(id); setCopied(false); }
  async function copy() {
    await navigator.clipboard.writeText(shown);
    setCopied(true);
  }
  return html`
    <div class="lightbox" onClick=${(e) => { if (e.target === e.currentTarget) state.outbox.showScript = false; }}>
      <div class="script-dialog">
        <div class="tabs">
          ${SHELLS.map((s) => html`
            <button class="tab-btn ${shell === s.id ? 'active' : ''}" onClick=${() => pick(s.id)}>${s.label}</button>`)}
        </div>
        ${shell === 'ps' ? html`
          <p>
            Click Copy, then run <code>Get-Clipboard -Raw | Invoke-Expression</code> in PowerShell to define${' '}
            <code>push2wgclip</code> for the session. Pasting the code directly can crash Windows PowerShell
            5.1 when it is taller than the console window.
          </p>
          <p>
            Usage: <code>Get-ChildItem | push2wgclip -Title 'listing'</code>.${' '}
            ${path
              ? html`This copy sets <code>$env:WGCLIP_OUTBOX</code> to <code>${path}</code>; connect that folder here.`
              : html`It writes to <code>~\\${DEFAULT_FOLDER}</code> unless <code>$env:WGCLIP_OUTBOX</code> is set; connect the same folder here.`}
          </p>` : html`
          <p>
            Click Copy and paste it into a bash or zsh terminal to define <code>push2wgclip</code> for the
            session. To keep it, save it as <code>~/push2wgclip.sh</code> and add${' '}
            <code>source ~/push2wgclip.sh</code> to <code>~/.bashrc</code> or <code>~/.zshrc</code>.
          </p>
          <p>
            Usage: <code>ls -la | push2wgclip -t 'listing'</code> or <code>push2wgclip -f notes.txt</code>.${' '}
            ${path
              ? html`This copy sets <code>$WGCLIP_OUTBOX</code> to <code>${path}</code>; connect that folder here.`
              : html`It writes to <code>~/${DEFAULT_FOLDER}</code> unless <code>$WGCLIP_OUTBOX</code> is set; connect the same folder here.`}
          </p>`}
        <pre class="script-code">${shown}</pre>
        <div class="outbox-buttons">
          <button class="choose-btn" onClick=${copy} disabled=${!script} title="Copy the function to the system clipboard.">${copied ? 'Copied' : 'Copy'}</button>
          <button class="choose-btn" onClick=${() => (state.outbox.showScript = false)} title="Close (Esc)">Close</button>
        </div>
      </div>
    </div>`;
}
