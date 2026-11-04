// Corner toast notices. A success (ok) is green and fades after a few seconds;
// anything else is red and stays until dismissed, unless sticky says otherwise.
import { state } from '../store.js';

var AUTO_DISMISS_MS = 5000;
var seq = 0;

export function addToast(ok, text, opts) {
  var sticky = opts && opts.sticky !== undefined ? opts.sticky : !ok;
  var id = ++seq;
  state.toasts.push({ id: id, ok: ok, text: text });
  if (!sticky) setTimeout(function() { dismissToast(id); }, AUTO_DISMISS_MS);
  return id;
}

export function dismissToast(id) {
  var i = state.toasts.findIndex(function(t) { return t.id === id; });
  if (i >= 0) state.toasts.splice(i, 1);
}
