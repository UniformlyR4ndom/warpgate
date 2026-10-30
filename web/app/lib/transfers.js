// In-flight transfer list (shown with progress bars on the Download/Upload tabs).
import { state } from '../store.js';

export function addTransfer(direction, name) {
  var t = { id: Date.now() + '-' + Math.random().toString(36).slice(2), direction: direction, name: name, progress: null };
  state.transfers.push(t);
  // Read the pushed element back so later mutations go through deepSignal's proxy.
  return state.transfers[state.transfers.length - 1];
}

export function removeTransfer(t) {
  var idx = state.transfers.indexOf(t);
  if (idx !== -1) { state.transfers.splice(idx, 1); }
}

export function makeTransferProgress(transfer, totalSize) {
  var bytesDone = 0;
  return function(n) {
    bytesDone += n;
    transfer.progress = totalSize > 0 ? Math.min(1, bytesDone / totalSize) : null;
  };
}

// Replaces state[prop] only on an actual change, so polling doesn't re-render.
export function updateIfChanged(prop, data) {
  if (JSON.stringify(data) !== JSON.stringify(state[prop])) { state[prop] = data; }
}
