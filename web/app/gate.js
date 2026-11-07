// Login gate: the only code loaded before authentication. The app (main.js
// and everything it imports) is fetched once the key is accepted.
import { deriveKey, authToken } from './lib/authhash.js';
import { API_BASE, loadAuthK, saveAuthK, dropAuthK, takeRejected, isBlankRefusal, reRegister } from './lib/authsession.js';

const MSG_REJECTED = 'The server rejected the key; enter the passphrase again.';
const MSG_WRONG = 'Wrong passphrase.';
const MSG_BLOCKED = 'Too many failed attempts from this IP; wait up to 10 minutes or restart the server.';

var gate = document.getElementById('gate');
var form = document.getElementById('gate-form');
var input = document.getElementById('gate-pass');
var toggle = document.getElementById('gate-toggle');
var submitBtn = document.getElementById('gate-submit');
var msg = document.getElementById('gate-msg');

var settingsPromise = null;
var busy = false;

function setMessage(text, isError) {
  msg.textContent = text || '';
  msg.classList.toggle('gate-error', !!isError);
}

function setBusy(b) {
  busy = b;
  input.disabled = b;
  submitBtn.disabled = b;
}

function showForm(text, isError) {
  gate.hidden = false;
  setBusy(false);
  setMessage(text, isError);
  input.focus();
}

async function fetchAdmitted(url, opts) {
  var res = await fetch(url, opts);
  if (isBlankRefusal(res) && await reRegister()) res = await fetch(url, opts);
  if (isBlankRefusal(res)) throw new Error('The server refused the request (IP not admitted?).');
  return res;
}

function loadPublicSettings() {
  if (!settingsPromise) {
    settingsPromise = fetchAdmitted(API_BASE + '/api/public/settings')
      .then(function(res) {
        if (!res.ok) throw new Error('HTTP ' + res.status);
        return res.json();
      })
      .then(function(data) {
        if (data.auth !== false && !/^[0-9a-f]+$/.test(data.kdfSalt || '')) throw new Error('invalid key salt');
        return data;
      })
      .catch(function(e) {
        settingsPromise = null;
        throw new Error('Could not load the server settings: ' + e.message);
      });
  }
  return settingsPromise;
}

// 'ok', 'rejected' or 'blocked'; throws on anything else.
async function checkKey(k) {
  var res = await fetchAdmitted(API_BASE + '/api/auth/check', { method: 'POST', headers: { 'X-Auth': authToken(k) } });
  if (res.status === 204) return 'ok';
  if (res.status === 401) return 'rejected';
  if (res.status === 429) return 'blocked';
  throw new Error('Key check failed: HTTP ' + res.status);
}

function loadStyle(href) {
  return new Promise(function(resolve, reject) {
    var link = document.createElement('link');
    link.rel = 'stylesheet';
    link.href = href;
    link.onload = resolve;
    link.onerror = function() { reject(new Error('could not load ' + href)); };
    document.head.appendChild(link);
  });
}

async function startApp(publicSettings) {
  setBusy(true);
  setMessage('Loading…');
  try {
    // aes.js and pdf-lib are classic scripts that set globals the app uses.
    await Promise.all([loadStyle('static/style.css'), import('../aes.js'), import('../pdf-lib.min.js')]);
    var app = await import('./main.js');
    gate.remove();
    app.start(publicSettings);
  } catch (e) {
    showForm('Could not load the app: ' + e.message, true);
  }
}

async function submit(e) {
  e.preventDefault();
  if (busy) return;
  var passphrase = input.value;
  if (!passphrase) { setMessage('Enter the passphrase.', true); return; }
  setBusy(true);
  try {
    var publicSettings = await loadPublicSettings();
    setMessage('Deriving key…');
    var k = await deriveKey(passphrase, publicSettings.kdfSalt, function(p) {
      setMessage('Deriving key… ' + Math.round(p * 100) + '%');
    });
    var result = await checkKey(k);
    if (result === 'ok') {
      input.value = '';
      saveAuthK(k);
      await startApp(publicSettings);
      return;
    }
    showForm(result === 'blocked' ? MSG_BLOCKED : MSG_WRONG, true);
  } catch (err) {
    showForm(err.message, true);
  }
}

async function boot() {
  var rejected = takeRejected();
  fetch(API_BASE + '/api/public/checkin').catch(function() {});
  try {
    var publicSettings = await loadPublicSettings();
    if (publicSettings.auth === false) { await startApp(publicSettings); return; }
    var k = loadAuthK();
    if (!k) { showForm(rejected ? MSG_REJECTED : '', rejected); return; }
    var result = await checkKey(k);
    if (result === 'ok') { await startApp(publicSettings); return; }
    dropAuthK();
    showForm(result === 'blocked' ? MSG_BLOCKED : MSG_REJECTED, true);
  } catch (err) {
    showForm(err.message, true);
  }
}

form.addEventListener('submit', submit);
toggle.addEventListener('click', function() {
  var masked = input.classList.toggle('key-masked');
  toggle.title = masked ? 'Show passphrase' : 'Hide passphrase';
});

boot();
