import { html, text, sel } from './html.js';
import { useRef, useEffect } from '../vendor/hooks.module.js';
import { state, settings } from '../store.js';
import { formatTS, saveURL } from '../lib/util.js';
import { visibleClipItems, submitClip, canSubmitClip, cancelEdit, editClip, deleteClip, copyClip, downloadClip,
         handleClipPaste, removePendingAttachment, ensureAttachment, openAttachment, isBlobURL,
         isAttachmentRemoved, toggleRemoveAttachment, downloadAttachment } from '../lib/clipboard.js';

const DOWNLOAD_ICON = html`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>`;

function captionKey(e) {
  if (e.key === 'Enter') e.preventDefault();
}

function Segments(props) {
  return props.segs.map((seg, i) => html`<span key=${i} class=${seg.m ? 'clip-hl' : ''}>${seg.t}</span>`);
}

function Composer(props) {
  var editing = state.clipEditId !== null;
  function onTitleKey(e) {
    if (e.key === 'Escape') { e.preventDefault(); cancelEdit(); }
    if (e.key === 'Enter')  { e.preventDefault(); props.textareaRef.current.focus(); }
  }
  function onTextKey(e) {
    if (e.key === 'Escape') { e.preventDefault(); cancelEdit(); }
  }
  return html`
    <div class="clip-compose">
      <input type="text" class="clip-title-input" ...${text(state, 'clipTitle')} maxlength="200" placeholder="Title (optional)"
             onPaste=${handleClipPaste} onKeyDown=${onTitleKey} />
      <textarea class="clip-textarea" ...${text(state, 'clipText')} ref=${props.textareaRef}
                placeholder="Type a note, or paste an image…" onPaste=${handleClipPaste} onKeyDown=${onTextKey}></textarea>
      ${state.pendingAttachments.length > 0 && html`
        <div class="clip-pending">
          ${state.pendingAttachments.map((pa, i) => html`
            <div class="clip-fig" key=${pa.url}>
              <div class="clip-thumb">
                <img src=${pa.url} alt="" />
                <button class="clip-thumb-del" onClick=${() => removePendingAttachment(i)} title="Remove image">✕</button>
              </div>
              <input type="text" class="clip-caption-input" ...${text(state.pendingAttachments[i], 'caption')} maxlength="200"
                     placeholder="Caption" title=${pa.caption || 'Caption (optional); also used as the download file name'}
                     onKeyDown=${captionKey} />
            </div>`)}
        </div>`}
      <div class="clip-btn-row">
        <button class="choose-btn" onClick=${submitClip} disabled=${!canSubmitClip()}>${editing ? 'Update' : 'Submit'}</button>
        ${editing && html`<button class="choose-btn" onClick=${cancelEdit}>Cancel</button>`}
      </div>
    </div>`;
}

function ListHeader() {
  return html`
    <div class="clip-list-header">
      <input type="search" class="clip-search" ...${text(state, 'clipSearch')} placeholder="Search notes…"
             title="Case-insensitive substring search; matches are highlighted"
             onKeyDown=${(e) => { if (e.key === 'Escape') state.clipSearch = ''; }} />
      <select class="clip-search-scope" ...${sel(settings, 'clipSearchScope')} title="Which fields the search looks at">
        <option value="all">Title + content</option>
        <option value="title">Title</option>
        <option value="content">Content</option>
      </select>
      <button class="clip-sort-btn" onClick=${() => (settings.clipSortDesc = !settings.clipSortDesc)}
              title=${settings.clipSortDesc ? 'Newest first — click to sort oldest first' : 'Oldest first — click to sort newest first'}>
        <svg class=${settings.clipSortDesc ? '' : 'clip-sort-asc'} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"></line><polyline points="19 12 12 19 5 12"></polyline></svg>
        <span>${settings.clipSortDesc ? 'Newest first' : 'Oldest first'}</span>
      </button>
    </div>`;
}

