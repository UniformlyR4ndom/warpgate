import { html } from './html.js';
import { state } from '../store.js';
import { TransferList } from './transfers.js';
import { FileBrowser } from './browser.js';

export function DownloadTab() {
  var err = state.browse.dl.error;
  return html`
    <div>
      ${err && html`<div class="error">${err}</div>`}
      <${TransferList} root="dl" />
      <${FileBrowser} root="dl" />
    </div>`;
}
