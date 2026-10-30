import { html } from './html.js';
import { state } from '../store.js';
import { uploadFiles } from '../lib/upload.js';
import { TransferList } from './transfers.js';
import { formatSize, formatTSAt, utcOffsetLabel, currentUTCOffset } from '../lib/util.js';

function DropZone() {
  var input = null;
  function onDragLeave(e) {
    if (!e.relatedTarget || !e.currentTarget.contains(e.relatedTarget)) e.currentTarget.classList.remove('drag-active');
  }
  function onDrop(e) {
    e.preventDefault();
    e.currentTarget.classList.remove('drag-active');
    uploadFiles(e.dataTransfer.files);
  }
  function onChange(e) {
    uploadFiles(e.target.files);
    e.target.value = '';
  }
  return html`
    <div class="drop-zone"
         onDragOver=${(e) => e.preventDefault()}
         onDragEnter=${(e) => { e.preventDefault(); e.currentTarget.classList.add('drag-active'); }}
         onDragLeave=${onDragLeave}
         onDrop=${onDrop}>
      Drop files here or
      <button class="choose-btn" onClick=${() => input.click()}>choose files</button>
      <input type="file" style="display:none" multiple ref=${(el) => (input = el)} onChange=${onChange} />
    </div>`;
}

export function UploadTab() {
  var off = currentUTCOffset();
  return html`
    <div>
      ${state.uploadError && html`<div class="error">${state.uploadError}</div>`}
      <${DropZone} />
      <${TransferList} direction="upload" />
      ${state.uploadEntries.length > 0 && html`
        <table>
          <thead>
            <tr><th>Name</th><th class="col-mtime">${'Modified (' + utcOffsetLabel(off) + ')'}</th><th class="col-size">Size</th></tr>
          </thead>
          <tbody>
            ${state.uploadEntries.map((entry) => html`
              <tr key=${entry.name} class="entry file">
                <td><span class="icon">📄</span><span>${entry.name}</span></td>
                <td class="col-mtime">${entry.mtime ? formatTSAt(entry.mtime, off) : ''}</td>
                <td class="col-size">${formatSize(entry.size)}</td>
              </tr>`)}
          </tbody>
        </table>`}
      ${!state.uploadError && state.uploadEntries.length === 0 && html`<div class="upload-empty">No files uploaded yet.</div>`}
    </div>`;
}
