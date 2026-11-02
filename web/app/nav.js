// Tab navigation + the shared refresh dispatch. Tab-specific loaders are
// wired in as each tab is ported.
import { state } from './store.js';
import { load } from './lib/download.js';
import { refreshUpload } from './lib/upload.js';
import { pollClipboard, resetClipSync } from './lib/clipboard.js';

const TABS = [
  { id: 'download',  label: 'Download' },
  { id: 'upload',    label: 'Upload' },
  { id: 'clipboard', label: 'Clipboard' },
  { id: 'benchmark', label: 'Benchmark' },
];

// Per browser tab, like the auth key: a reload keeps the tab, a new browser tab starts on Download.
const TAB_STORAGE_KEY = 'warpgate-tab';

export function visibleTabs() {
  return TABS.filter((t) => t.id !== 'upload' || state.uploads);
}

export function refreshActive() {
  if (state.tab === 'download') load('dl');
  if (state.tab === 'upload') refreshUpload();
  if (state.tab === 'clipboard') pollClipboard();
}

export function setTab(t) {
  state.tab = t;
  sessionStorage.setItem(TAB_STORAGE_KEY, t);
  if (t === 'upload') refreshUpload();
  if (t === 'clipboard') pollClipboard();
}

export function restoreTab() {
  var t = sessionStorage.getItem(TAB_STORAGE_KEY);
  if (t && t !== 'download' && visibleTabs().some((x) => x.id === t)) setTab(t);
}

// The new key may address a different server/DB: reload the download listing
// (kept across tab switches) and resync the clipboard from scratch.
export function onAuthKeyChange() {
  resetClipSync();
  load('dl');
  if (state.tab !== 'download') refreshActive();
}
