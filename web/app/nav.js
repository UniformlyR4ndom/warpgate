// Tab navigation + the shared refresh dispatch. Tab-specific loaders are
// wired in as each tab is ported.
import { state } from './store.js';
import { load } from './lib/download.js';
import { refreshUpload } from './lib/upload.js';
import { pollClipboard, resetClipSync } from './lib/clipboard.js';

export function refreshActive() {
  if (state.tab === 'download') load('dl');
  if (state.tab === 'upload') refreshUpload();
  if (state.tab === 'clipboard') pollClipboard();
}

export function setTab(t) {
  state.tab = t;
  if (t === 'upload') refreshUpload();
  if (t === 'clipboard') pollClipboard();
}

// The new key may address a different server/DB: reload the download listing
// (kept across tab switches) and resync the clipboard from scratch.
export function onAuthKeyChange() {
  resetClipSync();
  load('dl');
  if (state.tab !== 'download') refreshActive();
}
