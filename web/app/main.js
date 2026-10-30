import { render } from './vendor/preact.module.js';
import { effect } from './vendor/signals.module.js';
import { html } from './components/html.js';
import { settings } from './store.js';
import { initStore } from './store.js';
import { load } from './lib/download.js';
import { refreshActive, onAuthKeyChange } from './nav.js';
import { App } from './components/app.js';

// pdf.js needs its worker path set before any getDocument call.
pdfjsLib.GlobalWorkerOptions.workerSrc = 'pdf.worker.min.js';

var refreshTimer = null;

async function boot() {
  await initStore(onAuthKeyChange);
  load(); // initial download listing

  // Auto-refresh: poll the active tab every second while enabled.
  effect(function() {
    var on = settings.autoRefresh; // subscribe
    clearInterval(refreshTimer);
    refreshTimer = on ? setInterval(refreshActive, 1000) : null;
  });

  render(html`<${App} />`, document.getElementById('app'));
}

boot();
