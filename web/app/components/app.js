import { html, Fragment } from './html.js';
import { state } from '../store.js';
import { setTab, refreshActive } from '../nav.js';
import { navigateTo } from '../lib/download.js';
import { Panel } from './panel.js';
import { DownloadTab } from './download.js';
import { UploadTab } from './upload.js';
import { ClipboardTab } from './clipboard.js';
import { BenchmarkTab } from './benchmark.js';

const TABS = [
  { id: 'download',  label: 'Download' },
  { id: 'upload',    label: 'Upload' },
  { id: 'clipboard', label: 'Clipboard' },
  { id: 'benchmark', label: 'Benchmark' },
];

function Breadcrumb() {
  return html`
    <nav class="breadcrumb">
      <span class="crumb" onClick=${() => navigateTo(-1)}>root</span>
      ${state.pathSegments.map((seg, i) => html`
        <span key=${i}><span class="separator">/</span><span class="crumb" onClick=${() => navigateTo(i)}>${seg}</span></span>`)}
    </nav>`;
}

export function App() {
  return html`
    <${Fragment}>
      <header>
        <div class="header-top">
          <h1>Warpgate</h1>
          <button class="refresh-btn" onClick=${refreshActive} title="Reload the current tab's list once, without turning on auto-refresh.">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M16.59 5.45 A8 8 0 1 1 7.41 5.45"></path>
              <polyline points="8.90 7.58 11.92 2.29 5.92 3.32"></polyline>
            </svg>
            Refresh
          </button>
        </div>
        ${state.tab === 'download' && html`<${Breadcrumb} />`}
      </header>

      <div class="layout">
        <${Panel} />
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
    <//>`;
}
