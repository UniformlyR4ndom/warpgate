import { html } from './html.js';
import { state } from '../store.js';
import { enterDir, navigateTo, downloadFile } from '../lib/download.js';
import { TransferList } from './transfers.js';
import { formatSize, formatTSAt, utcOffsetLabel, currentUTCOffset } from '../lib/util.js';

export function DownloadTab() {
  var off = currentUTCOffset();
  return html`
    <div>
      ${state.error && html`<div class="error">${state.error}</div>`}
      <${TransferList} direction="download" />
      ${!state.error && html`
        <table>
          <thead>
            <tr><th>Name</th><th class="col-mtime">${'Modified (' + utcOffsetLabel(off) + ')'}</th><th class="col-size">Size</th></tr>
          </thead>
          <tbody>
            ${state.pathSegments.length > 0 && html`
              <tr class="entry dir" onClick=${() => navigateTo(state.pathSegments.length - 2)}>
                <td><span class="icon">📁</span>..</td><td></td><td></td>
              </tr>`}
            ${state.entries.map((entry) => entry.warning ? html`
              <tr key=${entry.name} class="entry blocked" title=${entry.warning}>
                <td><span class="icon">⚠</span><span>${entry.name}</span></td>
                <td class="col-mtime"></td>
                <td class="col-size"></td>
              </tr>` : html`
              <tr key=${entry.name} class=${'entry ' + (entry.type === 'd' ? 'dir' : 'file')}
                  onClick=${() => (entry.type === 'd' ? enterDir(entry.name) : downloadFile(entry))}>
                <td><span class="icon">${entry.type === 'd' ? '📁' : '📄'}</span><span>${entry.name}</span></td>
                <td class="col-mtime">${entry.mtime ? formatTSAt(entry.mtime, off) : ''}</td>
                <td class="col-size">${entry.type === 'f' ? formatSize(entry.size) : ''}</td>
              </tr>`)}
          </tbody>
        </table>`}
    </div>`;
}
