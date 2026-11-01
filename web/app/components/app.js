import { html, Fragment } from './html.js';
import { settings, state } from '../store.js';
import { TABS, setTab, refreshActive } from '../nav.js';
import { Panel } from './panel.js';
import { DownloadTab } from './download.js';
import { UploadTab } from './upload.js';
import { ClipboardTab } from './clipboard.js';
import { BenchmarkTab } from './benchmark.js';
import { OutboxNotices, OutboxScriptDialog } from './outbox.js';

// Shown as a dot on the closed settings button.
function settingsAttention() {
  if (state.authFailed) return 'The server rejected the auth key';
  var st = state.outbox.status;
  if (st === 'reconnect' || st === 'auth' || st === 'error') return 'The outbox needs attention';
  return '';
}

export function App() {
  return html`
    <${Fragment}>
      <header>
        <div class="header-top">
          <h1>Warpgate</h1>
          <div class="header-actions">
          <button class="refresh-btn settings-btn ${settings.panel.open ? 'active' : ''}"
                  onClick=${() => (settings.panel.open = !settings.panel.open)} title="Show or hide the settings (Esc closes).">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M9.89 5.11 L9.92 2.22 L14.08 2.22 L14.11 5.11 L16.91 6.73 L19.43 5.31 L21.51 8.91 L19.02 10.38 L19.02 13.62 L21.51 15.09 L19.43 18.69 L16.91 17.27 L14.11 18.89 L14.08 21.78 L9.92 21.78 L9.89 18.89 L7.09 17.27 L4.57 18.69 L2.49 15.09 L4.98 13.62 L4.98 10.38 L2.49 8.91 L4.57 5.31 L7.09 6.73 Z"></path>
              <circle cx="12" cy="12" r="3"></circle>
            </svg>
            Settings
            ${!settings.panel.open && settingsAttention() && html`<span class="settings-dot" title=${settingsAttention()}></span>`}
          </button>
          <button class="refresh-btn" onClick=${refreshActive} title="Reload the current tab's list once, without turning on auto-refresh.">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M16.59 5.45 A8 8 0 1 1 7.41 5.45"></path>
              <polyline points="8.90 7.58 11.92 2.29 5.92 3.32"></polyline>
            </svg>
            Refresh
          </button>
          </div>
        </div>
      </header>

      <div class="layout">
        ${settings.panel.open && html`<${Panel} />`}
        <main>
          <div class="tabs">
            ${TABS.map((t) => html`
              <button class="tab-btn ${state.tab === t.id ? 'active' : ''}" onClick=${() => setTab(t.id)}>${t.label}</button>`)}
          </div>
          ${state.tab === 'download'  && html`<${DownloadTab} />`}
          ${state.tab === 'upload'    && html`<${UploadTab} />`}
          ${state.tab === 'clipboard' && html`<${ClipboardTab} />`}
          ${state.tab === 'benchmark' && html`<${BenchmarkTab} />`}
        </main>
      </div>
      <${OutboxNotices} />
      ${state.outbox.showScript && html`<${OutboxScriptDialog} />`}
    <//>`;
}
