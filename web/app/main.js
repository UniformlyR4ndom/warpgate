import { render } from './vendor/preact.module.js';
import { effect } from './vendor/signals.module.js';
import { html } from './components/html.js';
import { settings } from './store.js';
import { initStore } from './store.js';
import { load } from './lib/download.js';
import { initOutbox } from './lib/outbox.js';
import { refreshActive, onAuthKeyChange, restoreTab } from './nav.js';
import { App } from './components/app.js';
import { GlobalWorkerOptions } from '../pdf.min.mjs';

var PDF_WORKER = 'static/pdf.worker.min.mjs';
var workerURL = window.trustedTypes
  ? trustedTypes.createPolicy('pdfjs-worker', {
      createScriptURL: function(url) {
        if (url !== PDF_WORKER) throw new TypeError('untrusted script URL: ' + url);
        return url;
      },
    }).createScriptURL(PDF_WORKER)
  : PDF_WORKER;
// pdf.js would construct the worker from a plain string, which Trusted Types rejects.
GlobalWorkerOptions.workerPort = new Worker(workerURL, { type: 'module' });

var refreshTimer = null;

async function boot() {
  await initStore(onAuthKeyChange);
  load('dl'); // initial download listing
  restoreTab();

  // Auto-refresh: poll the active tab every second while enabled.
  effect(function() {
    var on = settings.autoRefresh; // subscribe
    clearInterval(refreshTimer);
    refreshTimer = on ? setInterval(refreshActive, 1000) : null;
  });

  render(html`<${App} />`, document.getElementById('app'));
  initOutbox();
}

boot();
