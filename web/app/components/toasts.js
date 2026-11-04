import { html } from './html.js';
import { state } from '../store.js';
import { dismissToast } from '../lib/notices.js';

export function Toasts() {
  return html`
    <div class="notices">
      ${state.toasts.map((t) => html`
        <div key=${t.id} class="notice ${t.ok ? 'notice-success' : 'notice-error'}">
          <span>${t.text}</span>
          <button class="notice-close" onClick=${() => dismissToast(t.id)} title="Dismiss">✕</button>
        </div>`)}
    </div>`;
}
