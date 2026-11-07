// Tab navigation + the shared refresh dispatch. Tab-specific loaders are
// wired in as each tab is ported.
import { state, settings } from './store.js';
import { load } from './lib/download.js';
import { refreshUpload } from './lib/upload.js';
import { pollClipboard } from './lib/clipboard.js';

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
  refreshListings();
  if (state.tab === 'clipboard') pollClipboard();
}

// The clipboard runs its own poll loop while auto-refresh is on.
export function refreshListings() {
  if (state.tab === 'download') load('dl');
  if (state.tab === 'upload') refreshUpload();
}

export function setTab(t) {
  state.tab = t;
  sessionStorage.setItem(TAB_STORAGE_KEY, t);
  if (t === 'upload') refreshUpload();
  if (t === 'clipboard' && !settings.autoRefresh) pollClipboard();
}

export function restoreTab() {
  var t = sessionStorage.getItem(TAB_STORAGE_KEY);
  if (t && t !== 'download' && visibleTabs().some((x) => x.id === t)) setTab(t);
}
