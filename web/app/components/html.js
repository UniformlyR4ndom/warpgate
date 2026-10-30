import { h, Fragment } from '../vendor/preact.module.js';
import htm from '../vendor/htm.module.js';

export const html = htm.bind(h);
export { h, Fragment };

// Two-way-binding prop spreads for controlled inputs over a (deep)signal object.
export function text(obj, key) {
  return { value: obj[key], onInput: function(e) { obj[key] = e.target.value; } };
}
export function num(obj, key) {
  return { value: obj[key], onInput: function(e) {
    var v = e.target.valueAsNumber;
    obj[key] = Number.isNaN(v) ? e.target.value : v;
  } };
}
export function check(obj, key) {
  return { checked: obj[key], onChange: function(e) { obj[key] = e.target.checked; } };
}
export function sel(obj, key) {
  return { value: obj[key], onChange: function(e) { obj[key] = e.target.value; } };
}
