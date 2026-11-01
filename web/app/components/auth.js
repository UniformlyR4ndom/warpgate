import { html, text, check } from './html.js';
import { settings, state, authKeyHash, authFingerprint, submitPassphrase, clearAuthK } from '../store.js';

const TIP = {
  pass: "The server's -auth-key. A key is derived from it (about a second) and kept for this browser tab; the passphrase itself is not stored. Leave empty if the server was started with -no-auth.",
  fp: 'Fingerprint of the derived key. The server logs the same value at startup.',
  pcrypt: "Encrypt JSON request bodies and WebSocket control messages using keys derived from the passphrase, so a proxy inspecting plaintext traffic can't read transform keys or paths. Requires a key.",
};

function onKeyDown(e, canCancel) {
  if (e.key === 'Enter') { e.preventDefault(); submitPassphrase(); }
  else if (e.key === 'Escape' && canCancel) { e.preventDefault(); state.authEditing = false; }
}

export function AuthSettings() {
  var hasKey = !!authKeyHash.value;
  var deriving = state.authDeriving !== null;
  var showForm = !hasKey || state.authEditing || state.authFailed;
  var canCancel = hasKey && !state.authFailed;
  return html`
    <div>
      ${hasKey && !showForm && html`
        <div class="auth-status">
          Key ID: <code class="auth-fp" title=${TIP.fp}>${authFingerprint.value}</code>
        </div>
        <div class="outbox-buttons auth-buttons">
          <button class="choose-btn" onClick=${() => (state.authEditing = true)} title="Enter a different passphrase.">Change</button>
          <button class="choose-btn" onClick=${clearAuthK} title="Remove the key from this browser tab.">Clear</button>
        </div>`}
      ${showForm && html`
        <label title=${TIP.pass}>Passphrase</label>
        <div class="key-input-wrap">
          <input type="text" ...${text(state, 'authPassphrase')} placeholder="leave empty for -no-auth"
                 autocomplete="off" title=${TIP.pass} class=${state.authShowKey ? '' : 'key-masked'}
                 disabled=${deriving} onKeyDown=${(e) => onKeyDown(e, canCancel)} />
          <button type="button" class="key-toggle" onClick=${() => (state.authShowKey = !state.authShowKey)}
                  title=${state.authShowKey ? 'Hide passphrase' : 'Show passphrase'}>👁</button>
        </div>
        <div class="outbox-buttons auth-buttons">
          <button class="choose-btn" onClick=${submitPassphrase} disabled=${deriving} title="Derive the key and use it (Enter).">Set</button>
          ${canCancel && html`<button class="choose-btn" onClick=${() => (state.authEditing = false)} disabled=${deriving}
                                      title="Keep the current key (Esc).">Cancel</button>`}
        </div>
        ${deriving && html`<div class="auth-hint">Deriving key… ${Math.round(state.authDeriving * 100)}%</div>`}`}
      ${state.authFailed && html`<div class="auth-hint auth-error">${hasKey ? 'The server rejected the key.' : 'The server requires a passphrase.'}</div>`}
      ${state.authBlocked && html`<div class="auth-hint auth-error">Too many failed attempts from this IP; wait up to 10 minutes or restart the server.</div>`}
      ${state.authError && html`<div class="auth-hint auth-error">${state.authError}</div>`}
      <label class="auto-label ${hasKey ? '' : 'disabled'}" title=${TIP.pcrypt}>
        <input type="checkbox" ...${check(settings.paramCrypt, 'enabled')} disabled=${!hasKey} /> Parameter obfuscation
      </label>
    </div>`;
}