function Thumb(props) {
  var att = props.att;
  var item = props.item;
  useEffect(() => { ensureAttachment(att); }, [att.id]);
  var img = state.clipImages[att.id];
  var removed = isAttachmentRemoved(att.id);
  var loaded = isBlobURL(img);
  return html`
    <div class="clip-fig">
      <div class="clip-thumb ${removed ? 'clip-thumb-removing' : ''}">
        ${loaded && html`<img src=${img} alt="" title="Open image" onClick=${() => openAttachment(item, att)} />`}
        ${(!img || img === 'loading') && html`<span class="clip-thumb-ph">…</span>`}
        ${img === 'error' && html`<span class="clip-thumb-ph" title="Failed to load">⚠</span>`}
        ${loaded && !props.editing && html`
          <button class="clip-thumb-dl" onClick=${() => downloadAttachment(item, att)} title="Download image">${DOWNLOAD_ICON}</button>`}
        ${props.editing && html`
          <button class="clip-thumb-del" onClick=${() => toggleRemoveAttachment(att.id)}
                  title=${removed ? 'Keep image (will no longer be removed on Update)' : 'Remove image on Update'}>${removed ? '↺' : '✕'}</button>`}
      </div>
      ${props.editing ? html`
        <input type="text" class="clip-caption-input" ...${text(state.clipCaptionEdits, att.id)} maxlength="200"
               placeholder="Caption" disabled=${removed}
               title=${state.clipCaptionEdits[att.id] || 'Caption (optional); also used as the download file name'}
               onKeyDown=${captionKey} />` : att.caption && html`
        <div class="clip-caption" title=${att.caption}><${Segments} segs=${props.segs} /></div>`}
    </div>`;
}

function Note(props) {
  var v = props.v;
  var item = v.item;
  var atts = item.attachments || [];
  return html`
    <div class="clip-item">
      <div class="clip-meta">
        <span class="clip-head">
          ${item.source === 'outbox' && html`<span class="clip-source" title="Pushed via outbox by a local script">⇪</span>`}
          ${item.title && html`<span class="clip-title"><${Segments} segs=${v.title} /></span>`}
          <span class="clip-ts">${formatTS(item.ts)}</span>
        </span>
        <button class="clip-delete" onClick=${() => deleteClip(item.id)} title="Delete">✕</button>
      </div>
      <div class="clip-text"><${Segments} segs=${v.body} /></div>
      ${atts.length > 0 && html`
        <div class="clip-attachments">
          ${atts.map((att) => html`<${Thumb} key=${att.id} att=${att} item=${item} segs=${v.captions[att.id]}
                                    editing=${state.clipEditId === item.id} />`)}
        </div>`}
      <div class="clip-footer">
        <button class="clip-action" onClick=${() => downloadClip(item)} title="Download note">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>
        </button>
        <button class="clip-action" onClick=${() => copyClip(item.data)} title="Copy to clipboard">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
        </button>
        <button class="clip-action" onClick=${() => props.onEdit(item)} title="Edit note">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17 3a2.828 2.828 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z"></path></svg>
        </button>
      </div>
    </div>`;
}

function Lightbox() {
  useEffect(() => {
    function onKey(e) { if (e.key === 'Escape') state.clipLightbox = null; }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  var zoom = state.clipLightboxZoom;
  var lb = state.clipLightbox;
  return html`
    <div class="lightbox ${zoom ? 'lightbox-scroll' : ''}"
         onClick=${(e) => { if (e.target === e.currentTarget) state.clipLightbox = null; }}>
      <div class="lightbox-actions">
        <button class="lightbox-btn" onClick=${() => saveURL(lb.url, lb.fileName)} title=${'Download as ' + lb.fileName}>${DOWNLOAD_ICON}</button>
        <button class="lightbox-btn lightbox-close" onClick=${() => (state.clipLightbox = null)} title="Close (Esc)">✕</button>
      </div>
      <img src=${lb.url} class=${zoom ? 'lightbox-zoomed' : ''} title=${zoom ? 'Click to fit' : 'Click to zoom'}
           onClick=${(e) => { e.stopPropagation(); state.clipLightboxZoom = !zoom; }} alt="" />
      ${lb.caption && html`<div class="lightbox-caption">${lb.caption}</div>`}
    </div>`;
}

export function ClipboardTab() {
  var textareaRef = useRef(null);
  var visible = visibleClipItems();
  function onEdit(item) {
    editClip(item);
    textareaRef.current.focus();
  }
  return html`
    <div>
      ${state.clipError && html`<div class="error">${state.clipError}</div>`}
      <${Composer} textareaRef=${textareaRef} />
      <div class="clip-list">
        ${state.clipItems.length > 0 && html`<${ListHeader} />`}
        ${visible.map((v) => html`<${Note} key=${v.item.id} v=${v} onEdit=${onEdit} />`)}
        ${!state.clipError && state.clipItems.length === 0 && html`<div class="upload-empty">No notes yet.</div>`}
        ${state.clipItems.length > 0 && visible.length === 0 && html`<div class="upload-empty">No notes match.</div>`}
      </div>
      ${state.clipLightbox && html`<${Lightbox} />`}
    </div>`;
}
