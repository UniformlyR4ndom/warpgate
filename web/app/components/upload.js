import { html, text } from './html.js';
import { useRef, useEffect } from '../vendor/hooks.module.js';
import { state } from '../store.js';
import { uploadFiles, createDir, MKDIR_MAX_DEPTH } from '../lib/upload.js';
import { TransferList } from './transfers.js';
import { FileBrowser } from './browser.js';
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
      Drop files here or${' '}
      <button class="choose-btn" onClick=${() => input.click()}>choose files</button>
      <input type="file" style="display:none" multiple ref=${(el) => (input = el)} onChange=${onChange} />
    </div>`;
}

// FlatUploadTab is the upload tab under -ul-flat: one directory, listing only.
function FlatUploadTab() {
  var off = currentUTCOffset();
  return html`
    <div>
      ${state.uploadError && html`<div class="error">${state.uploadError}</div>`}
      <${DropZone} />
      <${TransferList} root="ul" />
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

function NewDirForm() {
  var input = useRef(null);
  useEffect(() => { input.current.focus(); }, []);
  var name = state.newDirName.trim();
  var valid = name !== '' && name.indexOf('/') === -1 && name.indexOf('\\') === -1;
  async function submit() {
    if (!valid) return;
    state.uploadError = null;
    try {
      await createDir(name);
      state.newDirName = null;
    } catch(err) {
      state.uploadError = 'Creating folder "' + name + '" failed: ' + err.message;
    }
  }
  function onKeyDown(e) {
    if (e.key === 'Enter') submit();
    if (e.key === 'Escape') { e.stopPropagation(); state.newDirName = null; }
  }
  return html`
    <span class="new-dir">
      <input type="text" ref=${input} ...${text(state, 'newDirName')} placeholder="Folder name" onKeyDown=${onKeyDown}
             title="Name of the new folder; no slashes." />
      <button class="choose-btn" disabled=${!valid} onClick=${submit}>Create</button>
      <button class="choose-btn" onClick=${() => (state.newDirName = null)}>Cancel</button>
    </span>`;
}

function NewDir() {
  if (state.newDirName !== null) return html`<${NewDirForm} />`;
  var full = state.browse.ul.segments.length >= MKDIR_MAX_DEPTH;
  return html`
    <button class="choose-btn" disabled=${full}
            title=${full ? 'Folders can be created at most ' + MKDIR_MAX_DEPTH + ' levels deep.' : 'Create a folder in the current directory.'}
            onClick=${() => (state.newDirName = '')}>New folder</button>`;
}

export function UploadTab() {
  if (state.ulFlat) return html`<${FlatUploadTab} />`;
  var listErr = state.browse.ul.error;
  return html`
    <div>
      ${state.uploadError && html`<div class="error">${state.uploadError}</div>`}
      ${listErr && html`<div class="error">${listErr}</div>`}
      <${DropZone} />
      <${TransferList} root="ul" />
      <${FileBrowser} root="ul" actions=${html`<${NewDir} />`} />
    </div>`;
}
