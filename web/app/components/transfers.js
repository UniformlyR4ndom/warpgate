import { html } from './html.js';
import { state } from '../store.js';

function TransferRow(props) {
  var t = props.t;
  return html`
    <div class="transfer-row">
      <span class="transfer-name">${t.name}</span>
      ${t.progress !== null ? html`
        <div class="progress-bar">
          <div class="progress-bar-fill" style=${'width:' + Math.round(t.progress * 100) + '%'}></div>
          <span class="progress-bar-label">${Math.round(t.progress * 100) + '%'}</span>
        </div>` : html`
        <div class="progress-caption progress-caption-pulse">${t.direction === 'upload' ? 'Uploading…' : 'Downloading…'}</div>`}
    </div>`;
}

export function TransferList(props) {
  var transfers = state.transfers.filter((t) => t.root === props.root);
  return transfers.length > 0 && html`
    <div class="transfer-list">
      ${transfers.map((t) => html`<${TransferRow} key=${t.id} t=${t} />`)}
    </div>`;
}
