// Tab navigation + the shared refresh dispatch. Tab-specific loaders are
// wired in as each tab is ported.
import { state } from './store.js';
import { load } from './lib/download.js';
import { loadUploadDir } from './lib/upload.js';
import { pollClipboard, resetClipSync } from './lib/clipboard.js';

export function refreshActive() {
  if (state.tab === 'download') load();
  if (state.tab === 'upload') loadUploadDir();
  if (state.tab === 'clipboard') pollClipboard();
}

export function setTab(t) {
  state.tab = t;
  if (t === 'upload') loadUploadDir();
  if (t === 'clipboard') pollClipboard();
}

// The new key may address a different server/DB: reload the download listing
// (kept across tab switches) and resync the clipboard from scratch.
export function onAuthKeyChange() {
  resetClipSync();
  load();
  if (state.tab !== 'download') refreshActive();
}
