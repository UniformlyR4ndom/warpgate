import { html } from './html.js';
import { state } from '../store.js';
import { enterDir, navigateTo, downloadFile } from '../lib/download.js';
import { formatSize, formatTSAt, utcOffsetLabel, currentUTCOffset } from '../lib/util.js';

function Breadcrumb(props) {
  var root = props.root;
  return html`
    <nav class="breadcrumb">
      <span class="crumb" onClick=${() => navigateTo(root, -1)}>root</span>
      ${state.browse[root].segments.map((seg, i) => html`
        <span key=${i}><span class="separator">/</span><span class="crumb" onClick=${() => navigateTo(root, i)}>${seg}</span></span>`)}
    </nav>`;
}

// FileBrowser lists one root (dl or ul); clicking a file downloads it.
// props.actions renders next to the breadcrumb.
export function FileBrowser(props) {
  var root = props.root;
  var b = state.browse[root];
  var off = currentUTCOffset();
  return html`
    <div>
      <div class="browser-bar">
        <${Breadcrumb} root=${root} />
        ${props.actions}
      </div>
      ${!b.error && html`
        <table>
          <thead>
            <tr><th>Name</th><th class="col-mtime">${'Modified (' + utcOffsetLabel(off) + ')'}</th><th class="col-size">Size</th></tr>
          </thead>
          <tbody>
            ${b.segments.length > 0 && html`
              <tr class="entry dir" onClick=${() => navigateTo(root, b.segments.length - 2)}>
                <td><span class="icon">📁</span>..</td><td></td><td></td>
              </tr>`}
            ${b.entries.map((entry) => html`
              <tr key=${entry.name} class=${'entry ' + (entry.type === 'd' ? 'dir' : 'file')}
                  onClick=${() => (entry.type === 'd' ? enterDir(root, entry.name) : downloadFile(root, entry))}>
                <td><span class="icon">${entry.type === 'd' ? '📁' : '📄'}</span><span>${entry.name}</span></td>
                <td class="col-mtime">${entry.mtime ? formatTSAt(entry.mtime, off) : ''}</td>
                <td class="col-size">${entry.type === 'f' ? formatSize(entry.size) : ''}</td>
              </tr>`)}
          </tbody>
        </table>`}
    </div>`;
}
